package dbconn

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// applyOracleRowCount reads NUM_ROWS and whether optimizer stats exist (last_analyzed).
// Values come from all_tables — no COUNT(*) on table data.
func applyOracleRowCount(ctx context.Context, db *sql.DB, schema, table string, meta *TableMeta) error {
	var analyzed int
	err := db.QueryRowContext(ctx, `
SELECT NVL(num_rows, 0),
  CASE WHEN last_analyzed IS NOT NULL THEN 1 ELSE 0 END
FROM all_tables
WHERE owner = :1 AND table_name = :2`,
		strings.ToUpper(schema), strings.ToUpper(table)).Scan(&meta.RowCount, &analyzed)
	if err == sql.ErrNoRows {
		meta.RowCount = 0
		meta.RowCountKnown = false
		return nil
	}
	if err != nil {
		return err
	}
	meta.RowCountKnown = analyzed == 1
	return nil
}

// oracleCappedRowCount runs at most cap row probes via ROWNUM (bounded cost).
// Returns the count and whether the true total exceeds cap.
func oracleCappedRowCount(ctx context.Context, db *sql.DB, schema, table string, cap int64) (count int64, exceeded bool, err error) {
	if cap <= 0 {
		return 0, false, nil
	}
	schemaQ := quoteOracleIdent(schema)
	tableQ := quoteOracleIdent(table)
	limit := cap + 1
	q := fmt.Sprintf(`SELECT COUNT(*) FROM (SELECT 1 FROM %s.%s WHERE ROWNUM <= %d)`, schemaQ, tableQ, limit)
	if err := db.QueryRowContext(ctx, q).Scan(&count); err != nil {
		return 0, false, err
	}
	if count > cap {
		return cap, true, nil
	}
	return count, false, nil
}

// fillOracleRowCount uses optimizer stats; optional fallbackCap probes up to N rows when stats are missing.
func fillOracleRowCount(ctx context.Context, db *sql.DB, schema, table string, meta *TableMeta, fallbackCap int64) error {
	if err := applyOracleRowCount(ctx, db, schema, table, meta); err != nil {
		return err
	}
	if meta.RowCountKnown || fallbackCap <= 0 {
		return nil
	}
	count, exceeded, err := oracleCappedRowCount(ctx, db, schema, table, fallbackCap)
	if err != nil {
		return err
	}
	meta.RowCount = count
	meta.RowCountKnown = true
	meta.RowCountExceeded = exceeded
	meta.RowCountApprox = false
	return nil
}
