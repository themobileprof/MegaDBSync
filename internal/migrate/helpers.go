package migrate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/themobileprof/megadbsync/internal/dbconn"
	"github.com/themobileprof/megadbsync/internal/store"
)

const defaultChunkTimeout = 5 * time.Minute

// ChunkTimeoutError is returned when a single read/insert batch exceeds the allowed time.
type ChunkTimeoutError struct {
	Timeout time.Duration
}

func (e ChunkTimeoutError) Error() string {
	return fmt.Sprintf("chunk timed out after %s — database may be overloaded; pause, adjust batch size, and resume", e.Timeout.Round(time.Second))
}

func IsChunkTimeout(err error) bool {
	var te ChunkTimeoutError
	return errors.As(err, &te)
}

func sortTablesBySize(metas []dbconn.TableMeta) {
	sort.Slice(metas, func(i, j int) bool {
		if metas[i].RowCount != metas[j].RowCount {
			return metas[i].RowCount < metas[j].RowCount
		}
		if metas[i].Schema != metas[j].Schema {
			return metas[i].Schema < metas[j].Schema
		}
		return metas[i].Name < metas[j].Name
	})
}

func chunkTimeout(job store.Job, settings store.AppSettings) time.Duration {
	sec := job.ChunkTimeoutSec
	if sec <= 0 {
		sec = settings.DefaultChunkTimeoutSec
	}
	if sec <= 0 {
		return defaultChunkTimeout
	}
	return time.Duration(sec) * time.Second
}

func tableTaskKey(schema, table string) string {
	return schema + "." + table
}

func summarizeCompletedTasks(tasks []store.TableTask) (tablesDone int, rowsDone int64) {
	for _, t := range tasks {
		if t.Status == store.JobCompleted {
			tablesDone++
			rowsDone += t.RowsDone
		}
	}
	return tablesDone, rowsDone
}

func taskMap(tasks []store.TableTask) map[string]store.TableTask {
	m := make(map[string]store.TableTask, len(tasks))
	for _, t := range tasks {
		m[tableTaskKey(t.SchemaName, t.TableName)] = t
	}
	return m
}

func applyMetaRowCount(task *store.TableTask, meta dbconn.TableMeta) {
	task.SourceRowCount = meta.RowCount
	task.SourceRowCountKnown = meta.RowCountKnown
	task.SourceRowCountApprox = meta.RowCountApprox
	task.SourceRowCountExceeded = meta.RowCountExceeded
	if meta.RowCountKnown {
		task.RowsTotal = meta.RowCount
	}
}

func contributeRowTotal(meta dbconn.TableMeta) int64 {
	if !meta.RowCountKnown {
		return 0
	}
	return meta.RowCount
}

func isTableWorkComplete(status store.JobStatus) bool {
	return status == store.JobCompleted
}


func wrapChunkErr(err error, timeout time.Duration) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ChunkTimeoutError{Timeout: timeout}
	}
	return err
}
