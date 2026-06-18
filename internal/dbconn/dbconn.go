package dbconn

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	go_ora "github.com/sijms/go-ora/v2"
	"github.com/themobileprof/megadbsync/internal/store"
	mssql "github.com/microsoft/go-mssqldb"
)

type TableMeta struct {
	Schema           string // Oracle owner (source)
	DestSchema       string // SQL Server schema (destination)
	Name             string
	Columns          []ColumnMeta
	PrimaryKeys      []string
	RowCount         int64
	RowCountKnown    bool
	RowCountApprox   bool
	RowCountExceeded bool
	DateCol          string
	WatermarkCol     string
	SyncMode         string
}

type ColumnMeta struct {
	Name         string
	DataType     string
	CharMaxLen   *int64
	NumericPrec  *int64
	NumericScale *int64
	Nullable     bool
}

func OpenOracle(ctx context.Context, c store.Connection, password string) (*sql.DB, error) {
	return openOracle(ctx, c, password, defaultConnectOpts)
}

func openOracle(ctx context.Context, c store.Connection, password string, opts ConnectOpts) (*sql.DB, error) {
	opts = opts.normalized()
	pingCtx, cancel := withConnectTimeout(ctx, opts)
	defer cancel()

	service := c.Database
	if service == "" {
		service = "ORCL"
	}
	port := c.Port
	if port == 0 {
		port = 1521
	}
	connStr := go_ora.BuildUrl(c.Host, port, service, c.Username, password, oracleTimeoutOptions(opts))
	db, err := sql.Open("oracle", connStr)
	if err != nil {
		return nil, err
	}
	ConfigurePool(db, 2)
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("oracle ping: %w", err)
	}
	return db, nil
}

func OpenMSSQL(ctx context.Context, c store.Connection, password string) (*sql.DB, error) {
	return openMSSQL(ctx, c, password, defaultConnectOpts)
}

func openMSSQL(ctx context.Context, c store.Connection, password string, opts ConnectOpts) (*sql.DB, error) {
	opts = opts.normalized()
	pingCtx, cancel := withConnectTimeout(ctx, opts)
	defer cancel()

	port := c.Port
	if port == 0 {
		port = 1433
	}
	host := fmt.Sprintf("%s:%d", c.Host, port)

	var u *url.URL
	if c.WindowsAuth {
		u = &url.URL{Scheme: "sqlserver", Host: host}
	} else {
		u = &url.URL{
			Scheme: "sqlserver",
			User:   url.UserPassword(c.Username, password),
			Host:   host,
		}
	}
	q := u.Query()
	if c.Database != "" {
		q.Add("database", c.Database)
	}
	if c.WindowsAuth {
		q.Add("integrated security", "true")
	}
	q.Add("encrypt", mssqlEncryptQuery(opts))
	q.Add("TrustServerCertificate", mssqlTrustCertQuery(opts))
	q.Add("connection timeout", fmt.Sprintf("%d", opts.TimeoutSec))
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlserver", u.String())
	if err != nil {
		return nil, err
	}
	ConfigurePool(db, 2)
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sql server ping: %w", err)
	}
	return db, nil
}

func TestConnection(ctx context.Context, c store.Connection, password string) error {
	switch c.Type {
	case store.ConnOracle:
		db, err := OpenOracle(ctx, c, password)
		if err != nil {
			return err
		}
		defer db.Close()
		return nil
	case store.ConnMSSQL:
		db, err := OpenMSSQL(ctx, c, password)
		if err != nil {
			return err
		}
		defer db.Close()
		return nil
	default:
		return fmt.Errorf("unsupported connection type: %s", c.Type)
	}
}

// DestinationMustBeEmpty blocks bulk loads into non-empty MSSQL databases.
func DestinationMustBeEmpty(ctx context.Context, db *sql.DB, schema string) (int, error) {
	query := `
SELECT COUNT(*)
FROM sys.tables t
INNER JOIN sys.schemas s ON t.schema_id = s.schema_id
WHERE s.name NOT IN ('sys','INFORMATION_SCHEMA','guest','db_accessadmin','db_backupoperator',
  'db_datareader','db_datawriter','db_ddladmin','db_denydatareader','db_denydatawriter',
  'db_owner','db_securityadmin')`
	args := []any{}
	if schema != "" {
		query += ` AND s.name = @p1`
		args = append(args, sql.Named("p1", schema))
	}
	var count int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func ListOracleTables(ctx context.Context, db *sql.DB, ownerFilter string) ([]TableMeta, error) {
	q := `
SELECT owner, table_name, NVL(num_rows, 0),
  CASE WHEN last_analyzed IS NOT NULL THEN 1 ELSE 0 END
FROM all_tables
WHERE owner NOT IN ('SYS','SYSTEM','XDB','MDSYS','CTXSYS','ORDDATA','LBACSYS','OUTLN')
`
	args := []any{}
	if ownerFilter != "" {
		q += ` AND owner = :1`
		args = append(args, ownerFilter)
	}
	q += ` ORDER BY NVL(num_rows, 0), owner, table_name`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []TableMeta
	for rows.Next() {
		var t TableMeta
		var analyzed int
		if err := rows.Scan(&t.Schema, &t.Name, &t.RowCount, &analyzed); err != nil {
			return nil, err
		}
		t.RowCountKnown = analyzed == 1
		t.RowCountApprox = t.RowCountKnown
		tables = append(tables, t)
	}
	return tables, rows.Err()
}

func LoadOracleTableMeta(ctx context.Context, db *sql.DB, schema, table string, rowCountFallbackCap int64) (TableMeta, error) {
	meta := TableMeta{Schema: schema, Name: table}
	cols, err := oracleColumns(ctx, db, schema, table)
	if err != nil {
		return meta, err
	}
	meta.Columns = cols
	meta.PrimaryKeys, _ = oraclePrimaryKeys(ctx, db, schema, table)
	meta.WatermarkCol, meta.SyncMode = detectSyncMode(cols, meta.PrimaryKeys)
	if err := fillOracleRowCount(ctx, db, schema, table, &meta, rowCountFallbackCap); err != nil {
		meta.RowCount = 0
		meta.RowCountKnown = false
	}
	if meta.RowCountKnown && !meta.RowCountExceeded {
		meta.RowCountApprox = true
	}
	return meta, nil
}

func oracleColumns(ctx context.Context, db *sql.DB, schema, table string) ([]ColumnMeta, error) {
	rows, err := db.QueryContext(ctx, `
SELECT column_name, data_type, char_length, data_precision, data_scale, nullable
FROM all_tab_columns
WHERE owner = :1 AND table_name = :2
ORDER BY column_id`, strings.ToUpper(schema), strings.ToUpper(table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []ColumnMeta
	for rows.Next() {
		var c ColumnMeta
		var nullable string
		var charLen, prec, scale sql.NullInt64
		if err := rows.Scan(&c.Name, &c.DataType, &charLen, &prec, &scale, &nullable); err != nil {
			return nil, err
		}
		if charLen.Valid {
			c.CharMaxLen = &charLen.Int64
		}
		if prec.Valid {
			c.NumericPrec = &prec.Int64
		}
		if scale.Valid {
			c.NumericScale = &scale.Int64
		}
		c.Nullable = nullable == "Y"
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

func oraclePrimaryKeys(ctx context.Context, db *sql.DB, schema, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
SELECT cols.column_name
FROM all_constraints cons
JOIN all_cons_columns cols ON cons.constraint_name = cols.constraint_name AND cons.owner = cols.owner
WHERE cons.constraint_type = 'P' AND cons.owner = :1 AND cons.table_name = :2
ORDER BY cols.position`, strings.ToUpper(schema), strings.ToUpper(table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pks []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		pks = append(pks, name)
	}
	return pks, rows.Err()
}

var watermarkNames = []string{
	"UPDATED_AT", "UPDATE_DATE", "MODIFIED_AT", "MODIFIED_DATE", "LAST_UPDATED",
	"LAST_MODIFIED", "CHANGED_AT", "CHANGE_DATE", "DATE_MODIFIED", "DT_MODIFIED",
}

func detectSyncMode(cols []ColumnMeta, pks []string) (watermarkCol, mode string) {
	for _, c := range cols {
		upper := strings.ToUpper(c.Name)
		for _, w := range watermarkNames {
			if upper == w {
				return c.Name, "watermark"
			}
		}
	}
	if len(pks) == 1 {
		for _, c := range cols {
			if strings.EqualFold(c.Name, pks[0]) {
				dt := strings.ToUpper(c.DataType)
				if strings.Contains(dt, "NUMBER") || strings.Contains(dt, "INT") {
					return "", "max_key"
				}
			}
		}
	}
	return "", "ora_rowscn"
}

func CreateMSSQLTable(ctx context.Context, db *sql.DB, meta TableMeta) error {
	schema := strings.TrimSpace(meta.DestSchema)
	if schema == "" {
		schema = "dbo"
	}
	var b strings.Builder
	b.WriteString("IF NOT EXISTS (SELECT 1 FROM sys.tables t JOIN sys.schemas s ON t.schema_id=s.schema_id WHERE s.name=")
	b.WriteString(quoteLiteral(schema))
	b.WriteString(" AND t.name=")
	b.WriteString(quoteLiteral(meta.Name))
	b.WriteString(") CREATE TABLE ")
	b.WriteString(quoteIdent(schema))
	b.WriteString(".")
	b.WriteString(quoteIdent(meta.Name))
	b.WriteString(" (")
	for i, col := range meta.Columns {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteIdent(col.Name))
		b.WriteString(" ")
		b.WriteString(mapOracleType(col))
		if !col.Nullable {
			b.WriteString(" NOT NULL")
		}
	}
	if len(meta.PrimaryKeys) > 0 {
		b.WriteString(", CONSTRAINT ")
		b.WriteString(quoteIdent("PK_" + meta.Name))
		b.WriteString(" PRIMARY KEY (")
		for i, pk := range meta.PrimaryKeys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(quoteIdent(pk))
		}
		b.WriteString(")")
	}
	b.WriteString(")")
	_, err := db.ExecContext(ctx, b.String())
	return err
}

func mapOracleType(c ColumnMeta) string {
	dt := strings.ToUpper(c.DataType)
	switch {
	case strings.Contains(dt, "CHAR"), strings.Contains(dt, "CLOB"):
		if c.CharMaxLen != nil && *c.CharMaxLen > 0 && *c.CharMaxLen <= 4000 {
			return fmt.Sprintf("NVARCHAR(%d)", *c.CharMaxLen)
		}
		return "NVARCHAR(MAX)"
	case strings.Contains(dt, "BLOB"), strings.Contains(dt, "RAW"), strings.Contains(dt, "LONG RAW"):
		return "VARBINARY(MAX)"
	case strings.Contains(dt, "DATE"), strings.Contains(dt, "TIMESTAMP"):
		return "DATETIME2(6)"
	case strings.Contains(dt, "FLOAT"), strings.Contains(dt, "BINARY_DOUBLE"):
		return "FLOAT(53)"
	case strings.Contains(dt, "BINARY_FLOAT"):
		return "REAL"
	case strings.Contains(dt, "NUMBER"):
		if c.NumericScale != nil && *c.NumericScale == 0 && c.NumericPrec != nil && *c.NumericPrec <= 18 {
			return "BIGINT"
		}
		if c.NumericPrec != nil && c.NumericScale != nil {
			return fmt.Sprintf("DECIMAL(%d,%d)", *c.NumericPrec, *c.NumericScale)
		}
		return "DECIMAL(38,10)"
	default:
		return "NVARCHAR(MAX)"
	}
}

func quoteIdent(s string) string {
	return "[" + strings.ReplaceAll(s, "]", "]]") + "]"
}

func quoteLiteral(s string) string {
	return "N'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func BulkInsertMSSQL(ctx context.Context, db *sql.DB, schema, table string, columns []string, rows [][]any) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	stmt, err := tx.PrepareContext(ctx, mssql.CopyIn(
		quoteIdent(schema)+"."+quoteIdent(table),
		mssql.BulkOptions{CheckConstraints: false, FireTriggers: false, Tablock: true},
		columns...,
	))
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	defer stmt.Close()
	for _, row := range rows {
		if _, err := stmt.ExecContext(ctx, row...); err != nil {
			_ = tx.Rollback()
			return 0, err
		}
	}
	if _, err := stmt.ExecContext(ctx); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(rows)), nil
}

func MergeUpsertMSSQL(ctx context.Context, db *sql.DB, schema, table string, columns, pks []string, rows [][]any) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	var applied int64
	for start := 0; start < len(rows); start += mergeBatchSize(len(columns)) {
		end := start + mergeBatchSize(len(columns))
		if end > len(rows) {
			end = len(rows)
		}
		chunk := rows[start:end]
		n, err := mergeChunk(ctx, db, schema, table, columns, pks, chunk)
		if err != nil {
			return applied, err
		}
		applied += n
	}
	return applied, nil
}

func mergeBatchSize(cols int) int {
	if cols <= 0 {
		return 100
	}
	// Stay under SQL Server's ~2100 parameter limit.
	n := 2000 / cols
	if n < 1 {
		return 1
	}
	if n > 100 {
		return 100
	}
	return n
}

func mergeChunk(ctx context.Context, db *sql.DB, schema, table string, columns, pks []string, rows [][]any) (int64, error) {
	if len(pks) == 0 {
		return BulkInsertMSSQL(ctx, db, schema, table, columns, rows)
	}
	// staging via VALUES clauses batched
	var b strings.Builder
	b.WriteString("MERGE ")
	b.WriteString(quoteIdent(schema))
	b.WriteString(".")
	b.WriteString(quoteIdent(table))
	b.WriteString(" AS tgt USING (VALUES ")
	for ri := range rows {
		if ri > 0 {
			b.WriteString(",")
		}
		b.WriteString("(")
		for ci := range columns {
			if ci > 0 {
				b.WriteString(",")
			}
			b.WriteString(fmt.Sprintf("@p%d_%d", ri, ci))
		}
		b.WriteString(")")
	}
	b.WriteString(") AS src(")
	for i, c := range columns {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(quoteIdent(c))
	}
	b.WriteString(") ON ")
	for i, pk := range pks {
		if i > 0 {
			b.WriteString(" AND ")
		}
		b.WriteString("tgt.")
		b.WriteString(quoteIdent(pk))
		b.WriteString("=src.")
		b.WriteString(quoteIdent(pk))
	}
	nonPK := 0
	for _, c := range columns {
		if containsFold(pks, c) {
			continue
		}
		if nonPK == 0 {
			b.WriteString(" WHEN MATCHED THEN UPDATE SET ")
		} else {
			b.WriteString(", ")
		}
		b.WriteString("tgt.")
		b.WriteString(quoteIdent(c))
		b.WriteString("=src.")
		b.WriteString(quoteIdent(c))
		nonPK++
	}
	b.WriteString(" WHEN NOT MATCHED THEN INSERT (")
	for i, c := range columns {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(quoteIdent(c))
	}
	b.WriteString(") VALUES (")
	for i, c := range columns {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("src.")
		b.WriteString(quoteIdent(c))
	}
	b.WriteString(");")
	args := make([]any, 0, len(rows)*len(columns))
	for ri, row := range rows {
		for ci, v := range row {
			args = append(args, sql.Named(fmt.Sprintf("p%d_%d", ri, ci), v))
		}
	}
	res, err := db.ExecContext(ctx, b.String(), args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
