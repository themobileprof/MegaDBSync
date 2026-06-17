# MDAS — Migration & Daily Sync

Windows-friendly Go service that migrates data from **Oracle** (source) to **SQL Server** (destination) in high-volume chunks, then runs scheduled incremental syncs. The web UI is a live view only — close the browser anytime; background workers keep running until you stop the process.

## Features

- **Multiple source & destination profiles** — add, test, select Oracle sources and SQL Server destinations
- **Empty destination guard** — bulk migrations refuse to run if the target database has any user tables
- **Chunked bulk load** — parallel table migration with configurable batch size (default 50,000 rows)
- **Incremental sync** — watermark columns, max-key, or `ORA_ROWSCN` (no Oracle schema changes)
- **Scheduled jobs** — every 4/6/12 hours or daily
- **Live dashboard** — SSE updates, animated activity indicator, reconnect anytime for exact state
- **Password-protected console** — first-run setup, session cookies

## Requirements

- Go 1.22+
- Network access to Oracle and SQL Server
- Pure Go build — **no CGO or Oracle Instant Client required** (uses `go-ora`)

## Build

```powershell
cd c:\projects\mdas
go mod tidy
go build -o mdas.exe .\cmd\mdas
```

### CI

Every push to `main` triggers [GitHub Actions](.github/workflows/build.yml), which cross-compiles **Windows** and **Linux** binaries (`CGO_ENABLED=0`) and uploads them as workflow artifacts (`mdas-windows-amd64`, `mdas-linux-amd64`). Download from the **Actions** tab → latest run → **Artifacts**.

## Run

```powershell
.\mdas.exe -addr 127.0.0.1:8080
```

Open http://127.0.0.1:8080 — create an admin password, add connections, start a bulk job.

Data and encrypted credentials are stored under `%MDAS_DATA%` or `data/` next to the executable.

## Usage notes

1. **Bulk migration** — destination must be completely empty (zero user tables). The app checks before every bulk job.
2. **Incremental sync** — safe to run against populated destinations; uses upsert (`MERGE`).
3. **Hard deletes** — not replicated; ghost rows may remain in SQL Server (by design).
4. **Performance** — increase batch size and parallel tables in Settings/Jobs for large datasets. SQL Server bulk insert (`CopyIn`) is used for initial loads. Bulk reads use ROWID-based paging (not OFFSET) so very large tables stay fast.

## Admin password

Set on first launch. Change anytime under **Settings → Admin password**. Minimum 8 characters. To fully reset without the current password, stop the app and run:

```sql
UPDATE settings SET admin_password_hash = '' WHERE id = 1;
```

on `data/mdas.db`, then restart.

## Oracle permissions

```sql
GRANT SELECT ON schema.table TO sync_user;
-- or read access to required schemas
```

## Project layout

```
cmd/mdas/          Main entry point
internal/api/      HTTP API + SSE
internal/auth/     Session auth
internal/dbconn/   Oracle/MSSQL drivers, bulk insert, schema mapping
internal/jobs/     Job runner + scheduler
internal/migrate/  Bulk & incremental engines
internal/store/    SQLite persistence
web/static/        Embedded UI (no CSS frameworks)
```
