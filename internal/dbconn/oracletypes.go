package dbconn

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// MapOracleType returns the SQL Server column type for Oracle metadata.
func MapOracleType(c ColumnMeta) string {
	dt := strings.ToUpper(strings.TrimSpace(c.DataType))

	if strings.Contains(dt, "TIMESTAMP") {
		return fmt.Sprintf("DATETIME2(%d)", timestampFractionalDigits(dt))
	}
	if dt == "DATE" {
		return "DATETIME2(0)"
	}
	if strings.Contains(dt, "INTERVAL") {
		return "NVARCHAR(50)"
	}
	if dt == "RAW" || strings.Contains(dt, "LONG RAW") {
		if n := rawByteLength(c); n > 0 && n <= 8000 {
			return fmt.Sprintf("VARBINARY(%d)", n)
		}
		return "VARBINARY(MAX)"
	}
	if strings.Contains(dt, "BLOB") || dt == "BFILE" {
		return "VARBINARY(MAX)"
	}
	if strings.Contains(dt, "CLOB") || dt == "LONG" || strings.Contains(dt, "XML") {
		return "NVARCHAR(MAX)"
	}
	if dt == "NCHAR" || strings.HasPrefix(dt, "NCHAR ") {
		return fmt.Sprintf("NCHAR(%d)", boundedCharLen(c, 1, 4000))
	}
	if isFixedChar(dt) {
		return fmt.Sprintf("NCHAR(%d)", boundedCharLen(c, 1, 4000))
	}
	if strings.Contains(dt, "VARCHAR") || strings.Contains(dt, "VARCHAR2") || strings.Contains(dt, "NVARCHAR") {
		n := charLength(c)
		if n <= 0 || n > 4000 {
			return "NVARCHAR(MAX)"
		}
		return fmt.Sprintf("NVARCHAR(%d)", n)
	}
	if dt == "BINARY_FLOAT" {
		return "REAL"
	}
	if dt == "BINARY_DOUBLE" || dt == "FLOAT" || dt == "DOUBLE PRECISION" || dt == "REAL" {
		return "FLOAT(53)"
	}
	if strings.Contains(dt, "NUMBER") || dt == "INTEGER" || dt == "INT" || dt == "SMALLINT" {
		return mapOracleNumber(c)
	}
	if strings.Contains(dt, "ROWID") {
		return "CHAR(18)"
	}
	return "NVARCHAR(MAX)"
}

func mapOracleNumber(c ColumnMeta) string {
	prec, scale, ok := oracleNumberPrecScale(c)
	if !ok {
		return "FLOAT(53)"
	}
	if scale == 0 {
		switch {
		case prec <= 4:
			return "SMALLINT"
		case prec <= 9:
			return "INT"
		case prec <= 18:
			return "BIGINT"
		default:
			return fmt.Sprintf("DECIMAL(%d,0)", minInt(prec, 38))
		}
	}
	prec = minInt(prec, 38)
	scale = minInt(scale, prec)
	if scale < 0 {
		scale = 0
	}
	return fmt.Sprintf("DECIMAL(%d,%d)", prec, scale)
}

// IsIntegerOracleColumn reports whether a column stores whole numbers (eligible as an integer FK).
func IsIntegerOracleColumn(c ColumnMeta) bool {
	dt := strings.ToUpper(strings.TrimSpace(c.DataType))
	switch {
	case dt == "INTEGER" || dt == "INT" || dt == "SMALLINT":
		return true
	case strings.Contains(dt, "NUMBER"):
		_, scale, ok := oracleNumberPrecScale(c)
		if !ok {
			return true // unconstrained NUMBER — common for ID columns
		}
		return scale == 0
	default:
		return false
	}
}

// MaxShortStringPKLength is the max character length for inferred string-key FK suggestions.
const MaxShortStringPKLength = 50

// IsShortStringPKColumn reports whether a column is a bounded string suitable as a code/PK FK.
func IsShortStringPKColumn(c ColumnMeta) bool {
	dt := strings.ToUpper(strings.TrimSpace(c.DataType))
	if strings.Contains(dt, "CLOB") || strings.Contains(dt, "BLOB") {
		return false
	}
	if !(strings.Contains(dt, "VARCHAR") || strings.Contains(dt, "CHAR") || strings.Contains(dt, "NCHAR")) {
		return false
	}
	n := charLength(c)
	return n > 0 && n <= MaxShortStringPKLength
}

func oracleNumberPrecScale(c ColumnMeta) (prec, scale int, ok bool) {
	if c.NumericPrec == nil && c.NumericScale == nil {
		return 0, 0, false
	}
	if c.NumericPrec != nil {
		prec = int(*c.NumericPrec)
	}
	if c.NumericScale != nil {
		scale = int(*c.NumericScale)
	} else if c.NumericPrec != nil {
		scale = 0
	}
	if prec == 0 && scale == 0 && c.NumericPrec == nil {
		return 0, 0, false
	}
	return prec, scale, true
}

func isFixedChar(dt string) bool {
	if !strings.Contains(dt, "CHAR") {
		return false
	}
	return !strings.Contains(dt, "VARCHAR")
}

func boundedCharLen(c ColumnMeta, minLen, maxLen int) int {
	n := charLength(c)
	if n < minLen {
		return minLen
	}
	if n > maxLen {
		return maxLen
	}
	return n
}

func charLength(c ColumnMeta) int {
	if c.CharMaxLen != nil && *c.CharMaxLen > 0 {
		return int(*c.CharMaxLen)
	}
	if c.DataLength != nil && *c.DataLength > 0 {
		return int(*c.DataLength)
	}
	return 0
}

func rawByteLength(c ColumnMeta) int {
	if c.DataLength != nil && *c.DataLength > 0 {
		return int(*c.DataLength)
	}
	if c.CharMaxLen != nil && *c.CharMaxLen > 0 {
		return int(*c.CharMaxLen)
	}
	return 0
}

func timestampFractionalDigits(dt string) int {
	open := strings.Index(dt, "(")
	if open < 0 {
		return 6
	}
	close := strings.Index(dt[open:], ")")
	if close < 0 {
		return 6
	}
	n, err := strconv.Atoi(strings.TrimSpace(dt[open+1 : open+close]))
	if err != nil || n < 0 {
		return 0
	}
	if n > 7 {
		return 7
	}
	return n
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// NormalizeRowsForMSSQL converts Oracle driver values into forms SQL Server accepts.
func NormalizeRowsForMSSQL(cols []ColumnMeta, rows [][]any) [][]any {
	if len(rows) == 0 {
		return rows
	}
	out := make([][]any, len(rows))
	for i, row := range rows {
		out[i] = NormalizeRowForMSSQL(cols, row)
	}
	return out
}

// NormalizeRowForMSSQL converts one row of Oracle values for SQL Server insert/merge.
func NormalizeRowForMSSQL(cols []ColumnMeta, row []any) []any {
	out := make([]any, len(row))
	for i, v := range row {
		col := ColumnMeta{}
		if i < len(cols) {
			col = cols[i]
		}
		out[i] = NormalizeValueForMSSQL(v, col)
	}
	return out
}

// NormalizeValueForMSSQL converts a single cell value for SQL Server.
func NormalizeValueForMSSQL(v any, col ColumnMeta) any {
	if v == nil {
		return nil
	}
	if unwrapped, ok := unwrapSQLNull(v); ok {
		if unwrapped == nil {
			return nil
		}
		v = unwrapped
	}

	dt := strings.ToUpper(col.DataType)
	switch val := v.(type) {
	case []byte:
		if isBinaryOracleType(dt) {
			return val
		}
		return string(val)
	case string:
		if isBinaryOracleType(dt) {
			if b, err := hex.DecodeString(val); err == nil {
				return b
			}
		}
		if isOracleNumericType(dt) {
			if n, ok := parseIntString(val); ok {
				return n
			}
			if f, ok := parseFloatString(val); ok {
				return normalizeFloat(f, dt)
			}
		}
		return val
	case time.Time:
		if strings.Contains(dt, "TIME ZONE") || strings.Contains(dt, "LOCAL TIME ZONE") {
			return val.UTC()
		}
		return val
	case float64:
		return normalizeFloat(val, dt)
	case float32:
		return normalizeFloat(float64(val), dt)
	case int64:
		return val
	case int:
		return int64(val)
	case int32:
		return int64(val)
	case int16:
		return int64(val)
	case int8:
		return int64(val)
	case uint64:
		if val <= math.MaxInt64 {
			return int64(val)
		}
		return fmt.Sprint(val)
	case uint, uint32, uint16, uint8:
		return int64(reflectUint(val))
	default:
		s := fmt.Sprint(val)
		if isOracleNumericType(dt) {
			if n, ok := parseIntString(s); ok {
				return n
			}
			if f, ok := parseFloatString(s); ok {
				return normalizeFloat(f, dt)
			}
		}
		if isBinaryOracleType(dt) {
			if b, err := hex.DecodeString(s); err == nil {
				return b
			}
		}
		return v
	}
}

func isOracleNumericType(dt string) bool {
	return strings.Contains(dt, "NUMBER") ||
		strings.Contains(dt, "INT") ||
		strings.Contains(dt, "FLOAT") ||
		dt == "INTEGER" ||
		dt == "SMALLINT"
}

func reflectUint(v any) uint64 {
	switch n := v.(type) {
	case uint:
		return uint64(n)
	case uint32:
		return uint64(n)
	case uint16:
		return uint64(n)
	case uint8:
		return uint64(n)
	default:
		return 0
	}
}

func unwrapSQLNull(v any) (any, bool) {
	switch n := v.(type) {
	case sql.NullString:
		if !n.Valid {
			return nil, true
		}
		return n.String, true
	case sql.NullInt64:
		if !n.Valid {
			return nil, true
		}
		return n.Int64, true
	case sql.NullFloat64:
		if !n.Valid {
			return nil, true
		}
		return n.Float64, true
	case sql.NullBool:
		if !n.Valid {
			return nil, true
		}
		return n.Bool, true
	case sql.NullTime:
		if !n.Valid {
			return nil, true
		}
		return n.Time, true
	case sql.NullByte:
		if !n.Valid {
			return nil, true
		}
		return []byte{n.Byte}, true
	default:
		return nil, false
	}
}

func normalizeFloat(val float64, dt string) any {
	if isOracleNumericType(dt) {
		if val == math.Trunc(val) && val >= float64(math.MinInt64) && val <= float64(math.MaxInt64) {
			return int64(val)
		}
	}
	if strings.Contains(dt, "BINARY_FLOAT") {
		return float32(val)
	}
	return val
}

func isBinaryOracleType(dt string) bool {
	dt = strings.ToUpper(dt)
	return strings.Contains(dt, "BLOB") ||
		strings.Contains(dt, "RAW") ||
		dt == "BFILE" ||
		strings.Contains(dt, "LONG RAW")
}
