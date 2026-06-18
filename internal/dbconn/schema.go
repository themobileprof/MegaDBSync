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
