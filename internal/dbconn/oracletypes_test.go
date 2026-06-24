package dbconn

import "testing"

func int64Ptr(v int64) *int64 { return &v }

func TestMapOracleType(t *testing.T) {
	tests := []struct {
		name string
		col  ColumnMeta
		want string
	}{
		{"varchar2", ColumnMeta{DataType: "VARCHAR2", CharMaxLen: int64Ptr(100)}, "NVARCHAR(100)"},
		{"nvarchar2", ColumnMeta{DataType: "NVARCHAR2", CharMaxLen: int64Ptr(200)}, "NVARCHAR(200)"},
		{"varchar2 max", ColumnMeta{DataType: "VARCHAR2", CharMaxLen: int64Ptr(9000)}, "NVARCHAR(MAX)"},
		{"char fixed", ColumnMeta{DataType: "CHAR", CharMaxLen: int64Ptr(10)}, "NCHAR(10)"},
		{"number int", ColumnMeta{DataType: "NUMBER", NumericPrec: int64Ptr(9), NumericScale: int64Ptr(0)}, "INT"},
		{"number bigint", ColumnMeta{DataType: "NUMBER", NumericPrec: int64Ptr(18), NumericScale: int64Ptr(0)}, "BIGINT"},
		{"number wide int", ColumnMeta{DataType: "NUMBER", NumericPrec: int64Ptr(20), NumericScale: int64Ptr(0)}, "DECIMAL(20,0)"},
		{"number decimal", ColumnMeta{DataType: "NUMBER", NumericPrec: int64Ptr(12), NumericScale: int64Ptr(2)}, "DECIMAL(12,2)"},
		{"number unconstrained", ColumnMeta{DataType: "NUMBER"}, "FLOAT(53)"},
		{"number prec only", ColumnMeta{DataType: "NUMBER", NumericPrec: int64Ptr(5)}, "INT"},
		{"date", ColumnMeta{DataType: "DATE"}, "DATETIME2(0)"},
		{"timestamp", ColumnMeta{DataType: "TIMESTAMP(9)"}, "DATETIME2(7)"},
		{"timestamp tz", ColumnMeta{DataType: "TIMESTAMP(6) WITH TIME ZONE"}, "DATETIME2(6)"},
		{"raw", ColumnMeta{DataType: "RAW", DataLength: int64Ptr(16)}, "VARBINARY(16)"},
		{"blob", ColumnMeta{DataType: "BLOB"}, "VARBINARY(MAX)"},
		{"clob", ColumnMeta{DataType: "CLOB"}, "NVARCHAR(MAX)"},
		{"interval", ColumnMeta{DataType: "INTERVAL DAY(2) TO SECOND(6)"}, "NVARCHAR(50)"},
		{"binary float", ColumnMeta{DataType: "BINARY_FLOAT"}, "REAL"},
		{"binary double", ColumnMeta{DataType: "BINARY_DOUBLE"}, "FLOAT(53)"},
		{"xml", ColumnMeta{DataType: "XMLTYPE"}, "NVARCHAR(MAX)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MapOracleType(tc.col); got != tc.want {
				t.Fatalf("MapOracleType() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsIntegerOracleColumn(t *testing.T) {
	tests := []struct {
		name string
		col  ColumnMeta
		want bool
	}{
		{"number int", ColumnMeta{DataType: "NUMBER", NumericPrec: int64Ptr(9), NumericScale: int64Ptr(0)}, true},
		{"number decimal", ColumnMeta{DataType: "NUMBER", NumericPrec: int64Ptr(12), NumericScale: int64Ptr(2)}, false},
		{"number unconstrained", ColumnMeta{DataType: "NUMBER"}, true},
		{"integer type", ColumnMeta{DataType: "INTEGER"}, true},
		{"varchar", ColumnMeta{DataType: "VARCHAR2"}, false},
		{"date", ColumnMeta{DataType: "DATE"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsIntegerOracleColumn(tc.col); got != tc.want {
				t.Fatalf("IsIntegerOracleColumn() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsShortStringPKColumn(t *testing.T) {
	tests := []struct {
		name string
		col  ColumnMeta
		want bool
	}{
		{"varchar code", ColumnMeta{DataType: "VARCHAR2", CharMaxLen: int64Ptr(10)}, true},
		{"char code", ColumnMeta{DataType: "CHAR", CharMaxLen: int64Ptr(5)}, true},
		{"varchar too long", ColumnMeta{DataType: "VARCHAR2", CharMaxLen: int64Ptr(100)}, false},
		{"varchar unknown length", ColumnMeta{DataType: "VARCHAR2"}, false},
		{"number", ColumnMeta{DataType: "NUMBER"}, false},
		{"clob", ColumnMeta{DataType: "CLOB"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsShortStringPKColumn(tc.col); got != tc.want {
				t.Fatalf("IsShortStringPKColumn() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeValueForMSSQL(t *testing.T) {
	numCol := ColumnMeta{DataType: "NUMBER"}
	if got := NormalizeValueForMSSQL(float64(42), numCol); got != int64(42) {
		t.Fatalf("float whole number = %#v, want int64(42)", got)
	}
	textCol := ColumnMeta{DataType: "VARCHAR2"}
	if got := NormalizeValueForMSSQL([]byte("hello"), textCol); got != "hello" {
		t.Fatalf("bytes to string = %#v", got)
	}
	rawCol := ColumnMeta{DataType: "RAW"}
	raw := []byte{1, 2, 3}
	if got := NormalizeValueForMSSQL(raw, rawCol); string(got.([]byte)) != string(raw) {
		t.Fatalf("raw bytes changed = %#v", got)
	}
}
