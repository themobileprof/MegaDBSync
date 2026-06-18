package dbconn

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/themobileprof/megadbsync/internal/store"
)

// VerifySchema checks that the configured schema exists and is readable.
// Returns the number of user tables visible in that schema.
func VerifySchema(ctx context.Context, db *sql.DB, connType store.ConnType, schema string) (int, error) {
	schema = strings.TrimSpace(schema)
	if schema == "" {
		return 0, nil
	}
	switch connType {
	case store.ConnOracle:
		return verifyOracleSchema(ctx, db, schema)
	case store.ConnMSSQL:
		return verifyMSSQLSchema(ctx, db, schema)
	default:
		return 0, fmt.Errorf("unsupported connection type: %s", connType)
	}
}

func verifyOracleSchema(ctx context.Context, db *sql.DB, schema string) (int, error) {
	owner := strings.ToUpper(schema)
	var exists int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM all_users WHERE username = :1`, owner).Scan(&exists)
	if err != nil {
		return 0, fmt.Errorf("schema lookup: %w", err)
	}
	if exists == 0 {
		return 0, fmt.Errorf("schema %q not found or not visible to this user", schema)
	}
	var tableCount int
	err = db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM all_tables
WHERE owner = :1
  AND owner NOT IN ('SYS','SYSTEM','XDB','MDSYS','CTXSYS','ORDDATA','LBACSYS','OUTLN')`, owner).Scan(&tableCount)
	if err != nil {
		return 0, fmt.Errorf("schema table list: %w", err)
	}
	return tableCount, nil
}

func verifyMSSQLSchema(ctx context.Context, db *sql.DB, schema string) (int, error) {
	var exists int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sys.schemas WHERE name = @p1`, sql.Named("p1", schema)).Scan(&exists)
	if err != nil {
		return 0, fmt.Errorf("schema lookup: %w", err)
	}
	if exists == 0 {
		return 0, fmt.Errorf("schema %q not found", schema)
	}
	tables, err := listMSSQLTables(ctx, db, schema)
	if err != nil {
		return 0, fmt.Errorf("schema table list: %w", err)
	}
	return len(tables), nil
}

// PrepareDestinationTable ensures the SQL Server schema exists and creates the destination
// table from Oracle column metadata (DDL) when it does not already exist.
func PrepareDestinationTable(ctx context.Context, db *sql.DB, meta TableMeta) error {
	dest := strings.TrimSpace(meta.DestSchema)
	if dest == "" {
		dest = "dbo"
	}
	if err := ensureMSSQLSchema(ctx, db, dest); err != nil {
		return fmt.Errorf("schema %s: %w", dest, err)
	}
	if err := CreateMSSQLTable(ctx, db, meta); err != nil {
		return fmt.Errorf("table %s.%s: %w", dest, meta.Name, err)
	}
	return nil
}

// PrepareDestinationTableInferred samples source rows, infers SQL Server column types, then creates the table.
func PrepareDestinationTableInferred(ctx context.Context, ora, mssql *sql.DB, meta *TableMeta, sampleRows int) error {
	if meta == nil {
		return fmt.Errorf("table metadata required")
	}
	if sampleRows > 0 && ora != nil {
		samples, err := FetchOracleSampleRows(ctx, ora, *meta, sampleRows)
		if err == nil && len(samples) > 0 {
			meta.Columns = InferMSSQLTypes(meta.Columns, samples)
		}
	}
	if err := EnrichColumnsFromDestination(ctx, mssql, meta); err != nil {
		return err
	}
	return PrepareDestinationTable(ctx, mssql, *meta)
}

// EnrichColumnsFromDestination fills MSSQLType from an existing SQL Server table when present.
func EnrichColumnsFromDestination(ctx context.Context, db *sql.DB, meta *TableMeta) error {
	if meta == nil {
		return nil
	}
	dest := strings.TrimSpace(meta.DestSchema)
	if dest == "" {
		dest = "dbo"
	}
	types, err := LoadMSSQLColumnTypes(ctx, db, dest, meta.Name)
	if err != nil {
		return err
	}
	if len(types) == 0 {
		return nil
	}
	for i := range meta.Columns {
		if t, ok := types[strings.ToUpper(meta.Columns[i].Name)]; ok {
			meta.Columns[i].MSSQLType = t
		}
	}
	return nil
}

// LoadMSSQLColumnTypes returns fully formatted SQL Server types for an existing table.
func LoadMSSQLColumnTypes(ctx context.Context, db *sql.DB, schema, table string) (map[string]string, error) {
	q := `
SELECT c.name,
       t.name,
       c.max_length,
       c.precision,
       c.scale
FROM sys.columns c
JOIN sys.types t ON c.user_type_id = t.user_type_id
JOIN sys.tables tb ON c.object_id = tb.object_id
JOIN sys.schemas s ON tb.schema_id = s.schema_id
WHERE s.name = @p1 AND tb.name = @p2`
	rows, err := db.QueryContext(ctx, q, sql.Named("p1", schema), sql.Named("p2", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var name, typeName string
		var maxLen int16
		var prec, scale uint8
		if err := rows.Scan(&name, &typeName, &maxLen, &prec, &scale); err != nil {
			return nil, err
		}
		out[strings.ToUpper(name)] = formatMSSQLTypeFromSys(typeName, maxLen, prec, scale)
	}
	return out, rows.Err()
}

func formatMSSQLTypeFromSys(typeName string, maxLen int16, prec, scale uint8) string {
	name := strings.ToUpper(typeName)
	switch name {
	case "NVARCHAR", "VARCHAR", "NCHAR", "CHAR", "VARBINARY", "BINARY":
		if maxLen == -1 {
			return name + "(MAX)"
		}
		n := int(maxLen)
		if name == "NVARCHAR" || name == "NCHAR" {
			n = int(maxLen) / 2
			if n < 1 {
				n = 1
			}
		}
		return fmt.Sprintf("%s(%d)", name, n)
	case "DECIMAL", "NUMERIC":
		return fmt.Sprintf("%s(%d,%d)", name, prec, scale)
	case "DATETIME2", "DATETIMEOFFSET", "TIME":
		return fmt.Sprintf("%s(%d)", name, scale)
	default:
		return name
	}
}

func ensureMSSQLSchema(ctx context.Context, db *sql.DB, schema string) error {
	schema = strings.TrimSpace(schema)
	if schema == "" {
		return nil
	}
	var b strings.Builder
	b.WriteString("IF NOT EXISTS (SELECT 1 FROM sys.schemas WHERE name = ")
	b.WriteString(quoteLiteral(schema))
	b.WriteString(") EXEC(N'CREATE SCHEMA ")
	b.WriteString(strings.ReplaceAll(quoteIdent(schema), "'", "''"))
	b.WriteString("')")
	_, err := db.ExecContext(ctx, b.String())
	return err
}
