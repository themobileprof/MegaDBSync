package dbconn

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/themobileprof/megadbsync/internal/store"
)

const (
	reportLargeTableRows    int64 = 10_000_000
	reportVeryLargeTableRows int64 = 100_000_000
	reportWideTableColumns  int   = 80
)

// MigrationSeverity ranks how strongly a finding affects migration planning.
type MigrationSeverity string

const (
	SeverityCritical MigrationSeverity = "critical"
	SeverityWarning  MigrationSeverity = "warning"
	SeverityInfo     MigrationSeverity = "info"
)

// MigrationFinding is one assessed risk or note for the migration report.
type MigrationFinding struct {
	Severity       MigrationSeverity `json:"severity"`
	Category       string            `json:"category"`
	Code           string            `json:"code"`
	Table          string            `json:"table,omitempty"`
	Column         string            `json:"column,omitempty"`
	Message        string            `json:"message"`
	Recommendation string            `json:"recommendation,omitempty"`
}

// ServerProfile is optional database host/version context when accessible.
type ServerProfile struct {
	DBMS    string            `json:"dbms"`
	Version string            `json:"version,omitempty"`
	Details map[string]string `json:"details,omitempty"`
}

// DestinationProfile summarizes SQL Server readiness for bulk migration.
type DestinationProfile struct {
	Reachable    bool          `json:"reachable"`
	Schema       string        `json:"schema"`
	TableCount   int           `json:"table_count"`
	EmptyForBulk bool          `json:"empty_for_bulk"`
	Server       ServerProfile `json:"server,omitempty"`
	Error        string        `json:"error,omitempty"`
}

// TableMigrationProfile is per-table assessment.
type TableMigrationProfile struct {
	Schema        string               `json:"schema"`
	Name          string               `json:"name"`
	Label         string               `json:"label"`
	RowCount      int64                `json:"row_count,omitempty"`
	RowCountKnown bool                 `json:"row_count_known"`
	ColumnCount   int                  `json:"column_count"`
	PrimaryKeys   []string             `json:"primary_keys,omitempty"`
	SyncMode      string               `json:"sync_mode"`
	MssqlDDL      string               `json:"mssql_ddl_preview,omitempty"`
	RiskLevel     MigrationSeverity    `json:"risk_level"`
	Findings      []MigrationFinding   `json:"findings,omitempty"`
}

// MigrationReportSummary rolls up counts for the UI header.
type MigrationReportSummary struct {
	TableCount           int    `json:"table_count"`
	CriticalCount        int    `json:"critical_count"`
	WarningCount         int    `json:"warning_count"`
	InfoCount            int    `json:"info_count"`
	TablesWithIssues     int    `json:"tables_with_issues"`
	EstimatedRows        int64  `json:"estimated_rows,omitempty"`
	RowsEstimateKnown    bool   `json:"rows_estimate_known"`
	BulkMigrationReady   bool   `json:"bulk_migration_ready"`
	SchemaSampleSuitable bool   `json:"schema_sample_suitable"`
	IncrementalNotes     string `json:"incremental_notes,omitempty"`
}

// MigrationReport is the full migration readiness assessment.
type MigrationReport struct {
	GeneratedAt   time.Time                `json:"generated_at"`
	SourceSchema  string                   `json:"source_schema"`
	Source        ServerProfile            `json:"source"`
	Destination   *DestinationProfile      `json:"destination,omitempty"`
	Summary       MigrationReportSummary   `json:"summary"`
	Findings      []MigrationFinding       `json:"findings"`
	Tables        []TableMigrationProfile  `json:"tables"`
	TopRisks      []string                 `json:"top_risks,omitempty"`
}

// BuildOracleMigrationReport analyzes an Oracle schema for MegaDBSync migration risks.
func BuildOracleMigrationReport(ctx context.Context, ora *sql.DB, owner string, rowCountCap int64, dest *store.Connection, destPass string) (MigrationReport, error) {
	owner = strings.ToUpper(strings.TrimSpace(owner))
	report := MigrationReport{
		GeneratedAt:  time.Now().UTC(),
		SourceSchema: owner,
		Source:       probeOracleServer(ctx, ora),
	}

	tables, err := ListOracleTables(ctx, ora, owner)
	if err != nil {
		return report, err
	}

	colsByTable, err := loadOracleOwnerColumns(ctx, ora, owner)
	if err != nil {
		return report, err
	}
	pksByTable, err := loadOracleOwnerPrimaryKeys(ctx, ora, owner)
	if err != nil {
		return report, err
	}

	var destFindings []MigrationFinding
	if dest != nil {
		prof := assessDestination(ctx, *dest, destPass)
		report.Destination = &prof
		if prof.Error != "" && !prof.Reachable {
			destFindings = append(destFindings, finding(SeverityWarning, "destination", "dest_unreachable", "", "",
				"Could not connect to SQL Server destination: "+prof.Error,
				"Fix destination connectivity before scheduling migration"))
		}
		if prof.Reachable && !prof.EmptyForBulk {
			destFindings = append(destFindings, finding(SeverityWarning, "destination", "dest_not_empty", "", "",
				fmt.Sprintf("Destination schema [%s] has %d table(s) — bulk migration will be blocked", prof.Schema, prof.TableCount),
				"Use Schema + sample, date-range backup, or incremental sync; or empty the destination schema before bulk"))
		}
	}

	var allFindings []MigrationFinding
	profiles := make([]TableMigrationProfile, 0, len(tables))
	var estRows int64
	allRowsKnown := true

	for _, t := range tables {
		cols := colsByTable[strings.ToUpper(t.Name)]
		pks := pksByTable[strings.ToUpper(t.Name)]
		wm, syncMode := detectSyncMode(cols, pks)

		meta := TableMeta{
			Schema: t.Schema, Name: t.Name, Columns: cols, PrimaryKeys: pks,
			RowCount: t.RowCount, RowCountKnown: t.RowCountKnown, RowCountApprox: t.RowCountApprox,
			WatermarkCol: wm, SyncMode: syncMode,
		}
		if !t.RowCountKnown && rowCountCap > 0 {
			_ = fillOracleRowCount(ctx, ora, t.Schema, t.Name, &meta, rowCountCap)
		}

		if meta.RowCountKnown {
			estRows += meta.RowCount
		} else {
			allRowsKnown = false
		}

		tFindings := assessTable(meta)
		risk := highestSeverity(tFindings)
		profiles = append(profiles, TableMigrationProfile{
			Schema: t.Schema, Name: t.Name, Label: t.Schema + "." + t.Name,
			RowCount: meta.RowCount, RowCountKnown: meta.RowCountKnown,
			ColumnCount: len(cols), PrimaryKeys: pks, SyncMode: syncMode,
			MssqlDDL:  previewMssqlDDL(meta),
			RiskLevel: risk, Findings: tFindings,
		})
		allFindings = append(allFindings, tFindings...)
	}
	allFindings = append(allFindings, destFindings...)

	sort.Slice(profiles, func(i, j int) bool {
		if severityRank(profiles[i].RiskLevel) != severityRank(profiles[j].RiskLevel) {
			return severityRank(profiles[i].RiskLevel) > severityRank(profiles[j].RiskLevel)
		}
		if profiles[i].RowCount != profiles[j].RowCount {
			return profiles[i].RowCount > profiles[j].RowCount
		}
		return profiles[i].Label < profiles[j].Label
	})

	report.Tables = profiles
	report.Findings = sortFindings(allFindings)
	report.Summary = summarizeMigrationReport(profiles, report.Findings, estRows, allRowsKnown, report.Destination)
	report.TopRisks = topRiskMessages(report.Findings, 8)
	return report, nil
}

func assessDestination(ctx context.Context, dest store.Connection, pass string) DestinationProfile {
	prof := DestinationProfile{Schema: EffectiveDestSchema(dest.Schema)}
	db, err := OpenMSSQL(ctx, dest, pass)
	if err != nil {
		prof.Error = err.Error()
		return prof
	}
	defer db.Close()
	prof.Reachable = true
	prof.Server = probeMssqlServer(ctx, db)
	count, err := DestinationMustBeEmpty(ctx, db, dest.Schema)
	if err != nil {
		prof.Error = err.Error()
		return prof
	}
	prof.TableCount = count
	prof.EmptyForBulk = count == 0
	return prof
}

func probeOracleServer(ctx context.Context, db *sql.DB) ServerProfile {
	prof := ServerProfile{DBMS: "oracle", Details: map[string]string{}}
	var banner string
	if err := db.QueryRowContext(ctx, `SELECT banner FROM v$version WHERE ROWNUM = 1`).Scan(&banner); err == nil {
		prof.Version = strings.TrimSpace(banner)
	}
	var bytes sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT NVL(SUM(bytes), 0) FROM user_segments`).Scan(&bytes); err == nil && bytes.Valid {
		prof.Details["user_segment_bytes"] = fmt.Sprintf("%d", bytes.Int64)
		prof.Details["user_segment_gb"] = fmt.Sprintf("%.2f", float64(bytes.Int64)/(1024*1024*1024))
	}
	var tblCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_tables`).Scan(&tblCount); err == nil {
		prof.Details["user_table_count"] = fmt.Sprintf("%d", tblCount)
	}
	return prof
}

func probeMssqlServer(ctx context.Context, db *sql.DB) ServerProfile {
	prof := ServerProfile{DBMS: "mssql", Details: map[string]string{}}
	var version string
	if err := db.QueryRowContext(ctx, `SELECT @@VERSION`).Scan(&version); err == nil {
		prof.Version = strings.TrimSpace(strings.Split(version, "\n")[0])
	}
	return prof
}

func loadOracleOwnerColumns(ctx context.Context, db *sql.DB, owner string) (map[string][]ColumnMeta, error) {
	rows, err := db.QueryContext(ctx, `
SELECT table_name, column_name, data_type, char_length, data_length, char_used, data_precision, data_scale, nullable
FROM all_tab_columns
WHERE owner = :1
ORDER BY table_name, column_id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]ColumnMeta)
	for rows.Next() {
		var tableName string
		var c ColumnMeta
		var nullable, charUsed sql.NullString
		var charLen, dataLen, prec, scale sql.NullInt64
		if err := rows.Scan(&tableName, &c.Name, &c.DataType, &charLen, &dataLen, &charUsed, &prec, &scale, &nullable); err != nil {
			return nil, err
		}
		if charLen.Valid {
			c.CharMaxLen = &charLen.Int64
		}
		if dataLen.Valid {
			c.DataLength = &dataLen.Int64
		}
		if charUsed.Valid {
			c.CharUsed = charUsed.String
		}
		if prec.Valid {
			c.NumericPrec = &prec.Int64
		}
		if scale.Valid {
			c.NumericScale = &scale.Int64
		}
		c.Nullable = nullable.Valid && strings.EqualFold(nullable.String, "Y")
		key := strings.ToUpper(tableName)
		out[key] = append(out[key], c)
	}
	return out, rows.Err()
}

func loadOracleOwnerPrimaryKeys(ctx context.Context, db *sql.DB, owner string) (map[string][]string, error) {
	rows, err := db.QueryContext(ctx, `
SELECT cols.table_name, cols.column_name
FROM all_constraints cons
JOIN all_cons_columns cols ON cons.owner = cols.owner AND cons.constraint_name = cols.constraint_name
WHERE cons.owner = :1 AND cons.constraint_type = 'P'
ORDER BY cols.table_name, cols.position`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]string)
	for rows.Next() {
		var tableName, col string
		if err := rows.Scan(&tableName, &col); err != nil {
			return nil, err
		}
		key := strings.ToUpper(tableName)
		out[key] = append(out[key], col)
	}
	return out, rows.Err()
}

func assessTable(meta TableMeta) []MigrationFinding {
	var f []MigrationFinding
	label := meta.Schema + "." + meta.Name

	if len(meta.Columns) == 0 {
		f = append(f, finding(SeverityCritical, "structure", "structure_no_columns", label, "",
			"Table has no visible columns",
			"Check privileges or whether this is a valid application table"))
	}

	if len(meta.PrimaryKeys) == 0 {
		f = append(f, finding(SeverityWarning, "structure", "structure_no_pk", label, "",
			"No primary key — incremental sync will use ORA_ROWSCN",
			"Add a primary key on Oracle if possible; SCN-based sync can miss updates on some storage layouts"))
	} else if len(meta.PrimaryKeys) > 1 {
		f = append(f, finding(SeverityInfo, "structure", "structure_composite_pk", label, "",
			fmt.Sprintf("Composite primary key (%s)", strings.Join(meta.PrimaryKeys, ", ")),
			"Incremental sync cannot use max-key mode; relies on watermark or ORA_ROWSCN"))
	}

	switch meta.SyncMode {
	case "ora_rowscn":
		if meta.WatermarkCol == "" {
			f = append(f, finding(SeverityInfo, "structure", "structure_sync_scn", label, "",
				"No watermark column — incremental sync uses ORA_ROWSCN",
				"Consider a dedicated UPDATED_AT column for clearer change tracking"))
		}
	case "watermark":
		f = append(f, finding(SeverityInfo, "structure", "structure_sync_watermark", label, meta.WatermarkCol,
			fmt.Sprintf("Incremental sync can use watermark column %s", meta.WatermarkCol), ""))
	case "max_key":
		f = append(f, finding(SeverityInfo, "structure", "structure_sync_maxkey", label, meta.PrimaryKeys[0],
			fmt.Sprintf("Incremental sync can use numeric PK %s", meta.PrimaryKeys[0]), ""))
	}

	if len(meta.Columns) >= reportWideTableColumns {
		f = append(f, finding(SeverityWarning, "size", "size_wide_table", label, "",
			fmt.Sprintf("%d columns — very wide table", len(meta.Columns)),
			"Wide tables increase batch memory and MERGE parameter pressure; consider lowering parallel tables"))
	}

	if !meta.RowCountKnown {
		f = append(f, finding(SeverityWarning, "size", "size_unknown_rows", label, "",
			"Row count unknown (missing or stale Oracle optimizer stats)",
			"Run DBMS_STATS on this table or rely on row-count fallback cap in Settings"))
	} else if meta.RowCountExceeded {
		f = append(f, finding(SeverityInfo, "size", "size_row_cap_exceeded", label, "",
			fmt.Sprintf("Row count exceeds configured probe cap (%d+ rows)", meta.RowCount),
			"Actual size may be larger than shown"))
	} else if meta.RowCount >= reportVeryLargeTableRows {
		f = append(f, finding(SeverityCritical, "size", "size_huge_table", label, "",
			fmt.Sprintf("~%s rows — very large table", formatReportNum(meta.RowCount)),
			"Plan long bulk window, tune batch size/chunk timeout, migrate during off-peak"))
	} else if meta.RowCount >= reportLargeTableRows {
		f = append(f, finding(SeverityWarning, "size", "size_large_table", label, "",
			fmt.Sprintf("~%s rows — large table", formatReportNum(meta.RowCount)),
			"Expect extended copy time; monitor chunk timeouts on the dashboard"))
	}

	for _, col := range meta.Columns {
		f = append(f, assessColumn(meta, col)...)
	}
	return f
}

func assessColumn(meta TableMeta, col ColumnMeta) []MigrationFinding {
	var f []MigrationFinding
	label := meta.Schema + "." + meta.Name
	dt := strings.ToUpper(strings.TrimSpace(col.DataType))
	mssqlType := MapOracleType(col)

	switch {
	case dt == "LONG" || strings.Contains(dt, "LONG RAW"):
		f = append(f, finding(SeverityCritical, "type", "type_legacy_long", label, col.Name,
			fmt.Sprintf("Legacy Oracle type %s", col.DataType),
			"Migrate off LONG/LONG RAW on Oracle first, or export via CLOB/BLOB conversion"))
	case dt == "BFILE":
		f = append(f, finding(SeverityCritical, "type", "type_bfile", label, col.Name,
			"BFILE points to server files outside the database",
			"Files must be migrated separately; column cannot be copied automatically"))
	case strings.Contains(dt, "INTERVAL"):
		f = append(f, finding(SeverityWarning, "type", "type_lossy_interval", label, col.Name,
			fmt.Sprintf("%s → %s (text serialization)", col.DataType, mssqlType),
			"Verify interval values after sample migration; consider computing scalar columns on Oracle"))
	case strings.Contains(dt, "TIME ZONE") || strings.Contains(dt, "LOCAL TIME ZONE"):
		f = append(f, finding(SeverityWarning, "type", "type_lossy_tz", label, col.Name,
			fmt.Sprintf("%s → %s (timezone normalized to UTC)", col.DataType, mssqlType),
			"Confirm application tolerates UTC storage without original offset"))
	case strings.Contains(dt, "XML"):
		f = append(f, finding(SeverityInfo, "type", "type_xml", label, col.Name,
			fmt.Sprintf("XMLType → %s", mssqlType),
			"XML is stored as Unicode text; advanced XML indexing is not replicated"))
	case strings.Contains(dt, "BLOB") || strings.Contains(dt, "LONG RAW"):
		f = append(f, finding(SeverityWarning, "type", "type_large_binary", label, col.Name,
			fmt.Sprintf("%s → %s", col.DataType, mssqlType),
			"Large binary columns slow bulk copy; consider parallel_tables=1 for wide binary tables"))
	case strings.Contains(dt, "CLOB") || strings.Contains(dt, "NCLOB"):
		f = append(f, finding(SeverityInfo, "type", "type_large_text", label, col.Name,
			fmt.Sprintf("%s → %s", col.DataType, mssqlType),
			"Very large text may increase memory per batch"))
	case strings.Contains(dt, "NUMBER"):
		prec, scale, ok := oracleNumberPrecScale(col)
		if ok && prec > 38 {
			f = append(f, finding(SeverityWarning, "type", "type_number_precision", label, col.Name,
				fmt.Sprintf("NUMBER(%d,%d) exceeds SQL Server DECIMAL max precision (38)", prec, scale),
				"Values may be rejected or require rounding on insert"))
		}
	case isHeuristicDefaultType(dt):
		f = append(f, finding(SeverityWarning, "type", "type_heuristic", label, col.Name,
			fmt.Sprintf("%s → %s (default fallback mapping)", col.DataType, mssqlType),
			"Run Schema + sample on this table and inspect values"))
	}

	if col.CharMaxLen != nil && *col.CharMaxLen > 4000 && (strings.Contains(dt, "VARCHAR") || isFixedChar(dt)) {
		f = append(f, finding(SeverityInfo, "type", "type_long_varchar", label, col.Name,
			fmt.Sprintf("Length %d maps to NVARCHAR(MAX)", *col.CharMaxLen), ""))
	}
	return f
}

func isHeuristicDefaultType(dt string) bool {
	switch {
	case strings.Contains(dt, "TIMESTAMP"), dt == "DATE":
		return false
	case strings.Contains(dt, "CHAR"), strings.Contains(dt, "CLOB"), strings.Contains(dt, "VARCHAR"):
		return false
	case strings.Contains(dt, "BLOB"), strings.Contains(dt, "RAW"), dt == "BFILE":
		return false
	case strings.Contains(dt, "NUMBER"), strings.Contains(dt, "FLOAT"), strings.Contains(dt, "BINARY"):
		return false
	case strings.Contains(dt, "INTERVAL"), strings.Contains(dt, "XML"), strings.Contains(dt, "ROWID"):
		return false
	case dt == "LONG", strings.Contains(dt, "LONG"):
		return false
	default:
		return true
	}
}

func previewMssqlDDL(meta TableMeta) string {
	if len(meta.Columns) == 0 {
		return ""
	}
	meta.DestSchema = "dbo"
	var parts []string
	for _, col := range meta.Columns {
		parts = append(parts, fmt.Sprintf("%s %s", col.Name, MapOracleType(col)))
	}
	return strings.Join(parts, ", ")
}

func finding(sev MigrationSeverity, cat, code, table, column, msg, rec string) MigrationFinding {
	return MigrationFinding{
		Severity: sev, Category: cat, Code: code,
		Table: table, Column: column,
		Message: msg, Recommendation: rec,
	}
}

func highestSeverity(findings []MigrationFinding) MigrationSeverity {
	best := SeverityInfo
	for _, f := range findings {
		if severityRank(f.Severity) > severityRank(best) {
			best = f.Severity
		}
	}
	return best
}

func severityRank(s MigrationSeverity) int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	default:
		return 1
	}
}

func sortFindings(in []MigrationFinding) []MigrationFinding {
	out := append([]MigrationFinding(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if severityRank(out[i].Severity) != severityRank(out[j].Severity) {
			return severityRank(out[i].Severity) > severityRank(out[j].Severity)
		}
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].Code < out[j].Code
	})
	return out
}

func summarizeMigrationReport(tables []TableMigrationProfile, findings []MigrationFinding, estRows int64, rowsKnown bool, dest *DestinationProfile) MigrationReportSummary {
	sum := MigrationReportSummary{
		TableCount: len(tables), RowsEstimateKnown: rowsKnown, EstimatedRows: estRows,
		SchemaSampleSuitable: true,
		BulkMigrationReady:   true,
	}
	issueTables := make(map[string]bool)
	for _, f := range findings {
		switch f.Severity {
		case SeverityCritical:
			sum.CriticalCount++
		case SeverityWarning:
			sum.WarningCount++
		default:
			sum.InfoCount++
		}
		if f.Table != "" && f.Severity != SeverityInfo {
			issueTables[f.Table] = true
		}
	}
	sum.TablesWithIssues = len(issueTables)

	if dest != nil {
		if !dest.Reachable {
			sum.BulkMigrationReady = false
		} else if !dest.EmptyForBulk {
			sum.BulkMigrationReady = false
		}
	}
	if sum.CriticalCount > 0 {
		sum.SchemaSampleSuitable = true // still useful to test
	}
	var notes []string
	if sum.CriticalCount > 0 {
		notes = append(notes, fmt.Sprintf("%d critical issue(s) need review before production cutover", sum.CriticalCount))
	}
	if dest != nil && !dest.EmptyForBulk {
		notes = append(notes, "bulk migration blocked until destination schema is empty")
	} else if dest == nil {
		notes = append(notes, "connect a SQL Server destination in the report to check bulk readiness")
	}
	sum.IncrementalNotes = strings.Join(notes, "; ")
	return sum
}

func topRiskMessages(findings []MigrationFinding, n int) []string {
	var out []string
	for _, f := range findings {
		if f.Severity == SeverityInfo {
			continue
		}
		line := f.Message
		if f.Table != "" {
			line = f.Table + ": " + line
		}
		out = append(out, line)
		if len(out) >= n {
			break
		}
	}
	return out
}

func formatReportNum(n int64) string {
	if n >= 1_000_000_000 {
		return fmt.Sprintf("%.1fB", float64(n)/1_000_000_000)
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}
