package migrate

import (
	"fmt"
	"strings"
	"time"
)

// DateBounds holds optional inclusive-from / inclusive-to filters. Empty inputs mean unbounded.
type DateBounds struct {
	Active      bool
	HasFrom     bool
	HasTo       bool
	From        time.Time
	ToExclusive time.Time // SQL uses col < ToExclusive
	FromRaw     string
	ToRaw       string
}

func ParseDateBounds(from, to string) (DateBounds, error) {
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	b := DateBounds{FromRaw: from, ToRaw: to}
	if from == "" && to == "" {
		return b, nil
	}
	b.Active = true
	if from != "" {
		t, dateOnly, err := parseDateInput(from)
		if err != nil {
			return b, fmt.Errorf("invalid date_from: %w", err)
		}
		b.HasFrom = true
		b.From = t
		if dateOnly {
			b.From = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		}
	}
	if to != "" {
		t, dateOnly, err := parseDateInput(to)
		if err != nil {
			return b, fmt.Errorf("invalid date_to: %w", err)
		}
		b.HasTo = true
		if dateOnly {
			// Inclusive end-of-day via exclusive next midnight.
			start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
			b.ToExclusive = start.Add(24 * time.Hour)
		} else {
			b.ToExclusive = t.Add(time.Nanosecond)
		}
	}
	if b.HasFrom && b.HasTo && !b.From.Before(b.ToExclusive) {
		return b, fmt.Errorf("date_from must be before date_to")
	}
	return b, nil
}

func parseDateInput(s string) (t time.Time, dateOnly bool, err error) {
	layouts := []struct {
		layout   string
		dateOnly bool
	}{
		{"2006-01-02", true},
		{time.RFC3339, false},
		{"2006-01-02T15:04:05", false},
		{"2006-01-02 15:04:05", false},
	}
	for _, l := range layouts {
		if parsed, e := time.Parse(l.layout, s); e == nil {
			return parsed.UTC(), l.dateOnly, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("use YYYY-MM-DD or RFC3339 datetime")
}

func (b DateBounds) Summary() string {
	if !b.Active {
		return "all records"
	}
	switch {
	case b.HasFrom && b.HasTo:
		return fmt.Sprintf("%s to %s", b.FromRaw, b.ToRaw)
	case b.HasFrom:
		return fmt.Sprintf("from %s", b.FromRaw)
	case b.HasTo:
		return fmt.Sprintf("through %s", b.ToRaw)
	default:
		return "all records"
	}
}
