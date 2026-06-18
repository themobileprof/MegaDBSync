package jobs

import "time"

// ScheduleLabel returns a human-readable label for a schedule expression.
func ScheduleLabel(expr string) string {
	switch expr {
	case "@every5min":
		return "Every 5 minutes"
	case "@hourly":
		return "Hourly"
	case "@daily":
		return "Daily at 2:00 AM"
	case "0 */4 * * *":
		return "Every 4 hours"
	case "0 */6 * * *":
		return "Every 6 hours"
	case "0 */12 * * *":
		return "Every 12 hours"
	default:
		return expr
	}
}

// CronDue reports whether a scheduled incremental sync should run at the given time.
func CronDue(expr string, now time.Time) bool {
	switch expr {
	case "@every5min":
		return now.Minute()%5 == 0
	case "@hourly":
		return now.Minute() == 0
	case "@daily":
		return now.Hour() == 2 && now.Minute() == 0
	case "0 */4 * * *":
		return now.Minute() == 0 && now.Hour()%4 == 0
	case "0 */6 * * *":
		return now.Minute() == 0 && now.Hour()%6 == 0
	case "0 */12 * * *":
		return now.Minute() == 0 && now.Hour()%12 == 0
	default:
		return now.Minute() == 0 && now.Hour()%4 == 0
	}
}

// NextCronRun returns the next minute boundary when CronDue would be true.
func NextCronRun(expr string, now time.Time) time.Time {
	t := now.UTC().Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 24*60; i++ {
		if CronDue(expr, t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return now.UTC().Add(24 * time.Hour)
}
