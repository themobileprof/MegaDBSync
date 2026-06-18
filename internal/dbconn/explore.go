package dbconn

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mdas/mdas/internal/store"
)

type ExploreTable struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

type ExploreColumn struct {
	Name       string `json:"name"`
	DataType   string `json:"data_type"`
	MaxLength  *int64 `json:"max_length,omitempty"`
	Precision  *int64 `json:"precision,omitempty"`
	Scale      *int64 `json:"scale,omitempty"`
	Nullable   bool   `json:"nullable"`
	IsPrimary  bool   `json:"is_primary_key"`
}

func OpenFromParams(ctx context.Context, c store.Connection, password string) (*sql.DB, error) {
	switch c.Type {
	case store.ConnOracle:
		return OpenOracle(ctx, c, password)
	case store.ConnMSSQL:
		return OpenMSSQL(ctx, c, password)
	default:
		return nil, fmt.Errorf("unsupported type: %s", c.Type)
	}
}

func ListTables(ctx context.Context, c store.Connection, password string) ([]ExploreTable, error) {
	db, err := OpenFromParams(ctx, c, password)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	switch c.Type {
	case store.ConnOracle:
		owner := strings.ToUpper(c.Schema)
		meta, err := ListOracleTables(ctx, db, owner)
		if err != nil {
			return nil, err
		}
		out := make([]ExploreTable, len(meta))
		for i, t := range meta {
			out[i] = ExploreTable{Schema: t.Schema, Name: t.Name}
		}
		return out, nil
	case store.ConnMSSQL:
		return listMSSQLTables(ctx, db, c.Schema)
	default:
		return nil, fmt.Errorf("unsupported type")
	}
}

func listMSSQLTables(ctx context.Context, db *sql.DB, schema string) ([]ExploreTable, error) {
	q := `
SELECT s.name, t.name
FROM sys.tables t
INNER JOIN sys.schemas s ON t.schema_id = s.schema_id
WHERE s.name NOT IN ('sys','INFORMATION_SCHEMA','guest')`
	args := []any{}
	if schema != "" {
		q += ` AND s.name = @p1`
		args = append(args, sql.Named("p1", schema))
	}
	q += ` ORDER BY s.name, t.name`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExploreTable
	for rows.Next() {
		var t ExploreTable
		if err := rows.Scan(&t.Schema, &t.Name); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func TableSchema(ctx context.Context, c store.Connection, password, schema, table string) ([]ExploreColumn, error) {
	db, err := OpenFromParams(ctx, c, password)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	switch c.Type {
	case store.ConnOracle:
		meta, err := LoadOracleTableMeta(ctx, db, schema, table)
		if err != nil {
			return nil, err
		}
		pkSet := make(map[string]bool)
		for _, pk := range meta.PrimaryKeys {
			pkSet[strings.ToUpper(pk)] = true
		}
		out := make([]ExploreColumn, len(meta.Columns))
		for i, col := range meta.Columns {
			out[i] = ExploreColumn{
				Name: col.Name, DataType: col.DataType,
				MaxLength: col.CharMaxLen, Precision: col.NumericPrec, Scale: col.NumericScale,
				Nullable: col.Nullable, IsPrimary: pkSet[strings.ToUpper(col.Name)],
			}
		}
		return out, nil
	case store.ConnMSSQL:
		return mssqlColumns(ctx, db, schema, table)
	default:
		return nil, fmt.Errorf("unsupported type")
	}
}

func mssqlColumns(ctx context.Context, db *sql.DB, schema, table string) ([]ExploreColumn, error) {
	rows, err := db.QueryContext(ctx, `
SELECT c.name, ty.name, c.max_length, c.precision, c.scale, c.is_nullable,
  CASE WHEN pk.column_id IS NOT NULL THEN 1 ELSE 0 END
FROM sys.columns c
INNER JOIN sys.types ty ON c.user_type_id = ty.user_type_id
INNER JOIN sys.tables t ON c.object_id = t.object_id
INNER JOIN sys.schemas s ON t.schema_id = s.schema_id
LEFT JOIN (
  SELECT ic.object_id, ic.column_id
  FROM sys.indexes i
  INNER JOIN sys.index_columns ic ON i.object_id = ic.object_id AND i.index_id = ic.index_id
  WHERE i.is_primary_key = 1
) pk ON pk.object_id = c.object_id AND pk.column_id = c.column_id
WHERE s.name = @p1 AND t.name = @p2
ORDER BY c.column_id`, sql.Named("p1", schema), sql.Named("p2", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExploreColumn
	for rows.Next() {
		var col ExploreColumn
		var nullable, isPK int
		var maxLen int16
		var prec, scale uint8
		if err := rows.Scan(&col.Name, &col.DataType, &maxLen, &prec, &scale, &nullable, &isPK); err != nil {
			return nil, err
		}
		ml := int64(maxLen)
		col.MaxLength = &ml
		p := int64(prec)
		col.Precision = &p
		sc := int64(scale)
		col.Scale = &sc
		col.Nullable = nullable == 1
		col.IsPrimary = isPK == 1
		out = append(out, col)
	}
	return out, rows.Err()
}

type SampleResult struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

func SampleRows(ctx context.Context, c store.Connection, password, schema, table string, limit int) (SampleResult, error) {
	if limit <= 0 || limit > 100 {
		limit = 5
	}
	db, err := OpenFromParams(ctx, c, password)
	if err != nil {
		return SampleResult{}, err
	}
	defer db.Close()

	var q string
	switch c.Type {
	case store.ConnOracle:
		q = fmt.Sprintf(`SELECT * FROM %s.%s FETCH FIRST %d ROWS ONLY`,
			quoteOracleIdent(schema), quoteOracleIdent(table), limit)
	case store.ConnMSSQL:
		q = fmt.Sprintf(`SELECT TOP (%d) * FROM %s.%s`, limit, quoteIdent(schema), quoteIdent(table))
	default:
		return SampleResult{}, fmt.Errorf("unsupported type")
	}

	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return SampleResult{}, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return SampleResult{}, err
	}
	var out SampleResult
	out.Columns = cols
	for rows.Next() {
		holders := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range holders {
			ptrs[i] = &holders[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return SampleResult{}, err
		}
		row := make([]string, len(cols))
		for i, v := range holders {
			if v == nil {
				row[i] = ""
			} else {
				row[i] = fmt.Sprint(v)
			}
		}
		out.Rows = append(out.Rows, row)
	}
	return out, rows.Err()
}

func quoteOracleIdent(s string) string {
	return `"` + strings.ReplaceAll(strings.ToUpper(s), `"`, `""`) + `"`
}

func SchemaJSON(cols []ExploreColumn) ([]byte, error) {
	return json.MarshalIndent(cols, "", "  ")
}

func SampleCSV(res SampleResult) ([]byte, error) {
	var b strings.Builder
	w := csv.NewWriter(&b)
	if err := w.Write(res.Columns); err != nil {
		return nil, err
	}
	for _, row := range res.Rows {
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return []byte(b.String()), w.Error()
}

func SchemaDDL(c store.Connection, schema, table string, cols []ExploreColumn) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("-- Schema for %s.%s (%s)\n", schema, table, c.Type))
	for _, col := range cols {
		b.WriteString(fmt.Sprintf("%s %s", col.Name, col.DataType))
		if col.MaxLength != nil && *col.MaxLength > 0 {
			b.WriteString(fmt.Sprintf("(%d)", *col.MaxLength))
		} else if col.Precision != nil && col.Scale != nil {
			b.WriteString(fmt.Sprintf("(%d,%d)", *col.Precision, *col.Scale))
		}
		if col.IsPrimary {
			b.WriteString(" PRIMARY KEY")
		}
		if !col.Nullable {
			b.WriteString(" NOT NULL")
		}
		b.WriteString("\n")
	}
	return b.String()
}
