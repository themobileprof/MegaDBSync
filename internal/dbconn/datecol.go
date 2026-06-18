package dbconn

import (
	"fmt"
	"strings"
)

var dateColumnNames = []string{
	"CREATED_AT", "CREATE_DATE", "CREATED_DATE", "CREATE_DT", "CREATED_DT",
	"INSERT_DATE", "INSERTED_AT", "INSERT_DT", "RECORD_DATE", "ENTRY_DATE",
	"UPDATED_AT", "UPDATE_DATE", "MODIFIED_AT", "MODIFIED_DATE", "LAST_UPDATED",
	"LAST_MODIFIED", "CHANGED_AT", "CHANGE_DATE", "DATE_MODIFIED", "DT_MODIFIED",
	"TRANSACTION_DATE", "TXN_DATE", "EFFECTIVE_DATE", "BUSINESS_DATE", "POSTING_DATE",
	"EVENT_DATE", "LOG_DATE", "WORK_DATE", "SALE_DATE", "ORDER_DATE",
}

func IsDateType(dataType string) bool {
	dt := strings.ToUpper(dataType)
	return strings.Contains(dt, "DATE") || strings.Contains(dt, "TIMESTAMP")
}

func detectDateColumn(cols []ColumnMeta) string {
	for _, name := range dateColumnNames {
		for _, c := range cols {
			if strings.EqualFold(c.Name, name) && IsDateType(c.DataType) {
				return c.Name
			}
		}
	}
	for _, c := range cols {
		if IsDateType(c.DataType) {
			return c.Name
		}
	}
	return ""
}

func columnByName(cols []ColumnMeta, name string) (ColumnMeta, bool) {
	for _, c := range cols {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return ColumnMeta{}, false
}

// ResolveDateColumn picks the column used for date-range filtering.
func ResolveDateColumn(meta TableMeta, override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		col, ok := columnByName(meta.Columns, override)
		if !ok {
			return "", fmt.Errorf("date column %q not found", override)
		}
		if !IsDateType(col.DataType) {
			return "", fmt.Errorf("column %q is not a date/timestamp type", override)
		}
		return col.Name, nil
	}
	if meta.WatermarkCol != "" {
		if col, ok := columnByName(meta.Columns, meta.WatermarkCol); ok && IsDateType(col.DataType) {
			return col.Name, nil
		}
	}
	if col := detectDateColumn(meta.Columns); col != "" {
		return col, nil
	}
	return "", nil
}
