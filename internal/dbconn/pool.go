package dbconn

import (
	"database/sql"
	"time"
)

func ConfigurePool(db *sql.DB, parallel int) {
	if parallel < 1 {
		parallel = 1
	}
	n := parallel + 2
	if n < 4 {
		n = 4
	}
	db.SetMaxOpenConns(n)
	db.SetMaxIdleConns(parallel)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)
}
