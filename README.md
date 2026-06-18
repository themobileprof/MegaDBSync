# MDAS — Migration & Daily Sync



**Windows-only** service that migrates data from **Oracle** (source) to **SQL Server** (destination) in high-volume chunks, then runs scheduled incremental syncs. The web UI is a live view only — close the browser anytime; background workers keep running until you stop the process.



## Features



- **Multiple source & destination profiles** — add, test, select Oracle sources and SQL Server destinations

- **Windows integrated SQL auth** — connect to SQL Server as the logged-in Windows user (no SQL password)

- **Empty destination guard** — bulk migrations refuse to run if the target database has any user tables

- **Chunked bulk load** — parallel table migration with configurable batch size (default 50,000 rows)

- **Incremental sync** — watermark columns, max-key, or `ORA_ROWSCN` (no Oracle schema changes)

- **Scheduled jobs** — every 4/6/12 hours or daily

- **Live dashboard** — SSE updates, animated activity indicator, reconnect anytime for exact state

- **Password-protected console** — first-run setup, change password in Settings



## Requirements



- Windows 10/11 or Windows Server

- Network access to Oracle and SQL Server

- Pure Go build — **no CGO or Oracle Instant Client required** (uses `go-ora`)



## Quick start (local)



```powershell

go mod tidy

go build -o mdas.exe .\cmd\mdas

.\mdas.exe -addr 127.0.0.1:8080

```



Open http://127.0.0.1:8080 — create an admin password, add connections, start a job.



State is stored under **`%ProgramData%\MDAS\`** by default (override with `-data` or `%MDAS_DATA%`).



## Production deployment on Windows



**[Windows deployment guide](docs/windows-deployment.md)** — full instructions for:



- Downloading `mdas.exe` from GitHub Actions artifacts

- Installing and upgrading on a server

- Running as a Windows Service (NSSM or `sc.exe`)

- IIS reverse proxy with HTTPS and SSE (live dashboard)

- Service accounts, firewall, SQL integrated auth, backup, and troubleshooting



## Usage notes



1. **Bulk migration** — destination must be completely empty (zero user tables).

2. **Incremental sync** — safe on populated destinations; uses upsert (`MERGE`).

3. **Hard deletes** — not replicated; ghost rows may remain in SQL Server (by design).

4. **Performance** — increase batch size and parallel tables for large datasets. Bulk reads use ROWID paging; bulk writes use SQL Server `CopyIn` with table lock hint on empty tables.



## Admin password



Set on first launch. Change under **Settings → Admin password**. To reset without the current password, stop the app and run:



```sql

UPDATE settings SET admin_password_hash = '' WHERE id = 1;

```



on `%ProgramData%\MDAS\mdas.db`, then restart.



## Oracle permissions



```sql

GRANT SELECT ON schema.table TO sync_user;

```



## CI



Every push to `main` builds `mdas.exe` on **windows-latest** and uploads **`mdas-windows-amd64`** as a workflow artifact (retained 30 days). See the [deployment guide](docs/windows-deployment.md#2-download-the-binary) for download steps.



## Project layout



```

cmd/mdas/              Main entry point (windows build tag)

docs/                  Deployment documentation

internal/platform/     Windows paths (%ProgramData%)

internal/api/          HTTP API + SSE

internal/auth/         Session auth

internal/dbconn/       Oracle/MSSQL drivers, bulk insert, schema mapping

internal/jobs/         Job runner + scheduler

internal/migrate/      Bulk & incremental engines

internal/store/        SQLite persistence + DPAPI crypto

web/static/            Embedded UI

```


