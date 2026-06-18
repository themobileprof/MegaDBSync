package dbconn

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// FetchOracleSampleRows reads up to limit rows from an Oracle table for schema inference.
func FetchOracleSampleRows(ctx context.Context, db *sql.DB, meta TableMeta, limit int) ([][]any, error) {
	if limit <= 0 {
		return nil, nil
	}
	cols := make([]string, len(meta.Columns))
	for i, c := range meta.Columns {
		cols[i] = c.Name
	}
	if len(cols) == 0 {
		return nil, nil
	}
	colList := strings.Join(oracleSelectCols(cols), ", ")
	q := fmt.Sprintf(`SELECT %s FROM %s.%s FETCH NEXT %d ROWS ONLY`,
		colList, quoteOracleIdent(meta.Schema), quoteOracleIdent(meta.Name), limit)
	return queryOracleRows(ctx, db, q)
}

func queryOracleRows(ctx context.Context, db *sql.DB, q string, args ...any) ([][]any, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	colCount := len(cols)
	out := make([][]any, 0, 8)
	holders := make([]any, colCount)
	ptrs := make([]any, colCount)
	for i := range holders {
		ptrs[i] = &holders[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]any, colCount)
		copy(row, holders)
		out = append(out, row)
	}
	return out, rows.Err()
}

func oracleSelectCols(cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = quoteOracleIdent(c)
	}
	return out
}
