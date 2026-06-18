package dbconn

import "testing"

func TestAssessColumnLegacyTypes(t *testing.T) {
	meta := TableMeta{Schema: "HR", Name: "DOCS"}
	findings := assessColumn(meta, ColumnMeta{Name: "BODY", DataType: "LONG"})
	if len(findings) == 0 || findings[0].Code != "type_legacy_long" {
		t.Fatalf("expected legacy long finding, got %#v", findings)
	}
}

func TestAssessTableLargeSize(t *testing.T) {
	meta := TableMeta{
		Schema: "HR", Name: "BIG", RowCount: 150_000_000, RowCountKnown: true,
		Columns: []ColumnMeta{{Name: "ID", DataType: "NUMBER"}},
		PrimaryKeys: []string{"ID"}, SyncMode: "max_key",
	}
	findings := assessTable(meta)
	found := false
	for _, f := range findings {
		if f.Code == "size_huge_table" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected size_huge_table finding")
	}
}

func TestMapOracleTypeInReportPreview(t *testing.T) {
	meta := TableMeta{
		Schema: "HR", Name: "T1",
		Columns: []ColumnMeta{{Name: "X", DataType: "VARCHAR2", CharMaxLen: int64Ptr(50)}},
	}
	ddl := previewMssqlDDL(meta)
	if ddl == "" || ddl != "X NVARCHAR(50)" {
		t.Fatalf("ddl = %q", ddl)
	}
}
