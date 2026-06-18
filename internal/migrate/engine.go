package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/themobileprof/megadbsync/internal/dbconn"
	"github.com/themobileprof/megadbsync/internal/store"
)

type Engine struct {
	Store      *store.Store
	OnProgress func()
}

type syncState struct {
	watermark string
	maxKey    string
	scn       int64
	lastRowID string
}

func (e *Engine) RunBulk(ctx context.Context, job store.Job, src, dst store.Connection, srcPass, dstPass string) error {
	ora, err := dbconn.OpenOracle(ctx, src, srcPass)
	if err != nil {
		return fmt.Errorf("oracle connect: %w", err)
	}
	defer ora.Close()
	dbconn.ConfigurePool(ora, job.ParallelTables)

	mssqlDB, err := dbconn.OpenMSSQL(ctx, dst, dstPass)
	if err != nil {
		return fmt.Errorf("mssql connect: %w", err)
	}
	defer mssqlDB.Close()
	dbconn.ConfigurePool(mssqlDB, job.ParallelTables)

	count, err := dbconn.DestinationMustBeEmpty(ctx, mssqlDB, dst.Schema)
	if err != nil {
		return fmt.Errorf("destination check: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("destination database is not empty (%d tables found); bulk migration refused to protect existing data", count)
	}

	owner := strings.ToUpper(src.Schema)
	tables, err := dbconn.ListOracleTables(ctx, ora, owner)
	if err != nil {
		return err
	}
	if job.TableFilter != "" {
		tables = filterTables(tables, job.TableFilter)
	}

	job.TablesTotal = len(tables)
	job.Status = store.JobRunning
	now := time.Now().UTC()
	job.StartedAt = &now
	job.CurrentPhase = "schema"
	_ = e.Store.UpdateJob(job)
	_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("Starting bulk migration: %d tables", len(tables)))

	metas := make([]dbconn.TableMeta, 0, len(tables))
	for _, t := range tables {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		meta, err := dbconn.LoadOracleTableMeta(ctx, ora, t.Schema, t.Name)
		if err != nil {
			return err
		}
		if dst.Schema != "" && !strings.EqualFold(meta.Schema, dst.Schema) {
			meta.Schema = dst.Schema
		}
		job.CurrentTable = meta.Schema + "." + meta.Name
		_ = e.Store.UpdateJob(job)
		if err := dbconn.CreateMSSQLTable(ctx, mssqlDB, meta); err != nil {
			return fmt.Errorf("create table %s: %w", meta.Name, err)
		}
		metas = append(metas, meta)
	}

	job.CurrentPhase = "data"
	_ = e.Store.UpdateJob(job)

	parallel := max(1, job.ParallelTables)
	sem := make(chan struct{}, parallel)
	errCh := make(chan error, len(metas))
	done := make(chan struct{}, len(metas))
	var rowsDone atomic.Int64

	for _, meta := range metas {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sem <- struct{}{}:
		}
		tableMeta := meta
		go func() {
			defer func() { <-sem; done <- struct{}{} }()
			task := store.TableTask{
				JobID: job.ID, SchemaName: tableMeta.Schema, TableName: tableMeta.Name,
				Status: store.JobRunning, SyncMode: tableMeta.SyncMode, WatermarkCol: tableMeta.WatermarkCol,
			}
			start := time.Now().UTC()
			task.StartedAt = &start
			_ = e.Store.UpsertTableTask(task)

			n, err := e.copyTable(ctx, ora, mssqlDB, tableMeta, job.BatchSize)
			if err != nil {
				task.Status = store.JobFailed
				task.ErrorMessage = err.Error()
				_ = e.Store.UpsertTableTask(task)
				errCh <- fmt.Errorf("%s.%s: %w", tableMeta.Schema, tableMeta.Name, err)
				return
			}
			rowsDone.Add(n)
			end := time.Now().UTC()
			task.CompletedAt = &end
			task.Status = store.JobCompleted
			task.RowsDone = n
			task.RowsTotal = n
			if end.Sub(start).Seconds() > 0 {
				task.RowsPerSec = float64(n) / end.Sub(start).Seconds()
			}
			_ = e.Store.UpsertTableTask(task)

			high, err := e.captureSyncHighWater(ctx, ora, tableMeta)
			if err != nil {
				_ = e.Store.LogEvent(job.ID, "error", fmt.Sprintf("sync state for %s.%s: %v", tableMeta.Schema, tableMeta.Name, err))
			} else {
				_ = e.Store.UpsertSyncState(job.SourceID, job.DestID, tableMeta.Schema, tableMeta.Name,
					tableMeta.SyncMode, tableMeta.WatermarkCol, high.watermark, high.maxKey, high.scn)
			}
		}()
	}

	for i := 0; i < len(metas); i++ {
		select {
		case err := <-errCh:
			return err
		case <-done:
			job.TablesDone++
			job.RowsDone = rowsDone.Load()
			_ = e.Store.UpdateJob(job)
			if e.OnProgress != nil {
				e.OnProgress()
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	job.Status = store.JobCompleted
	job.CurrentPhase = "done"
	end := time.Now().UTC()
	job.CompletedAt = &end
	_ = e.Store.UpdateJob(job)
	_ = e.Store.LogEvent(job.ID, "info", "Bulk migration completed")
	return nil
}

func (e *Engine) RunIncremental(ctx context.Context, job store.Job, src, dst store.Connection, srcPass, dstPass string) error {
	ora, err := dbconn.OpenOracle(ctx, src, srcPass)
	if err != nil {
		return err
	}
	defer ora.Close()
	dbconn.ConfigurePool(ora, job.ParallelTables)

	mssqlDB, err := dbconn.OpenMSSQL(ctx, dst, dstPass)
	if err != nil {
		return err
	}
	defer mssqlDB.Close()
	dbconn.ConfigurePool(mssqlDB, job.ParallelTables)

	owner := strings.ToUpper(src.Schema)
	tableList, err := dbconn.ListOracleTables(ctx, ora, owner)
	if err != nil {
		return err
	}
	if job.TableFilter != "" {
		tableList = filterTables(tableList, job.TableFilter)
	}

	job.TablesTotal = len(tableList)
	job.Status = store.JobRunning
	now := time.Now().UTC()
	job.StartedAt = &now
	job.CurrentPhase = "incremental"
	_ = e.Store.UpdateJob(job)
	_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("Starting incremental sync: %d tables", len(tableList)))

	type tableWork struct {
		meta  dbconn.TableMeta
		state syncState
	}
	works := make([]tableWork, 0, len(tableList))
	for _, t := range tableList {
		meta, err := dbconn.LoadOracleTableMeta(ctx, ora, t.Schema, t.Name)
		if err != nil {
			return err
		}
		if dst.Schema != "" {
			meta.Schema = dst.Schema
		}
		mode, wmCol, wm, maxKey, scn, _ := e.Store.GetSyncState(job.SourceID, job.DestID, meta.Schema, meta.Name)
		if mode != "" {
			meta.SyncMode = mode
			meta.WatermarkCol = wmCol
		}
		works = append(works, tableWork{
			meta:  meta,
			state: syncState{watermark: wm, maxKey: maxKey, scn: scn},
		})
	}

	parallel := max(1, job.ParallelTables)
	sem := make(chan struct{}, parallel)
	errCh := make(chan error, len(works))
	done := make(chan int64, len(works))
	var tablesDone atomic.Int32
	var rowsDone atomic.Int64

	for _, work := range works {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sem <- struct{}{}:
		}
		w := work
		go func() {
			defer func() { <-sem }()
			meta := w.meta
			task := store.TableTask{
				JobID: job.ID, SchemaName: meta.Schema, TableName: meta.Name,
				Status: store.JobRunning, SyncMode: meta.SyncMode, WatermarkCol: meta.WatermarkCol,
				LastWatermark: w.state.watermark, LastMaxKey: w.state.maxKey, LastSCN: w.state.scn,
			}
			start := time.Now().UTC()
			task.StartedAt = &start
			_ = e.Store.UpsertTableTask(task)

			n, newState, err := e.syncTableIncremental(ctx, job.SourceID, job.DestID, ora, mssqlDB, meta, job.BatchSize, w.state, &task)
			if err != nil {
				task.Status = store.JobFailed
				task.ErrorMessage = err.Error()
				_ = e.Store.UpsertTableTask(task)
				_ = e.Store.LogEvent(job.ID, "error", fmt.Sprintf("%s.%s: %v", meta.Schema, meta.Name, err))
				errCh <- err
				return
			}
			end := time.Now().UTC()
			task.CompletedAt = &end
			task.Status = store.JobCompleted
			task.RowsDone = n
			task.RowsTotal = n
			if end.Sub(start).Seconds() > 0 {
				task.RowsPerSec = float64(n) / end.Sub(start).Seconds()
			}
			task.LastWatermark = newState.watermark
			task.LastMaxKey = newState.maxKey
			task.LastSCN = newState.scn
			_ = e.Store.UpsertTableTask(task)
			_ = e.Store.UpsertSyncState(job.SourceID, job.DestID, meta.Schema, meta.Name,
				meta.SyncMode, meta.WatermarkCol, newState.watermark, newState.maxKey, newState.scn)
			done <- n
		}()
	}

	for i := 0; i < len(works); i++ {
		select {
		case err := <-errCh:
			return err
		case n := <-done:
			tablesDone.Add(1)
			rowsDone.Add(n)
			job.TablesDone = int(tablesDone.Load())
			job.RowsDone = rowsDone.Load()
			_ = e.Store.UpdateJob(job)
			if e.OnProgress != nil {
				e.OnProgress()
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	job.Status = store.JobCompleted
	job.CurrentPhase = "done"
	end := time.Now().UTC()
	job.CompletedAt = &end
	_ = e.Store.UpdateJob(job)
	_ = e.Store.LogEvent(job.ID, "info", "Incremental sync completed")
	return nil
}

func (e *Engine) syncTableIncremental(ctx context.Context, sourceID, destID string, ora, mssqlDB *sql.DB, meta dbconn.TableMeta, batch int, state syncState, task *store.TableTask) (int64, syncState, error) {
	var total int64
	cur := state
	var lastUI time.Time
	for {
		select {
		case <-ctx.Done():
			return total, cur, ctx.Err()
		default:
		}
		rows, cols, next, err := e.fetchIncremental(ctx, ora, meta, batch, cur)
		if err != nil {
			return total, cur, err
		}
		if len(rows) == 0 {
			break
		}
		n, err := dbconn.MergeUpsertMSSQL(ctx, mssqlDB, meta.Schema, meta.Name, cols, meta.PrimaryKeys, rows)
		if err != nil {
			return total, cur, err
		}
		total += n
		cur = next

		_ = e.Store.UpsertSyncState(sourceID, destID, meta.Schema, meta.Name,
			meta.SyncMode, meta.WatermarkCol, cur.watermark, cur.maxKey, cur.scn)

		if task != nil && time.Since(lastUI) >= 3*time.Second {
			task.RowsDone = total
			task.LastWatermark = cur.watermark
			task.LastMaxKey = cur.maxKey
			task.LastSCN = cur.scn
			_ = e.Store.UpsertTableTask(*task)
			lastUI = time.Now()
			if e.OnProgress != nil {
				e.OnProgress()
			}
		}

		if len(rows) < batch {
			break
		}
	}
	return total, cur, nil
}

func (e *Engine) copyTable(ctx context.Context, ora, mssqlDB *sql.DB, meta dbconn.TableMeta, batch int) (int64, error) {
	colNames := make([]string, len(meta.Columns))
	for i, c := range meta.Columns {
		colNames[i] = c.Name
	}
	var total int64
	var lastRowID string
	for {
		rows, nextRowID, err := e.fetchFullChunk(ctx, ora, meta, colNames, batch, lastRowID)
		if err != nil {
			return total, err
		}
		if len(rows) == 0 {
			break
		}
		n, err := dbconn.BulkInsertMSSQL(ctx, mssqlDB, meta.Schema, meta.Name, colNames, rows)
		if err != nil {
			return total, err
		}
		total += n
		lastRowID = nextRowID
		if len(rows) < batch {
			break
		}
	}
	return total, nil
}

func (e *Engine) captureSyncHighWater(ctx context.Context, ora *sql.DB, meta dbconn.TableMeta) (syncState, error) {
	var st syncState
	schema := quoteOracleIdent(meta.Schema)
	table := quoteOracleIdent(meta.Name)

	switch meta.SyncMode {
	case "watermark":
		if meta.WatermarkCol == "" {
			return st, fmt.Errorf("no watermark column")
		}
		q := fmt.Sprintf(`SELECT MAX(%s) FROM %s.%s`, quoteOracleIdent(meta.WatermarkCol), schema, table)
		var v any
		if err := ora.QueryRowContext(ctx, q).Scan(&v); err != nil {
			return st, err
		}
		if v != nil {
			st.watermark = fmt.Sprint(v)
		}
	case "max_key":
		if len(meta.PrimaryKeys) == 0 {
			return st, fmt.Errorf("no primary key for max_key mode")
		}
		pk := quoteOracleIdent(meta.PrimaryKeys[0])
		q := fmt.Sprintf(`SELECT MAX(%s) FROM %s.%s`, pk, schema, table)
		var v any
		if err := ora.QueryRowContext(ctx, q).Scan(&v); err != nil {
			return st, err
		}
		if v != nil {
			st.maxKey = fmt.Sprint(v)
		}
	default:
		q := fmt.Sprintf(`SELECT MAX(ORA_ROWSCN) FROM %s.%s`, schema, table)
		var v sql.NullInt64
		if err := ora.QueryRowContext(ctx, q).Scan(&v); err != nil {
			return st, err
		}
		if v.Valid {
			st.scn = v.Int64
		}
	}
	return st, nil
}

func (e *Engine) fetchFullChunk(ctx context.Context, ora *sql.DB, meta dbconn.TableMeta, cols []string, batch int, lastRowID string) ([][]any, string, error) {
	colList := strings.Join(quoteOracleCols(cols), ", ")
	schema := quoteOracleIdent(meta.Schema)
	table := quoteOracleIdent(meta.Name)

	var q string
	var args []any
	if lastRowID == "" {
		q = fmt.Sprintf(`SELECT %s, ROWIDTOCHAR(ROWID) AS MDAS_RID FROM %s.%s ORDER BY ROWID FETCH NEXT %d ROWS ONLY`,
			colList, schema, table, batch)
	} else {
		q = fmt.Sprintf(`SELECT %s, ROWIDTOCHAR(ROWID) AS MDAS_RID FROM %s.%s WHERE ROWID > CHARTOROWID(:1) ORDER BY ROWID FETCH NEXT %d ROWS ONLY`,
			colList, schema, table, batch)
		args = []any{lastRowID}
	}

	raw, err := queryRows(ctx, ora, q, args...)
	if err != nil {
		return nil, "", err
	}
	if len(raw) == 0 {
		return nil, lastRowID, nil
	}

	nextRowID := fmt.Sprint(raw[len(raw)-1][len(cols)])
	rows := make([][]any, len(raw))
	for i, r := range raw {
		rows[i] = r[:len(cols)]
	}
	return rows, nextRowID, nil
}

func (e *Engine) fetchIncremental(ctx context.Context, ora *sql.DB, meta dbconn.TableMeta, batch int, state syncState) ([][]any, []string, syncState, error) {
	cols := make([]string, len(meta.Columns))
	for i, c := range meta.Columns {
		cols[i] = c.Name
	}
	colList := strings.Join(quoteOracleCols(cols), ", ")

	var q string
	var args []any
	next := state

	switch meta.SyncMode {
	case "watermark":
		q = fmt.Sprintf(`SELECT %s FROM %s.%s WHERE %s > :1 ORDER BY %s FETCH NEXT %d ROWS ONLY`,
			colList, quoteOracleIdent(meta.Schema), quoteOracleIdent(meta.Name),
			quoteOracleIdent(meta.WatermarkCol), quoteOracleIdent(meta.WatermarkCol), batch)
		args = []any{state.watermark}
	case "max_key":
		pk := meta.PrimaryKeys[0]
		q = fmt.Sprintf(`SELECT %s FROM %s.%s WHERE %s > :1 ORDER BY %s FETCH NEXT %d ROWS ONLY`,
			colList, quoteOracleIdent(meta.Schema), quoteOracleIdent(meta.Name),
			quoteOracleIdent(pk), quoteOracleIdent(pk), batch)
		args = []any{state.maxKey}
	default:
		q = fmt.Sprintf(`SELECT %s, ORA_ROWSCN AS MDAS_ROWSCN FROM %s.%s WHERE ORA_ROWSCN > :1 ORDER BY ORA_ROWSCN FETCH NEXT %d ROWS ONLY`,
			colList, quoteOracleIdent(meta.Schema), quoteOracleIdent(meta.Name), batch)
		args = []any{state.scn}
	}

	rows, err := queryRows(ctx, ora, q, args...)
	if err != nil {
		return nil, cols, next, err
	}
	if len(rows) == 0 {
		return rows, cols, next, nil
	}

	switch meta.SyncMode {
	case "watermark":
		idx := colIndex(cols, meta.WatermarkCol)
		if idx >= 0 {
			next.watermark = fmt.Sprint(rows[len(rows)-1][idx])
		}
	case "max_key":
		idx := colIndex(cols, meta.PrimaryKeys[0])
		if idx >= 0 {
			next.maxKey = fmt.Sprint(rows[len(rows)-1][idx])
		}
	default:
		last := rows[len(rows)-1][len(rows[len(rows)-1])-1]
		switch v := last.(type) {
		case int64:
			next.scn = v
		case float64:
			next.scn = int64(v)
		default:
			fmt.Sscan(fmt.Sprint(v), &next.scn)
		}
		for i := range rows {
			rows[i] = rows[i][:len(cols)]
		}
	}
	return rows, cols, next, nil
}

func queryRows(ctx context.Context, db *sql.DB, q string, args ...any) ([][]any, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := make([][]any, 0, 1024)
	holders := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range holders {
		ptrs[i] = &holders[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]any, len(cols))
		copy(row, holders)
		out = append(out, row)
	}
	return out, rows.Err()
}

func filterTables(tables []dbconn.TableMeta, filter string) []dbconn.TableMeta {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return tables
	}
	parts := strings.Split(filter, ",")
	want := make(map[string]bool)
	for _, p := range parts {
		want[strings.ToUpper(strings.TrimSpace(p))] = true
	}
	var out []dbconn.TableMeta
	for _, t := range tables {
		key := strings.ToUpper(t.Schema + "." + t.Name)
		if want[key] || want[strings.ToUpper(t.Name)] {
			out = append(out, t)
		}
	}
	return out
}

func quoteOracleIdent(s string) string {
	return `"` + strings.ReplaceAll(strings.ToUpper(s), `"`, `""`) + `"`
}

func quoteOracleCols(cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = quoteOracleIdent(c)
	}
	return out
}

func colIndex(cols []string, name string) int {
	for i, c := range cols {
		if strings.EqualFold(c, name) {
			return i
		}
	}
	return -1
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
