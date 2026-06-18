package dbconn

import "testing"

func TestInferMSSQLTypesFromSamples(t *testing.T) {
	cols := []ColumnMeta{
		{Name: "ID", DataType: "NUMBER"},
		{Name: "NAME", DataType: "VARCHAR2", CharMaxLen: int64Ptr(50)},
	}
	samples := [][]any{
		{int64(1), "Alice"},
		{int64(2), "Bob"},
		{int64(100000), "Charlie"},
	}
	out := InferMSSQLTypes(cols, samples)
	if out[0].MSSQLType != "INT" {
		t.Fatalf("ID type = %q, want INT", out[0].MSSQLType)
	}
	if out[1].MSSQLType != "NVARCHAR(50)" {
		t.Fatalf("NAME type = %q, want NVARCHAR(50)", out[1].MSSQLType)
	}
}

func TestInferMSSQLTypesUnconstrainedNumber(t *testing.T) {
	col := ColumnMeta{Name: "AMT", DataType: "NUMBER"}
	samples := [][]any{{"12.34"}, {"0.5"}}
	out := InferMSSQLTypes([]ColumnMeta{col}, samples)
	if out[0].MSSQLType != "DECIMAL(4,2)" && out[0].MSSQLType != "DECIMAL(3,2)" {
		t.Fatalf("decimal inference = %q", out[0].MSSQLType)
	}
}

func TestCoerceValueStringToInt(t *testing.T) {
	col := ColumnMeta{DataType: "NUMBER", MSSQLType: "INT"}
	got := CoerceValueForMSSQL("42", col)
	if got != int64(42) {
		t.Fatalf("coerce = %#v, want int64(42)", got)
	}
}

func TestNormalizeValueStringNumber(t *testing.T) {
	col := ColumnMeta{DataType: "NUMBER"}
	got := NormalizeValueForMSSQL("99", col)
	if got != int64(99) {
		t.Fatalf("normalize = %#v, want int64(99)", got)
	}
}
