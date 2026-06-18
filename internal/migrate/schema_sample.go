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

// SchemaSampleRowsPerTable is how many rows are copied per table in schema sample jobs.
const SchemaSampleRowsPerTable = 5

// RunSchemaSample creates SQL Server tables from Oracle metadata, then copies a small
// sample of rows into each empty destination table. It does not require an empty database.
func (e *Engine) RunSchemaSample(ctx context.Context, job store.Job, src, dst store.Connection, srcPass, dstPass string) error {
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

	owner := strings.ToUpper(src.Schema)
	tables, err := dbconn.ListOracleTables(ctx, ora, owner)
	if err != nil {
		return err
	}
	if job.TableFilter != "" {
		tables = filterTables(tables, job.TableFilter)
	}

	sampleRows := SchemaSampleRowsPerTable

	job.TablesTotal = len(tables)
	job.RowsTotal = int64(len(tables) * sampleRows)
	if job.StartedAt == nil {
		now := time.Now().UTC()
		job.StartedAt = &now
	}
	job.Status = store.JobRunning
	job.CurrentPhase = "schema"
	_ = e.Store.UpdateJob(job)

	if resuming {
		_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("Resuming schema sample: %d tables, %d row(s) each", len(tables), sampleRows))
	} else {
		_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("Schema sample started: %d tables — create DDL on SQL Server, then copy %d row(s) per table", len(tables), sampleRows))
	}

	metas := make([]dbconn.TableMeta, 0, len(tables))
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

		key := tableTaskKey(meta.DestSchema, meta.Name)
		if task, ok := taskByKey[key]; ok && isTableWorkComplete(task.Status) {
			metas = append(metas, meta)
			continue
		}

		job.CurrentTable = destTableLabel(meta)
		_ = e.Store.UpdateJob(job)

		if err := dbconn.PrepareDestinationTable(ctx, mssqlDB, meta); err != nil {
			return fmt.Errorf("schema %s: %w", destTableLabel(meta), err)
		}
		_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("Schema created: %s (%d columns)", destTableLabel(meta), len(meta.Columns)))
		metas = append(metas, meta)
	}
	sortTablesBySize(metas)

	tablesDone, rowsDone := summarizeCompletedTasks(existingTasks)
	job.TablesDone = tablesDone
	job.RowsDone = rowsDone
	job.CurrentPhase = "sample"
	_ = e.Store.UpdateJob(job)
	_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("Schema phase complete — copying up to %d row(s) per table", sampleRows))

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

	copyOpts := tableCopyOpts{maxRows: sampleRows}

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
				Status: store.JobRunning, SyncMode: "schema_sample",
				LastRowID: existing.LastRowID, RowsDone: existing.RowsDone,
			}
			applyMetaRowCount(&task, tableMeta)
			if task.RowsTotal > int64(sampleRows) || !tableMeta.RowCountKnown {
				task.RowsTotal = int64(sampleRows)
			}
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

			destRows, err := dbconn.DestinationTableRowCount(ctx, mssqlDB, tableMeta.DestSchema, tableMeta.Name)
			if err != nil {
				task.Status = store.JobFailed
				task.ErrorMessage = err.Error()
				_ = e.Store.UpsertTableTask(task)
				errCh <- fmt.Errorf("%s: %w", destTableLabel(tableMeta), err)
				return
			}
			if destRows > 0 {
				end := time.Now().UTC()
				task.CompletedAt = &end
				task.Status = store.JobCompleted
				task.RowsDone = 0
				_ = e.Store.UpsertTableTask(task)
				_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("%s: skipped sample data (%d row(s) already on destination)", destTableLabel(tableMeta), destRows))
				return
			}

			tableRows, err := e.copyTable(ctx, ora, mssqlDB, tableMeta, sampleRows, existing.LastRowID, timeout, &task, copyOpts)
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
			if task.RowsTotal < task.RowsDone {
				task.RowsTotal = task.RowsDone
			}
			elapsed := end.Sub(*task.StartedAt).Seconds()
			if elapsed > 0 && tableRows > 0 {
				task.RowsPerSec = float64(tableRows) / elapsed
			}
			_ = e.Store.UpsertTableTask(task)
			if tableRows == 0 {
				_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("%s: no rows on source", destTableLabel(tableMeta)))
			} else {
				_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("%s: copied %d sample row(s)", destTableLabel(tableMeta), tableRows))
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
	_ = e.Store.LogEvent(job.ID, "info", fmt.Sprintf("Schema sample completed — %d table(s), %d row(s) copied", job.TablesDone, job.RowsDone))
	return nil
}
