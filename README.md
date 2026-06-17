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

## Build (local)

```powershell
cd c:\projects\mdas
go mod tidy
go build -o mdas.exe .\cmd\mdas
```

The binary is tagged `//go:build windows` — build on Windows only.

## Run

```powershell
.\mdas.exe -addr 127.0.0.1:8080
```

Open http://127.0.0.1:8080 — create an admin password, add connections, start a bulk job.

### State directory (default)

On first run, data is stored under:

```
%ProgramData%\MDAS\
  mdas.db          — settings, connections, job history, sync watermarks
```

Override with `-data C:\path` or `%MDAS_DATA%`.

Connection passwords are protected with **Windows DPAPI** (bound to the machine/user, not a plain `.key` file).

## CI

Every push to `main` builds `mdas.exe` on **windows-latest** and uploads it as a workflow artifact. Download from **Actions** → latest run → **Artifacts** → `mdas-windows-amd64`.

## Install as a Windows Service (optional)

```powershell
sc.exe create MDAS binPath= "C:\MDAS\mdas.exe -addr 127.0.0.1:8080 -data C:\ProgramData\MDAS" start= auto
sc.exe start MDAS
```

Run the service account with access to Oracle, SQL Server, and (for integrated auth) appropriate Windows permissions.

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

## Project layout

```
cmd/mdas/          Main entry point (windows build tag)
internal/platform/ Windows paths (%ProgramData%)
internal/api/      HTTP API + SSE
internal/auth/     Session auth
internal/dbconn/   Oracle/MSSQL drivers, bulk insert, schema mapping
internal/jobs/     Job runner + scheduler
internal/migrate/  Bulk & incremental engines
internal/store/    SQLite persistence + DPAPI crypto
web/static/        Embedded UI
```
