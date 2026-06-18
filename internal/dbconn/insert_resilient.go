package dbconn

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// InsertFailure describes a row that could not be written after conversion retries.
type InsertFailure struct {
	JobID      string
	DestSchema string
	Table      string
	RowIndex   int
	RowJSON    string
	Error      string
	Statement  string
}

// InsertFailureHandler persists or logs rows that fail insert after coercion retries.
type InsertFailureHandler func(InsertFailure) error

// WriteRowsResult summarizes a resilient write pass.
type WriteRowsResult struct {
	Inserted int64
	Skipped  int64
}

// WriteRowsMSSQL inserts or upserts rows. On batch failure it splits at the failing row when
// possible, bulk-inserts good partitions, coerces and retries bad rows, and logs persistent failures.
func WriteRowsMSSQL(ctx context.Context, db *sql.DB, meta TableMeta, colNames []string, rows [][]any, upsert bool, onFail InsertFailureHandler) (WriteRowsResult, error) {
	if len(rows) == 0 {
		return WriteRowsResult{}, nil
	}
	rows = NormalizeRowsForMSSQL(meta.Columns, rows)
	part, err := writeRowsPartition(ctx, db, meta, colNames, rows, upsert, 0, onFail)
	if part.Inserted == 0 && part.Skipped == int64(len(rows)) && err != nil {
		return part, fmt.Errorf("all %d row(s) failed insert for %s.%s: %w", len(rows), meta.DestSchema, meta.Name, err)
	}
	return part, err
}

func writeRowsPartition(ctx context.Context, db *sql.DB, meta TableMeta, colNames []string, rows [][]any, upsert bool, baseIndex int, onFail InsertFailureHandler) (WriteRowsResult, error) {
	if len(rows) == 0 {
		return WriteRowsResult{}, nil
	}
	if upsert {
		return writeRowsMergePartition(ctx, db, meta, colNames, rows, baseIndex, onFail)
	}
	return writeRowsBulkPartition(ctx, db, meta, colNames, rows, baseIndex, onFail)
}

func writeRowsBulkPartition(ctx context.Context, db *sql.DB, meta TableMeta, colNames []string, rows [][]any, baseIndex int, onFail InsertFailureHandler) (WriteRowsResult, error) {
	n, failIdx, err := bulkInsertMSSQL(ctx, db, meta.DestSchema, meta.Name, colNames, rows)
	if err == nil {
		return WriteRowsResult{Inserted: n}, nil
	}
	if failIdx < 0 {
		return writeRowsBulkUnknownFailure(ctx, db, meta, colNames, rows, baseIndex, onFail, err)
	}
	return writeRowsBulkSplitAt(ctx, db, meta, colNames, rows, baseIndex, failIdx, onFail, err)
}

func writeRowsBulkUnknownFailure(ctx context.Context, db *sql.DB, meta TableMeta, colNames []string, rows [][]any, baseIndex int, onFail InsertFailureHandler, cause error) (WriteRowsResult, error) {
	if len(rows) == 1 {
		return handleFailedRow(ctx, db, meta, colNames, rows[0], baseIndex, false, onFail, cause)
	}
	mid := len(rows) / 2
	left, err := writeRowsBulkPartition(ctx, db, meta, colNames, rows[:mid], baseIndex, onFail)
	if err != nil {
		return left, err
	}
	right, err := writeRowsBulkPartition(ctx, db, meta, colNames, rows[mid:], baseIndex+mid, onFail)
	left.Inserted += right.Inserted
	left.Skipped += right.Skipped
	return left, err
}

func writeRowsBulkSplitAt(ctx context.Context, db *sql.DB, meta TableMeta, colNames []string, rows [][]any, baseIndex, failIdx int, onFail InsertFailureHandler, cause error) (WriteRowsResult, error) {
	var res WriteRowsResult
	if failIdx > 0 {
		prefix, err := writeRowsBulkPartition(ctx, db, meta, colNames, rows[:failIdx], baseIndex, onFail)
		if err != nil {
			return res, err
		}
		res.Inserted += prefix.Inserted
		res.Skipped += prefix.Skipped
	}
	bad, err := handleFailedRow(ctx, db, meta, colNames, rows[failIdx], baseIndex+failIdx, false, onFail, cause)
	if err != nil {
		return res, err
	}
	res.Inserted += bad.Inserted
	res.Skipped += bad.Skipped
	if failIdx+1 < len(rows) {
		tail, err := writeRowsBulkPartition(ctx, db, meta, colNames, rows[failIdx+1:], baseIndex+failIdx+1, onFail)
		if err != nil {
			return res, err
		}
		res.Inserted += tail.Inserted
		res.Skipped += tail.Skipped
	}
	return res, nil
}

func writeRowsMergePartition(ctx context.Context, db *sql.DB, meta TableMeta, colNames []string, rows [][]any, baseIndex int, onFail InsertFailureHandler) (WriteRowsResult, error) {
	_, err := mergeChunk(ctx, db, meta.DestSchema, meta.Name, colNames, meta.PrimaryKeys, rows)
	if err == nil {
		return WriteRowsResult{Inserted: int64(len(rows))}, nil
	}
	if len(rows) == 1 {
		return handleFailedRow(ctx, db, meta, colNames, rows[0], baseIndex, true, onFail, err)
	}
	mid := len(rows) / 2
	left, err := writeRowsMergePartition(ctx, db, meta, colNames, rows[:mid], baseIndex, onFail)
	if err != nil {
		return left, err
	}
	right, err := writeRowsMergePartition(ctx, db, meta, colNames, rows[mid:], baseIndex+mid, onFail)
	left.Inserted += right.Inserted
	left.Skipped += right.Skipped
	return left, err
}

func handleFailedRow(ctx context.Context, db *sql.DB, meta TableMeta, colNames []string, row []any, rowIndex int, upsert bool, onFail InsertFailureHandler, cause error) (WriteRowsResult, error) {
	if inserted, err := tryWriteSingleRow(ctx, db, meta, colNames, row, upsert); inserted {
		return WriteRowsResult{Inserted: 1}, nil
	} else if err != nil {
		cause = err
	}
	coerced := CoerceRowForMSSQL(meta.Columns, row)
	if inserted, err := tryWriteSingleRow(ctx, db, meta, colNames, coerced, upsert); inserted {
		return WriteRowsResult{Inserted: 1}, nil
	} else if err != nil {
		cause = err
	}
	if onFail != nil {
		stmt := formatInsertPreview(meta.DestSchema, meta.Name, colNames, coerced)
		rowJSON, _ := json.Marshal(rowValuesMap(colNames, row))
		_ = onFail(InsertFailure{
			JobID:      meta.JobID,
			DestSchema: meta.DestSchema,
			Table:      meta.Name,
			RowIndex:   rowIndex,
			RowJSON:    string(rowJSON),
			Error:      cause.Error(),
			Statement:  stmt,
		})
	}
	return WriteRowsResult{Skipped: 1}, nil
}

func tryWriteSingleRow(ctx context.Context, db *sql.DB, meta TableMeta, colNames []string, row []any, upsert bool) (bool, error) {
	if upsert {
		_, err := mergeChunk(ctx, db, meta.DestSchema, meta.Name, colNames, meta.PrimaryKeys, [][]any{row})
		if err != nil {
			return false, err
		}
		return true, nil
	}
	_, _, err := bulkInsertMSSQL(ctx, db, meta.DestSchema, meta.Name, colNames, [][]any{row})
	if err != nil {
		return false, err
	}
	return true, nil
}

func rowValuesMap(colNames []string, row []any) map[string]any {
	out := make(map[string]any, len(colNames))
	for i, name := range colNames {
		if i < len(row) {
			out[name] = row[i]
		}
	}
	return out
}

func formatInsertPreview(schema, table string, columns []string, row []any) string {
	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(quoteIdent(schema))
	b.WriteString(".")
	b.WriteString(quoteIdent(table))
	b.WriteString(" (")
	for i, c := range columns {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteIdent(c))
	}
	b.WriteString(") VALUES (")
	for i, v := range row {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(formatSQLLiteral(v))
	}
	b.WriteString(")")
	return b.String()
}

func formatSQLLiteral(v any) string {
	if v == nil {
		return "NULL"
	}
	switch x := v.(type) {
	case string:
		return quoteLiteral(truncateForLog(x, 500))
	case []byte:
		if len(x) > 64 {
			return fmt.Sprintf("0x%s...", fmt.Sprintf("%x", x[:32]))
		}
		return fmt.Sprintf("0x%x", x)
	case time.Time:
		return quoteLiteral(x.UTC().Format("2006-01-02 15:04:05.9999999"))
	default:
		return fmt.Sprint(v)
	}
}

func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
