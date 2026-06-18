package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
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
	settings, _ := e.Store.GetSettings()
	timeout := chunkTimeout(job, settings)
	rowCountCap := settings.DefaultRowCountFallbackCap

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

	existingTasks, _ := e.Store.ListTableTasks(job.ID)
	taskByKey := taskMap(existingTasks)
	resuming := job.StartedAt != nil && len(existingTasks) > 0

	if !resuming {
		count, err := dbconn.DestinationMustBeEmpty(ctx, mssqlDB, dst.Schema)
		if err != nil {
			return fmt.Errorf("destination check: %w", err)
		}
		if count > 0 {
			tables, _ := dbconn.ListDestinationTables(ctx, mssqlDB, dst.Schema)
			var resumableID, resumableStatus string
			if resumable, _ := e.Store.FindResumableBulkJob(job.SourceID, job.DestID); resumable != nil {
				resumableID = resumable.ID
				resumableStatus = string(resumable.Status)
			}
			return fmt.Errorf("%s", dbconn.FormatBulkBlockedError(dst.Schema, count, tables, resumableID, resumableStatus))
		}
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
	if job.StartedAt == nil {
		now := time.Now().UTC()
		job.StartedAt = &now
	}
	job.Status = store.JobRunning
	job.CurrentPhase = "schema"
	_ = e.Store.UpdateJob(job)
	if resuming {
		_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("Resuming bulk migration: %d tables", len(tables)))
	} else {
		_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("Starting bulk migration: %d tables (smallest first)", len(tables)))
	}

	metas := make([]dbconn.TableMeta, 0, len(tables))
	var rowsTotal int64
	for _, t := range tables {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		meta, err := dbconn.LoadOracleTableMeta(ctx, ora, t.Schema, t.Name, rowCountCap)
		if err != nil {
			return err
		}
		applyDestSchema(&meta, dst)
		rowsTotal += contributeRowTotal(meta)
		job.CurrentTable = destTableLabel(meta)
		_ = e.Store.UpdateJob(job)
		if err := dbconn.PrepareDestinationTable(ctx, mssqlDB, meta); err != nil {
			return fmt.Errorf("prepare table %s: %w", meta.Name, err)
		}
		metas = append(metas, meta)
	}
	sortTablesBySize(metas)
	job.RowsTotal = rowsTotal

	tablesDone, rowsDone := summarizeCompletedTasks(existingTasks)
	job.TablesDone = tablesDone
	job.RowsDone = rowsDone

	job.CurrentPhase = "data"
	_ = e.Store.UpdateJob(job)

	pending := make([]dbconn.TableMeta, 0, len(metas))
	for _, meta := range metas {
		key := tableTaskKey(meta.DestSchema, meta.Name)
		if t, ok := taskByKey[key]; ok && isTableWorkComplete(t.Status) {
			continue
		}
		pending = append(pending, meta)
	}

	parallel := max(1, job.ParallelTables)
	sem := make(chan struct{}, parallel)
	errCh := make(chan error, len(pending))
	done := make(chan struct{}, len(pending))
	var rowsDoneAtomic atomic.Int64
	rowsDoneAtomic.Store(rowsDone)
	var tablesDoneAtomic atomic.Int32
	tablesDoneAtomic.Store(int32(tablesDone))

	for _, meta := range pending {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sem <- struct{}{}:
		}
		tableMeta := meta
		existing := taskByKey[tableTaskKey(tableMeta.DestSchema, tableMeta.Name)]
		go func() {
			defer func() { <-sem; done <- struct{}{} }()
			task := store.TableTask{
				JobID: job.ID, SchemaName: tableMeta.DestSchema, TableName: tableMeta.Name,
				Status: store.JobRunning, SyncMode: tableMeta.SyncMode, WatermarkCol: tableMeta.WatermarkCol,
				LastRowID: existing.LastRowID, RowsDone: existing.RowsDone,
			}
			applyMetaRowCount(&task, tableMeta)
			if existing.ID != "" {
				task.ID = existing.ID
			}
			if existing.StartedAt != nil {
				task.StartedAt = existing.StartedAt
			} else {
				start := time.Now().UTC()
				task.StartedAt = &start
			}
			task.ErrorMessage = ""
			_ = e.Store.UpsertTableTask(task)

			tableRows, err := e.copyTable(ctx, ora, mssqlDB, tableMeta, job.BatchSize, existing.LastRowID, timeout, &task, tableCopyOpts{maxRows: job.MaxRowsPerTable})
			if err != nil {
				if ctx.Err() != nil {
					task.Status = store.JobPaused
					task.RowsDone = tableRows
					_ = e.Store.UpsertTableTask(task)
					return
				}
				task.Status = store.JobFailed
				task.ErrorMessage = err.Error()
				task.RowsDone = tableRows
				_ = e.Store.UpsertTableTask(task)
				errCh <- fmt.Errorf("%s: %w", destTableLabel(tableMeta), err)
				return
			}
			rowsDoneAtomic.Add(tableRows - existing.RowsDone)
			end := time.Now().UTC()
			task.CompletedAt = &end
			task.Status = store.JobCompleted
			task.RowsDone = tableRows
			task.RowsTotal = tableMeta.RowCount
			if task.RowsTotal < task.RowsDone {
				task.RowsTotal = task.RowsDone
			}
			elapsed := end.Sub(*task.StartedAt).Seconds()
			if elapsed > 0 {
				task.RowsPerSec = float64(task.RowsDone) / elapsed
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

	for i := 0; i < len(pending); i++ {
		select {
		case err := <-errCh:
			return err
		case <-done:
			tablesDoneAtomic.Add(1)
			job.TablesDone = int(tablesDoneAtomic.Load())
			job.RowsDone = rowsDoneAtomic.Load()
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
	settings, _ := e.Store.GetSettings()
	timeout := chunkTimeout(job, settings)
	rowCountCap := settings.DefaultRowCountFallbackCap

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

	existingTasks, _ := e.Store.ListTableTasks(job.ID)
	taskByKey := taskMap(existingTasks)
	resuming := job.StartedAt != nil && len(existingTasks) > 0

	job.TablesTotal = len(tableList)
	if job.StartedAt == nil {
		now := time.Now().UTC()
		job.StartedAt = &now
	}
	job.Status = store.JobRunning
	job.CurrentPhase = "scanning"
	_ = e.Store.UpdateJob(job)
	if resuming {
		_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("Resuming incremental sync: %d tables", len(tableList)))
	} else {
		_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("Starting incremental sync: %d tables — checking for changes since last watermark", len(tableList)))
	}

	type tableWork struct {
		meta  dbconn.TableMeta
		state syncState
	}
	works := make([]tableWork, 0, len(tableList))
	var rowsTotal int64
	for _, t := range tableList {
		meta, err := dbconn.LoadOracleTableMeta(ctx, ora, t.Schema, t.Name, rowCountCap)
		if err != nil {
			return err
		}
		applyDestSchema(&meta, dst)
		rowsTotal += contributeRowTotal(meta)
		mode, wmCol, wm, maxKey, scn, _ := e.Store.GetSyncState(job.SourceID, job.DestID, meta.Schema, meta.Name)
		if mode != "" {
			meta.SyncMode = mode
			meta.WatermarkCol = wmCol
		}
		key := tableTaskKey(meta.DestSchema, meta.Name)
		if resuming {
			if tsk, ok := taskByKey[key]; ok && isTableWorkComplete(tsk.Status) {
				continue
			}
		}
		st := syncState{watermark: wm, maxKey: maxKey, scn: scn}
		works = append(works, tableWork{meta: meta, state: st})
		job.CurrentTable = destTableLabel(meta)
		job.CurrentPhase = "scanning"
		_ = e.Store.UpdateJob(job)
	}
	if len(works) == 0 {
		if len(tableList) == 0 {
			return fmt.Errorf("no Oracle tables found to sync (check source schema filter)")
		}
		_ = e.Store.LogEvent(job.ID, "info", "All tables already checked in this run — nothing to resume")
		job.Status = store.JobCompleted
		job.CurrentPhase = "done"
		end := time.Now().UTC()
		job.CompletedAt = &end
		_ = e.Store.UpdateJob(job)
		return nil
	}
	sort.Slice(works, func(i, j int) bool {
		if works[i].meta.RowCount != works[j].meta.RowCount {
			return works[i].meta.RowCount < works[j].meta.RowCount
		}
		return works[i].meta.Name < works[j].meta.Name
	})

	tablesDone, rowsDone := summarizeCompletedTasks(existingTasks)
	job.RowsTotal = rowsTotal
	job.TablesDone = tablesDone
	job.RowsDone = rowsDone
	job.TablesTotal = len(works)
	if resuming {
		job.TablesTotal = tablesDone + len(works)
	}
	job.CurrentPhase = "syncing"
	_ = e.Store.UpdateJob(job)

	parallel := max(1, job.ParallelTables)
	sem := make(chan struct{}, parallel)
	errCh := make(chan error, len(works))
	done := make(chan int64, len(works))
	var tablesDoneAtomic atomic.Int32
	tablesDoneAtomic.Store(int32(tablesDone))
	var rowsDoneAtomic atomic.Int64
	rowsDoneAtomic.Store(rowsDone)

	for _, work := range works {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sem <- struct{}{}:
		}
		w := work
		existing := taskByKey[tableTaskKey(w.meta.DestSchema, w.meta.Name)]
		go func() {
			defer func() { <-sem }()
			meta := w.meta
			job.CurrentTable = destTableLabel(meta)
			job.CurrentPhase = "syncing"
			_ = e.Store.UpdateJob(job)
			_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("Checking %s (%s, mode %s)", destTableLabel(meta), syncStateSummary(meta, w.state), meta.SyncMode))
			task := store.TableTask{
				JobID: job.ID, SchemaName: meta.DestSchema, TableName: meta.Name,
				Status: store.JobRunning, SyncMode: meta.SyncMode, WatermarkCol: meta.WatermarkCol,
				LastWatermark: w.state.watermark, LastMaxKey: w.state.maxKey, LastSCN: w.state.scn,
				RowsDone: existing.RowsDone,
			}
			applyMetaRowCount(&task, meta)
			if existing.ID != "" {
				task.ID = existing.ID
			}
			if existing.StartedAt != nil {
				task.StartedAt = existing.StartedAt
			} else {
				start := time.Now().UTC()
				task.StartedAt = &start
			}
			task.ErrorMessage = ""
			_ = e.Store.UpsertTableTask(task)

			n, newState, err := e.syncTableIncremental(ctx, job.ID, job.SourceID, job.DestID, ora, mssqlDB, meta, job.BatchSize, w.state, timeout, &task)
			if err != nil {
				if ctx.Err() != nil {
					task.Status = store.JobPaused
					task.RowsDone = existing.RowsDone + n
					task.LastWatermark = newState.watermark
					task.LastMaxKey = newState.maxKey
					task.LastSCN = newState.scn
					_ = e.Store.UpsertTableTask(task)
					return
				}
				task.Status = store.JobFailed
				task.ErrorMessage = err.Error()
				_ = e.Store.UpsertTableTask(task)
				_ = e.Store.LogEvent(job.ID, "error", fmt.Sprintf("%s: %v", destTableLabel(meta), err))
				errCh <- err
				return
			}
			end := time.Now().UTC()
			task.CompletedAt = &end
			task.Status = store.JobCompleted
			task.RowsDone = existing.RowsDone + n
			task.RowsTotal = meta.RowCount
			if task.RowsTotal < task.RowsDone {
				task.RowsTotal = task.RowsDone
			}
			if task.StartedAt != nil && end.Sub(*task.StartedAt).Seconds() > 0 {
				task.RowsPerSec = float64(task.RowsDone) / end.Sub(*task.StartedAt).Seconds()
			}
			task.LastWatermark = newState.watermark
			task.LastMaxKey = newState.maxKey
			task.LastSCN = newState.scn
			_ = e.Store.UpsertTableTask(task)
			if n == 0 {
				_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("%s: no changes (%s)", destTableLabel(meta), syncStateSummary(meta, newState)))
			} else {
				_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("%s: %d row(s) upserted (%s)", destTableLabel(meta), n, syncStateSummary(meta, newState)))
			}
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
			tablesDoneAtomic.Add(1)
			rowsDoneAtomic.Add(n)
			job.TablesDone = int(tablesDoneAtomic.Load())
			job.RowsDone = rowsDoneAtomic.Load()
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
	rowsSynced := rowsDoneAtomic.Load()
	if rowsSynced == 0 {
		_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("Incremental sync finished: checked %d table(s) — no changes since last sync. Run again after Oracle updates, or use Settings to schedule automatic checks.", len(works)))
	} else {
		_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("Incremental sync finished: %d row(s) upserted across %d table(s)", rowsSynced, len(works)))
	}
	return nil
}

func (e *Engine) syncTableIncremental(ctx context.Context, jobID, sourceID, destID string, ora, mssqlDB *sql.DB, meta dbconn.TableMeta, batch int, state syncState, chunkTimeout time.Duration, task *store.TableTask) (int64, syncState, error) {
	normalizeDestSchema(&meta)
	if err := dbconn.PrepareDestinationTable(ctx, mssqlDB, meta); err != nil {
		return 0, state, err
	}
	cur := state
	if incrementalNeedsBaseline(meta, cur) {
		high, err := e.captureSyncHighWater(ctx, ora, meta)
		if err != nil {
			return 0, cur, fmt.Errorf("establish sync baseline: %w", err)
		}
		cur = high
		_ = e.Store.UpsertSyncState(sourceID, destID, meta.Schema, meta.Name,
			meta.SyncMode, meta.WatermarkCol, cur.watermark, cur.maxKey, cur.scn)
		_ = e.Store.LogEvent(jobID, "info", fmt.Sprintf("%s: baseline set (%s) — future runs will pick up changes", destTableLabel(meta), syncStateSummary(meta, cur)))
		return 0, cur, nil
	}
	var total int64
	var lastUI time.Time
	for {
		select {
		case <-ctx.Done():
			return total, cur, ctx.Err()
		default:
		}
		chunkCtx, cancel := context.WithTimeout(ctx, chunkTimeout)
		rows, cols, next, err := e.fetchIncremental(chunkCtx, ora, meta, batch, cur)
		if err != nil {
			cancel()
			return total, cur, wrapChunkErr(err, chunkTimeout)
		}
		if len(rows) == 0 {
			cancel()
			break
		}
		n, err := dbconn.MergeUpsertMSSQL(chunkCtx, mssqlDB, meta.DestSchema, meta.Name, cols, meta.PrimaryKeys, rows)
		cancel()
		if err != nil {
			return total, cur, wrapChunkErr(err, chunkTimeout)
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

func (e *Engine) copyTable(ctx context.Context, ora, mssqlDB *sql.DB, meta dbconn.TableMeta, batch int, startRowID string, chunkTimeout time.Duration, task *store.TableTask, opts tableCopyOpts) (int64, error) {
	normalizeDestSchema(&meta)
	if err := dbconn.PrepareDestinationTable(ctx, mssqlDB, meta); err != nil {
		return 0, err
	}
	colNames := make([]string, len(meta.Columns))
	for i, c := range meta.Columns {
		colNames[i] = c.Name
	}
	var total int64
	if task != nil {
		total = task.RowsDone
	}
	lastRowID := startRowID
	var lastUI time.Time
	for {
		select {
		case <-ctx.Done():
			if task != nil {
				task.LastRowID = lastRowID
				task.RowsDone = total
			}
			return total, ctx.Err()
		default:
		}
		b := effectiveBatch(batch, opts.maxRows, total)
		if b <= 0 {
			break
		}
		chunkCtx, cancel := context.WithTimeout(ctx, chunkTimeout)
		rows, nextRowID, err := e.fetchFullChunk(chunkCtx, ora, meta, colNames, b, lastRowID, opts.dateCol, opts.bounds)
		if err != nil {
			cancel()
			if task != nil {
				task.LastRowID = lastRowID
				task.RowsDone = total
			}
			return total, wrapChunkErr(err, chunkTimeout)
		}
		if len(rows) == 0 {
			cancel()
			break
		}
		n, err := e.writeChunk(chunkCtx, mssqlDB, meta, colNames, rows, opts)
		cancel()
		if err != nil {
			if task != nil {
				task.LastRowID = lastRowID
				task.RowsDone = total
			}
			return total, wrapChunkErr(err, chunkTimeout)
		}
		total += n
		lastRowID = nextRowID
		if task != nil {
			task.LastRowID = lastRowID
			task.RowsDone = total
			if time.Since(lastUI) >= 3*time.Second {
				_ = e.Store.UpsertTableTask(*task)
				lastUI = time.Now()
				if e.OnProgress != nil {
					e.OnProgress()
				}
			}
		}
		if opts.maxRows > 0 && total >= int64(opts.maxRows) {
			break
		}
		if len(rows) < b {
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

func (e *Engine) writeChunk(ctx context.Context, mssqlDB *sql.DB, meta dbconn.TableMeta, colNames []string, rows [][]any, opts tableCopyOpts) (int64, error) {
	rows = dbconn.NormalizeRowsForMSSQL(meta.Columns, rows)
	if opts.upsert {
		return dbconn.MergeUpsertMSSQL(ctx, mssqlDB, meta.DestSchema, meta.Name, colNames, meta.PrimaryKeys, rows)
	}
	return dbconn.BulkInsertMSSQL(ctx, mssqlDB, meta.DestSchema, meta.Name, colNames, rows)
}

func (e *Engine) fetchFullChunk(ctx context.Context, ora *sql.DB, meta dbconn.TableMeta, cols []string, batch int, lastRowID, dateCol string, bounds DateBounds) ([][]any, string, error) {
	colList := strings.Join(quoteOracleCols(cols), ", ")
	schema := quoteOracleIdent(meta.Schema)
	table := quoteOracleIdent(meta.Name)

	var conds []string
	var args []any
	n := 1
	if bounds.Active && dateCol != "" {
		if bounds.HasFrom {
			conds = append(conds, fmt.Sprintf("%s >= :%d", quoteOracleIdent(dateCol), n))
			args = append(args, bounds.From)
			n++
		}
		if bounds.HasTo {
			conds = append(conds, fmt.Sprintf("%s < :%d", quoteOracleIdent(dateCol), n))
			args = append(args, bounds.ToExclusive)
			n++
		}
	}
	if lastRowID != "" {
		conds = append(conds, fmt.Sprintf("ROWID > CHARTOROWID(:%d)", n))
		args = append(args, lastRowID)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	q := fmt.Sprintf(`SELECT %s, ROWIDTOCHAR(ROWID) AS MDAS_RID FROM %s.%s %s ORDER BY ROWID FETCH NEXT %d ROWS ONLY`,
		colList, schema, table, where, batch)

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
