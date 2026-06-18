package dbconn

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// CoerceRowForMSSQL converts values to match inferred/destination SQL Server column types.
func CoerceRowForMSSQL(cols []ColumnMeta, row []any) []any {
	out := make([]any, len(row))
	for i, v := range row {
		col := ColumnMeta{}
		if i < len(cols) {
			col = cols[i]
		}
		out[i] = CoerceValueForMSSQL(v, col)
	}
	return out
}

// CoerceValueForMSSQL applies destination-type-driven coercion after basic normalization.
func CoerceValueForMSSQL(v any, col ColumnMeta) any {
	v = NormalizeValueForMSSQL(v, col)
	if v == nil {
		return nil
	}
	target := strings.ToUpper(EffectiveMSSQLType(col))
	switch {
	case isMSSQLIntegerType(target):
		if n, ok := coerceToInt64(v); ok {
			return n
		}
	case isMSSQLDecimalType(target):
		if f, ok := coerceToFloat64(v); ok {
			return f
		}
	case strings.HasPrefix(target, "REAL") || strings.HasPrefix(target, "FLOAT"):
		if f, ok := coerceToFloat64(v); ok {
			return f
		}
	case strings.Contains(target, "CHAR") || strings.Contains(target, "TEXT") || target == "XML":
		return valueAsString(v)
	case strings.Contains(target, "BINARY"):
		switch b := v.(type) {
		case []byte:
			return b
		case string:
			return []byte(b)
		}
	case strings.Contains(target, "DATE") || strings.Contains(target, "TIME"):
		if t, ok := coerceToTime(v); ok {
			return t
		}
	}
	return v
}

func isMSSQLIntegerType(t string) bool {
	switch {
	case t == "INT", t == "BIGINT", t == "SMALLINT", t == "TINYINT", t == "BIT":
		return true
	default:
		return false
	}
}

func isMSSQLDecimalType(t string) bool {
	return strings.HasPrefix(t, "DECIMAL") || strings.HasPrefix(t, "NUMERIC")
}

func coerceToInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case float64:
		if n != math.Trunc(n) {
			return 0, false
		}
		return int64(n), true
	case float32:
		f := float64(n)
		if f != math.Trunc(f) {
			return 0, false
		}
		return int64(f), true
	case string:
		return parseIntString(n)
	case []byte:
		return parseIntString(string(n))
	default:
		return parseIntString(valueAsString(v))
	}
}

func coerceToFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	case string:
		return parseFloatString(n)
	case []byte:
		return parseFloatString(string(n))
	default:
		return parseFloatString(valueAsString(v))
	}
}

func coerceToTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case string:
		for _, layout := range []string{
			time.RFC3339Nano, time.RFC3339,
			"2006-01-02 15:04:05", "2006-01-02",
		} {
			if parsed, err := time.Parse(layout, t); err == nil {
				return parsed, true
			}
		}
	}
	return time.Time{}, false
}

func parseIntString(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err == nil {
		return n, true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f != math.Trunc(f) {
		return 0, false
	}
	return int64(f), true
}

func parseFloatString(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

func valueAsString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}
