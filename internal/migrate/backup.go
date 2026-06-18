package migrate

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/themobileprof/megadbsync/internal/dbconn"
	"github.com/themobileprof/megadbsync/internal/store"
)

// tableCopyOpts configures optional date filtering, row cap, and upsert behaviour during table copy.
type tableCopyOpts struct {
	dateCol string
	bounds  DateBounds
	JobID string
	upsert  bool
	maxRows int
}

func effectiveBatch(batch, maxRows int, total int64) int {
	if maxRows <= 0 {
		return batch
	}
	remain := maxRows - int(total)
	if remain <= 0 {
		return 0
	}
	if remain < batch {
		return remain
	}
	return batch
}

func (e *Engine) RunDateRangeBackup(ctx context.Context, job store.Job, src, dst store.Connection, srcPass, dstPass string) error {
	settings, _ := e.Store.GetSettings()
	timeout := chunkTimeout(job, settings)
	rowCountCap := settings.DefaultRowCountFallbackCap

	bounds, err := ParseDateBounds(job.DateFrom, job.DateTo)
	if err != nil {
		return err
	}

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
	rangeLabel := bounds.Summary()
	if resuming {
		_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("Resuming date-range backup (%s): %d tables", rangeLabel, len(tables)))
	} else {
		_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("Starting date-range backup (%s): %d tables (smallest first)", rangeLabel, len(tables)))
	}

	metas := make([]dbconn.TableMeta, 0, len(tables))
	var rowsTotal int64
	var skippedTables int
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
		dateCol, err := dbconn.ResolveDateColumn(meta, job.DateColumn)
		if err != nil {
			return err
		}
		meta.DateCol = dateCol
		if bounds.Active && dateCol == "" {
			_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("Skipping %s.%s: no date column for range filter", meta.Schema, meta.Name))
			task := store.TableTask{
				JobID: job.ID, SchemaName: meta.DestSchema, TableName: meta.Name,
				Status: store.JobCompleted, SyncMode: "date_backup",
				ErrorMessage: "skipped: no date column for range filter",
			}
			applyMetaRowCount(&task, meta)
			now := time.Now().UTC()
			task.CompletedAt = &now
			_ = e.Store.UpsertTableTask(task)
			skippedTables++
			continue
		}
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
	tablesDone += skippedTables
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

	copyOpts := tableCopyOpts{JobID: job.ID, bounds: bounds, upsert: true, maxRows: job.MaxRowsPerTable}
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
		opts := copyOpts
		opts.dateCol = tableMeta.DateCol
		go func() {
			defer func() { <-sem; done <- struct{}{} }()
			task := store.TableTask{
				JobID: job.ID, SchemaName: tableMeta.DestSchema, TableName: tableMeta.Name,
				Status: store.JobRunning, SyncMode: "date_backup", WatermarkCol: tableMeta.DateCol,
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

			tableRows, err := e.copyTable(ctx, ora, mssqlDB, tableMeta, job.BatchSize, existing.LastRowID, timeout, &task, opts)
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
			if task.StartedAt != nil && end.Sub(*task.StartedAt).Seconds() > 0 {
				task.RowsPerSec = float64(task.RowsDone) / end.Sub(*task.StartedAt).Seconds()
			}
			_ = e.Store.UpsertTableTask(task)
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
	_ = e.Store.LogEvent(job.ID, "info", "Date-range backup completed")
	return nil
}
