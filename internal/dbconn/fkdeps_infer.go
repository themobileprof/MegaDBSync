package dbconn

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// InferredRelEdge is a suggested parent link from column-name heuristics (no DB FK).
type InferredRelEdge struct {
	ChildSchema  string `json:"child_schema"`
	ChildTable   string `json:"child_table"`
	ChildColumn  string `json:"child_column"`
	ParentSchema string `json:"parent_schema"`
	ParentTable  string `json:"parent_table"`
	ParentColumn string `json:"parent_column"`
	Reason       string `json:"reason"`
	Confidence   string `json:"confidence"` // high, medium
}

// schemaTableIndex holds columns and primary keys for heuristic matching.
type schemaTableIndex struct {
	Owner    string
	Tables   map[string]TableRef            // TABLE -> ref
	Columns  map[string][]string            // TABLE -> column names
	ColTypes columnTypes                    // TABLE -> COLUMN -> metadata
	PKCols   map[string]map[string]bool     // TABLE -> set of PK column names
	ColIndex map[string][]string            // COLUMN -> tables that have it
}

func (idx schemaTableIndex) columnMeta(table, col string) (ColumnMeta, bool) {
	return idx.ColTypes.get(table, col)
}

func integerFKPair(idx schemaTableIndex, childTable, childCol, parentTable, parentCol string) bool {
	child, ok1 := idx.columnMeta(childTable, childCol)
	parent, ok2 := idx.columnMeta(parentTable, parentCol)
	if !ok1 || !ok2 {
		return false
	}
	return IsIntegerOracleColumn(child) && IsIntegerOracleColumn(parent)
}

// stringPKFKPair matches short string columns with the same name where the parent column is a PK.
func stringPKFKPair(idx schemaTableIndex, childTable, childCol, parentTable, parentCol string) bool {
	if childCol != parentCol {
		return false
	}
	if !idx.PKCols[parentTable][parentCol] {
		return false
	}
	child, ok1 := idx.columnMeta(childTable, childCol)
	parent, ok2 := idx.columnMeta(parentTable, parentCol)
	if !ok1 || !ok2 {
		return false
	}
	return IsShortStringPKColumn(child) && IsShortStringPKColumn(parent)
}

func loadSchemaTableIndex(ctx context.Context, db *sql.DB, owner string) (schemaTableIndex, error) {
	owner = strings.ToUpper(strings.TrimSpace(owner))
	idx := schemaTableIndex{
		Owner:    owner,
		Tables:   make(map[string]TableRef),
		Columns:  make(map[string][]string),
		PKCols:   make(map[string]map[string]bool),
		ColIndex: make(map[string][]string),
	}

	types, err := loadOracleColumnTypes(ctx, db, owner)
	if err != nil {
		return idx, err
	}
	idx.ColTypes = types
	for table, cols := range types {
		idx.Tables[table] = TableRef{Schema: owner, Name: table}
		for col := range cols {
			idx.Columns[table] = append(idx.Columns[table], col)
			idx.ColIndex[col] = appendUnique(idx.ColIndex[col], table)
		}
	}
	for table := range idx.Columns {
		sort.Strings(idx.Columns[table])
	}

	pkRows, err := db.QueryContext(ctx, `
SELECT cols.table_name, cols.column_name
FROM all_constraints cons
JOIN all_cons_columns cols
  ON cons.owner = cols.owner AND cons.constraint_name = cols.constraint_name
WHERE cons.constraint_type = 'P' AND cons.owner = :1`, owner)
	if err != nil {
		return idx, err
	}
	defer pkRows.Close()
	for pkRows.Next() {
		var table, col string
		if err := pkRows.Scan(&table, &col); err != nil {
			return idx, err
		}
		table = strings.ToUpper(table)
		col = strings.ToUpper(col)
		if idx.PKCols[table] == nil {
			idx.PKCols[table] = make(map[string]bool)
		}
		idx.PKCols[table][col] = true
	}
	return idx, pkRows.Err()
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func isLikelyFKColumn(col string, table string, pkCols map[string]bool) bool {
	col = strings.ToUpper(col)
	table = strings.ToUpper(table)
	if pkCols != nil && pkCols[col] && len(pkCols) == 1 {
		return false // sole PK on this table — not an outgoing FK
	}
	if strings.HasSuffix(col, "_ID") {
		return true
	}
	if col == "ID" || col == "CODE" || col == "NO" || col == "NUM" {
		return false
	}
	if strings.HasSuffix(col, "_NO") || strings.HasSuffix(col, "_NUM") || strings.HasSuffix(col, "_CODE") {
		return true
	}
	return false
}

func tableNameCandidatesFromColumn(col string) []string {
	col = strings.ToUpper(strings.TrimSpace(col))
	if strings.HasSuffix(col, "_ID") {
		base := strings.TrimSuffix(col, "_ID")
		if base == "" {
			return nil
		}
		return []string{base, base + "S"}
	}
	if strings.HasSuffix(col, "_NO") {
		base := strings.TrimSuffix(col, "_NO")
		if base != "" {
			return []string{base, base + "S"}
		}
	}
	return nil
}

func pkColumnNames(idx schemaTableIndex, table string) []string {
	set := idx.PKCols[table]
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// InferColumnRelationships suggests parent tables by matching column names to PKs / table names.
func InferColumnRelationships(idx schemaTableIndex, selected []TableRef, declared []FKEdge) []InferredRelEdge {
	declaredSet := make(map[string]bool)
	for _, e := range declared {
		key := tableKey(e.ChildSchema, e.ChildTable) + ">" + tableKey(e.ParentSchema, e.ParentTable) + ":" + strings.ToUpper(e.ChildColumn)
		declaredSet[key] = true
	}

	childTables := make(map[string]bool)
	for _, t := range selected {
		name := strings.ToUpper(strings.TrimSpace(t.Name))
		if name != "" {
			childTables[name] = true
		}
	}

	var out []InferredRelEdge
	seen := make(map[string]bool)

	add := func(e InferredRelEdge, integersOnly bool) {
		if e.ChildTable == e.ParentTable {
			return
		}
		eligible := integerFKPair(idx, e.ChildTable, e.ChildColumn, e.ParentTable, e.ParentColumn)
		if !eligible && !integersOnly {
			eligible = stringPKFKPair(idx, e.ChildTable, e.ChildColumn, e.ParentTable, e.ParentColumn)
		}
		if !eligible {
			return
		}
		dk := tableKey(e.ChildSchema, e.ChildTable) + ">" + tableKey(e.ParentSchema, e.ParentTable) + ":" + e.ChildColumn
		if declaredSet[dk] {
			return
		}
		sk := dk + "|" + e.ParentColumn + "|" + e.Reason
		if seen[sk] {
			return
		}
		seen[sk] = true
		out = append(out, e)
	}

	for childTable := range childTables {
		cols := idx.Columns[childTable]
		pk := idx.PKCols[childTable]
		for _, col := range cols {
			if !isLikelyFKColumn(col, childTable, pk) {
				continue
			}
			colUpper := strings.ToUpper(col)
			childMeta, ok := idx.columnMeta(childTable, colUpper)
			if !ok {
				continue
			}
			isInt := IsIntegerOracleColumn(childMeta)
			isStrPK := IsShortStringPKColumn(childMeta)
			if !isInt && !isStrPK {
				continue
			}

			// 1) Same column name is a primary key on another table.
			for _, parentTable := range idx.ColIndex[colUpper] {
				if parentTable == childTable {
					continue
				}
				if !idx.PKCols[parentTable][colUpper] {
					continue
				}
				conf := "high"
				reason := fmtReason("column %s is primary key on %s", colUpper, parentTable)
				if isStrPK && !isInt {
					conf = "medium"
					reason = fmtReason("column %s is string primary key on %s", colUpper, parentTable)
				}
				add(InferredRelEdge{
					ChildSchema: idx.Owner, ChildTable: childTable, ChildColumn: colUpper,
					ParentSchema: idx.Owner, ParentTable: parentTable, ParentColumn: colUpper,
					Reason: reason, Confidence: conf,
				}, false)
			}

			if !isInt {
				continue
			}

			// 2) Column prefix matches table name (STUDENT_ID → STUDENTS).
			for _, candidate := range tableNameCandidatesFromColumn(colUpper) {
				if _, ok := idx.Tables[candidate]; !ok {
					continue
				}
				if candidate == childTable {
					continue
				}
				parentPKs := pkColumnNames(idx, candidate)
				parentCol := colUpper
				conf := "medium"
				if len(parentPKs) == 1 {
					parentCol = parentPKs[0]
					if parentCol == colUpper {
						conf = "high"
					}
				} else if len(parentPKs) == 0 {
					continue
				}
				add(InferredRelEdge{
					ChildSchema: idx.Owner, ChildTable: childTable, ChildColumn: colUpper,
					ParentSchema: idx.Owner, ParentTable: candidate, ParentColumn: parentCol,
					Reason:     fmtReason("column %s matches table name %s", colUpper, candidate),
					Confidence: conf,
				}, true)
			}

			// 3) Same column name on parent (non-PK) — weaker, medium confidence.
			for _, parentTable := range idx.ColIndex[colUpper] {
				if parentTable == childTable {
					continue
				}
				if idx.PKCols[parentTable][colUpper] {
					continue // already handled
				}
				add(InferredRelEdge{
					ChildSchema: idx.Owner, ChildTable: childTable, ChildColumn: colUpper,
					ParentSchema: idx.Owner, ParentTable: parentTable, ParentColumn: colUpper,
					Reason:     fmtReason("shared column name %s on %s", colUpper, parentTable),
					Confidence: "medium",
				}, true)
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		ki := tableKey(out[i].ChildSchema, out[i].ChildTable) + out[i].ChildColumn
		kj := tableKey(out[j].ChildSchema, out[j].ChildTable) + out[j].ChildColumn
		if ki != kj {
			return ki < kj
		}
		return tableKey(out[i].ParentSchema, out[i].ParentTable) < tableKey(out[j].ParentSchema, out[j].ParentTable)
	})
	return out
}

func fmtReason(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

// inferredToFKEdges converts inferred links for dependency resolution.
func inferredToFKEdges(in []InferredRelEdge) []FKEdge {
	out := make([]FKEdge, len(in))
	for i, e := range in {
		out[i] = FKEdge{
			ChildSchema: e.ChildSchema, ChildTable: e.ChildTable, ChildColumn: e.ChildColumn,
			ParentSchema: e.ParentSchema, ParentTable: e.ParentTable, ParentColumn: e.ParentColumn,
			ConstraintName: "INFERRED:" + e.Reason,
		}
	}
	return out
}

// DiscoverTableRelationshipsResult combines declared FK and inferred suggestions.
type DiscoverTableRelationshipsResult struct {
	Declared                TableDependencyResult `json:"declared"`
	Inferred                []InferredRelEdge     `json:"inferred"`
	Suggested               TableDependencyResult `json:"suggested"`
	SuggestedMigrationOrder []string              `json:"suggested_migration_order"`
}

// DiscoverOracleTableRelationships loads declared FKs and infers column-name relationships.
func DiscoverOracleTableRelationships(ctx context.Context, db *sql.DB, owner string, selected []TableRef) (DiscoverTableRelationshipsResult, error) {
	declaredEdges, err := ListOracleFKEdges(ctx, db, owner)
	if err != nil {
		return DiscoverTableRelationshipsResult{}, err
	}
	idx, err := loadSchemaTableIndex(ctx, db, owner)
	if err != nil {
		return DiscoverTableRelationshipsResult{}, err
	}
	declared := ResolveTableDependencies(declaredEdges, owner, selected)

	inferred := InferColumnRelationships(idx, selected, declaredEdges)
	infEdges := inferredToFKEdges(inferred)
	suggested := ResolveTableDependencies(append(declaredEdges, infEdges...), owner, selected)

	return DiscoverTableRelationshipsResult{
		Declared:                declared,
		Inferred:                inferred,
		Suggested:               markInferredDependencies(suggested, declared, inferred),
		SuggestedMigrationOrder: suggested.SuggestedMigrationOrder,
	}, nil
}

func markInferredDependencies(suggested TableDependencyResult, declared TableDependencyResult, inferred []InferredRelEdge) TableDependencyResult {
	declaredKeys := make(map[string]bool)
	for _, d := range declared.Dependencies {
		declaredKeys[tableKey(d.Schema, d.Name)] = true
	}
	inferredParentReason := make(map[string]string)
	for _, e := range inferred {
		pk := tableKey(e.ParentSchema, e.ParentTable)
		if inferredParentReason[pk] == "" {
			inferredParentReason[pk] = e.Reason
		}
	}
	for i := range suggested.Dependencies {
		k := tableKey(suggested.Dependencies[i].Schema, suggested.Dependencies[i].Name)
		if !declaredKeys[k] {
			suggested.Dependencies[i].Inferred = true
			suggested.Dependencies[i].MatchReason = inferredParentReason[k]
		}
	}
	return suggested
}
