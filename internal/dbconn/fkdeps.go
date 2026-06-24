package dbconn

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// FKEdge is one foreign-key column link (child → parent).
type FKEdge struct {
	ChildSchema    string `json:"child_schema"`
	ChildTable     string `json:"child_table"`
	ChildColumn    string `json:"child_column"`
	ParentSchema   string `json:"parent_schema"`
	ParentTable    string `json:"parent_table"`
	ParentColumn   string `json:"parent_column"`
	ConstraintName string `json:"constraint_name"`
}

// TableRef identifies a table by owner and name.
type TableRef struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

// TableDependency is a parent table required by FK references from selected or child tables.
type TableDependency struct {
	TableRef
	Depth      int      `json:"depth"`
	RequiredBy []string `json:"required_by"`
	Selected   bool     `json:"selected"`
}

// TableDependencyResult is the transitive FK parent closure for a table selection.
type TableDependencyResult struct {
	Selected                []TableRef        `json:"selected"`
	Dependencies            []TableDependency `json:"dependencies"`
	Edges                   []FKEdge          `json:"edges"`
	SuggestedMigrationOrder []string          `json:"suggested_migration_order"`
}

func tableKey(schema, name string) string {
	return strings.ToUpper(strings.TrimSpace(schema)) + "." + strings.ToUpper(strings.TrimSpace(name))
}

// ListOracleFKEdges returns FK column links visible for an Oracle owner.
func ListOracleFKEdges(ctx context.Context, db *sql.DB, owner string) ([]FKEdge, error) {
	owner = strings.ToUpper(strings.TrimSpace(owner))
	rows, err := db.QueryContext(ctx, `
SELECT
  fk.owner,
  fk.table_name,
  fk.constraint_name,
  pk.owner,
  pk.table_name,
  fkc.column_name,
  pkc.column_name
FROM all_constraints fk
JOIN all_cons_columns fkc
  ON fk.owner = fkc.owner AND fk.constraint_name = fkc.constraint_name
JOIN all_constraints pk
  ON fk.r_owner = pk.owner AND fk.r_constraint_name = pk.constraint_name
JOIN all_cons_columns pkc
  ON pk.owner = pkc.owner AND pk.constraint_name = pkc.constraint_name AND pkc.position = fkc.position
WHERE fk.constraint_type = 'R'
  AND fk.owner = :1
ORDER BY fk.table_name, fk.constraint_name, fkc.position`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FKEdge
	for rows.Next() {
		var e FKEdge
		if err := rows.Scan(&e.ChildSchema, &e.ChildTable, &e.ConstraintName, &e.ParentSchema, &e.ParentTable, &e.ChildColumn, &e.ParentColumn); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ResolveTableDependencies walks FK parent links transitively from selected tables.
func ResolveTableDependencies(edges []FKEdge, defaultSchema string, selected []TableRef) TableDependencyResult {
	defaultSchema = strings.ToUpper(strings.TrimSpace(defaultSchema))
	selectedSet := make(map[string]TableRef)
	for _, t := range selected {
		schema := strings.ToUpper(strings.TrimSpace(t.Schema))
		if schema == "" {
			schema = defaultSchema
		}
		name := strings.ToUpper(strings.TrimSpace(t.Name))
		if name == "" {
			continue
		}
		selectedSet[tableKey(schema, name)] = TableRef{Schema: schema, Name: name}
	}

	parentsByChild := make(map[string][]FKEdge)
	for _, e := range edges {
		childKey := tableKey(e.ChildSchema, e.ChildTable)
		parentsByChild[childKey] = append(parentsByChild[childKey], e)
	}

	closure := make(map[string]TableRef)
	for k, v := range selectedSet {
		closure[k] = v
	}
	requiredBy := make(map[string]map[string]bool)
	depth := make(map[string]int)

	frontier := make([]string, 0, len(closure))
	for k := range selectedSet {
		frontier = append(frontier, k)
		depth[k] = 0
	}
	sort.Strings(frontier)

	for len(frontier) > 0 {
		next := frontier[:0]
		for _, childKey := range frontier {
			childDepth := depth[childKey]
			for _, e := range parentsByChild[childKey] {
				parentKey := tableKey(e.ParentSchema, e.ParentTable)
				if _, isSelected := selectedSet[parentKey]; !isSelected {
					if requiredBy[parentKey] == nil {
						requiredBy[parentKey] = make(map[string]bool)
					}
					requiredBy[parentKey][childKey] = true
				}
				if _, ok := closure[parentKey]; ok {
					continue
				}
				closure[parentKey] = TableRef{Schema: strings.ToUpper(e.ParentSchema), Name: strings.ToUpper(e.ParentTable)}
				depth[parentKey] = childDepth + 1
				next = append(next, parentKey)
			}
		}
		sort.Strings(next)
		frontier = next
	}

	var selectedOut []TableRef
	for _, k := range sortedKeys(selectedSet) {
		selectedOut = append(selectedOut, selectedSet[k])
	}

	var deps []TableDependency
	for k, ref := range closure {
		if _, isSelected := selectedSet[k]; isSelected {
			continue
		}
		req := sortedKeys(requiredBy[k])
		deps = append(deps, TableDependency{
			TableRef:   ref,
			Depth:      depth[k],
			RequiredBy: req,
			Selected:   false,
		})
	}
	sort.Slice(deps, func(i, j int) bool {
		if deps[i].Depth != deps[j].Depth {
			return deps[i].Depth < deps[j].Depth
		}
		ki := tableKey(deps[i].Schema, deps[i].Name)
		kj := tableKey(deps[j].Schema, deps[j].Name)
		return ki < kj
	})

	var resultEdges []FKEdge
	for _, e := range edges {
		childKey := tableKey(e.ChildSchema, e.ChildTable)
		parentKey := tableKey(e.ParentSchema, e.ParentTable)
		if _, childOK := closure[childKey]; !childOK {
			continue
		}
		if _, parentOK := closure[parentKey]; !parentOK {
			continue
		}
		resultEdges = append(resultEdges, e)
	}

	order := topologicalMigrationOrder(closure, resultEdges)

	return TableDependencyResult{
		Selected:                selectedOut,
		Dependencies:            deps,
		Edges:                   resultEdges,
		SuggestedMigrationOrder: order,
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func topologicalMigrationOrder(closure map[string]TableRef, edges []FKEdge) []string {
	inDegree := make(map[string]int, len(closure))
	children := make(map[string][]string)
	for k := range closure {
		inDegree[k] = 0
	}
	for _, e := range edges {
		child := tableKey(e.ChildSchema, e.ChildTable)
		parent := tableKey(e.ParentSchema, e.ParentTable)
		if _, ok := closure[child]; !ok {
			continue
		}
		if _, ok := closure[parent]; !ok {
			continue
		}
		if child == parent {
			continue
		}
		children[parent] = append(children[parent], child)
		inDegree[child]++
	}
	queue := make([]string, 0)
	for k, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, k)
		}
	}
	sort.Strings(queue)
	var order []string
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		order = append(order, n)
		kids := append([]string(nil), children[n]...)
		sort.Strings(kids)
		for _, child := range kids {
			inDegree[child]--
			if inDegree[child] == 0 {
				queue = append(queue, child)
			}
		}
		sort.Strings(queue)
	}
	if len(order) != len(closure) {
		// cyclic FK graph — fall back to stable key order
		order = sortedKeys(closure)
	}
	return order
}

// OracleTableDependencies loads FK edges and resolves parent dependencies for selected tables.
func OracleTableDependencies(ctx context.Context, db *sql.DB, owner string, selected []TableRef) (TableDependencyResult, error) {
	edges, err := ListOracleFKEdges(ctx, db, owner)
	if err != nil {
		return TableDependencyResult{}, err
	}
	return ResolveTableDependencies(edges, owner, selected), nil
}

// FormatTableFilter builds the comma-separated table_filter value from table refs.
func FormatTableFilter(tables []TableRef) string {
	if len(tables) == 0 {
		return ""
	}
	keys := make([]string, 0, len(tables))
	seen := make(map[string]bool)
	for _, t := range tables {
		k := tableKey(t.Schema, t.Name)
		if seen[k] {
			continue
		}
		seen[k] = true
		if t.Schema != "" {
			keys = append(keys, fmt.Sprintf("%s.%s", t.Schema, t.Name))
		} else {
			keys = append(keys, t.Name)
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// ParseTableSelection turns user table names into refs (SCHEMA.TABLE or TABLE).
func ParseTableSelection(defaultSchema string, names []string) []TableRef {
	defaultSchema = strings.ToUpper(strings.TrimSpace(defaultSchema))
	var out []TableRef
	seen := make(map[string]bool)
	for _, raw := range names {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		schema := defaultSchema
		name := strings.ToUpper(raw)
		if i := strings.Index(raw, "."); i >= 0 {
			schema = strings.ToUpper(strings.TrimSpace(raw[:i]))
			name = strings.ToUpper(strings.TrimSpace(raw[i+1:]))
		}
		if name == "" {
			continue
		}
		k := tableKey(schema, name)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, TableRef{Schema: schema, Name: name})
	}
	return out
}
