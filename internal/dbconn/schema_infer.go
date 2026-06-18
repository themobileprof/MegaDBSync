package dbconn

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// SchemaInferenceSampleRows is how many source rows are used to refine destination types.
const SchemaInferenceSampleRows = 5

// InferMSSQLTypes assigns MSSQLType on each column using Oracle metadata plus optional sample values.
func InferMSSQLTypes(cols []ColumnMeta, samples [][]any) []ColumnMeta {
	out := make([]ColumnMeta, len(cols))
	copy(out, cols)
	for i := range out {
		out[i].MSSQLType = inferColumnMSSQLType(out[i], sampleColumnValues(samples, i))
	}
	return out
}

func sampleColumnValues(samples [][]any, colIdx int) []any {
	var vals []any
	for _, row := range samples {
		if colIdx >= len(row) {
			continue
		}
		if row[colIdx] == nil {
			continue
		}
		vals = append(vals, row[colIdx])
	}
	return vals
}

func inferColumnMSSQLType(col ColumnMeta, samples []any) string {
	base := MapOracleType(col)
	if len(samples) == 0 {
		return base
	}

	dt := strings.ToUpper(strings.TrimSpace(col.DataType))
	switch {
	case strings.Contains(dt, "NUMBER") || dt == "INTEGER" || dt == "INT" || dt == "SMALLINT":
		return inferNumericMSSQLType(col, samples, base)
	case strings.Contains(dt, "VARCHAR") || strings.Contains(dt, "CHAR") || strings.Contains(dt, "CLOB"):
		return inferStringMSSQLType(col, samples, base)
	case dt == "DATE" || strings.Contains(dt, "TIMESTAMP"):
		return base
	default:
		return base
	}
}

func inferNumericMSSQLType(col ColumnMeta, samples []any, base string) string {
	if prec, scale, ok := oracleNumberPrecScale(col); ok {
		if scale == 0 && prec > 0 {
			return base
		}
		if scale > 0 {
			return base
		}
	}

	stats := analyzeNumericSamples(samples)
	if !stats.seenNumeric {
		return base
	}
	if stats.allInteger {
		switch {
		case stats.maxAbs <= math.MaxInt16:
			return "SMALLINT"
		case stats.maxAbs <= math.MaxInt32:
			return "INT"
		case stats.maxAbs <= int64(math.MaxInt64):
			return "BIGINT"
		default:
			return "DECIMAL(38,0)"
		}
	}
	if stats.maxScale <= 4 && stats.maxPrec <= 18 {
		return fmtDecimalType(minInt(stats.maxPrec, 18), stats.maxScale)
	}
	return fmtDecimalType(minInt(stats.maxPrec, 38), minInt(stats.maxScale, 10))
}

func inferStringMSSQLType(col ColumnMeta, samples []any, base string) string {
	maxLen := charLength(col)
	for _, v := range samples {
		s := valueAsString(v)
		if n := len([]rune(s)); n > maxLen {
			maxLen = n
		}
	}
	if maxLen <= 0 {
		return base
	}
	if maxLen > 4000 {
		return "NVARCHAR(MAX)"
	}
	if strings.HasPrefix(strings.ToUpper(base), "NCHAR(") {
		return fmt.Sprintf("NCHAR(%d)", maxLen)
	}
	return fmt.Sprintf("NVARCHAR(%d)", maxLen)
}

type numericSampleStats struct {
	seenNumeric bool
	allInteger  bool
	maxAbs      int64
	maxPrec     int
	maxScale    int
}

func analyzeNumericSamples(samples []any) numericSampleStats {
	var st numericSampleStats
	st.allInteger = true
	for _, v := range samples {
		f, prec, scale, ok := sampleAsFloat(v)
		if !ok {
			continue
		}
		st.seenNumeric = true
		abs := int64(math.Abs(f))
		if abs > st.maxAbs {
			st.maxAbs = abs
		}
		if scale > 0 || f != math.Trunc(f) {
			st.allInteger = false
		}
		if prec > st.maxPrec {
			st.maxPrec = prec
		}
		if scale > st.maxScale {
			st.maxScale = scale
		}
	}
	return st
}

func sampleAsFloat(v any) (float64, int, int, bool) {
	switch n := v.(type) {
	case int64:
		f, p, s := floatDigitStats(float64(n))
		return f, p, s, true
	case int:
		f, p, s := floatDigitStats(float64(n))
		return f, p, s, true
	case int32:
		f, p, s := floatDigitStats(float64(n))
		return f, p, s, true
	case float64:
		f, p, s := floatDigitStats(n)
		return f, p, s, true
	case float32:
		f, p, s := floatDigitStats(float64(n))
		return f, p, s, true
	case string:
		if f, ok := parseFloatString(n); ok {
			ff, p, s := floatDigitStats(f)
			return ff, p, s, true
		}
	case []byte:
		if f, ok := parseFloatString(string(n)); ok {
			ff, p, s := floatDigitStats(f)
			return ff, p, s, true
		}
	case time.Time:
		return 0, 0, 0, false
	}
	s := valueAsString(v)
	if f, ok := parseFloatString(s); ok {
		ff, p, sc := floatDigitStats(f)
		return ff, p, sc, true
	}
	return 0, 0, 0, false
}

func floatDigitStats(f float64) (float64, int, int) {
	if f != math.Trunc(f) {
		s := strconvTrimFloat(f)
		parts := strings.SplitN(s, ".", 2)
		scale := 0
		if len(parts) == 2 {
			scale = len(strings.TrimRight(parts[1], "0"))
		}
		prec := len(strings.TrimLeft(parts[0], "-"))
		if scale > 0 {
			prec += scale
		}
		return f, prec, scale
	}
	abs := int64(math.Abs(f))
	return f, digitCount(abs), 0
}

func digitCount(n int64) int {
	if n == 0 {
		return 1
	}
	count := 0
	for n > 0 {
		n /= 10
		count++
	}
	return count
}

func fmtDecimalType(prec, scale int) string {
	if prec < 1 {
		prec = 1
	}
	if scale < 0 {
		scale = 0
	}
	if scale > prec {
		scale = prec
	}
	return fmt.Sprintf("DECIMAL(%d,%d)", prec, scale)
}

func strconvTrimFloat(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", f), "0"), ".")
}
