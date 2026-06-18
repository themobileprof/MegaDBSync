# MegaDBSync — Migration & Daily Sync

**Windows-only** service that migrates data from **Oracle** (source) to **SQL Server** (destination) in high-volume chunks, then runs scheduled incremental syncs. Start and stop the migration engine from the web dashboard.

## Features

- **Multiple source & destination profiles** — add, test, select Oracle sources and SQL Server destinations
- **Windows integrated SQL auth** — connect to SQL Server as the logged-in Windows user (no SQL password)
- **Empty destination guard** — bulk migrations refuse to run if the target database has any user tables
- **Chunked bulk load** — parallel table migration with configurable batch size (default 50,000 rows)
- **Incremental sync** — watermark columns, max-key, or `ORA_ROWSCN` (no Oracle schema changes)
- **Scheduled jobs** — every 4/6/12 hours or daily (when the engine is running)
- **Live dashboard** — SSE updates, engine start/stop, animated activity indicator
- **Explore tab** — list tables, sample rows, and schema for Oracle or SQL Server targets
- **Password-protected console** — first-run setup, change password in Settings

## Requirements

- Windows 10/11 or Windows Server
- Network access to Oracle and SQL Server
- Pure Go build — **no CGO or Oracle Instant Client required** (uses `go-ora`)

## Quick start (local)

```powershell
go mod tidy
go build -o megadbsync.exe .\cmd\megadbsync
.\megadbsync.exe -addr 127.0.0.1:8080
```

Open http://127.0.0.1:8080 — create an admin password, add connections, **Start engine** on the dashboard, then start a job.

State is stored under **`%ProgramData%\MegaDBSync\`** by default (override with `-data` or `%MEGADBSYNC_DATA%`). Existing installs that used `%ProgramData%\MDAS\` are detected automatically.

## Download a release binary

Permanent builds are attached to **[GitHub Releases](https://github.com/themobileprof/mdas/releases)** (tagged `v*`, e.g. `v1.0.0`).

1. Open **Releases** on the repository
2. Download **`megadbsync.exe`** from the latest release
3. Copy to e.g. `C:\MegaDBSync\megadbsync.exe` and run

CI also uploads short-lived build artifacts (30 days) on every push to `main`; releases are the long-term download location.

To publish a new release from your machine:

```powershell
git tag v1.0.0
git push origin v1.0.0
```

The **Release** workflow builds `megadbsync.exe` and attaches it to the GitHub release automatically.

## Production deployment on Windows

**[Windows deployment guide](docs/windows-deployment.md)** — full instructions for:

- Downloading `megadbsync.exe` from GitHub Releases
- Installing and upgrading on a server
- Running as a Windows Service (NSSM or `sc.exe`)
- IIS reverse proxy with HTTPS and SSE (live dashboard)
- Service accounts, firewall, SQL integrated auth, backup, and troubleshooting

## Usage notes

1. **Migration engine** — stopped by default; use **Dashboard → Start engine** before jobs or scheduled sync.
2. **Bulk migration** — destination must be completely empty (zero user tables).
3. **Incremental sync** — safe on populated destinations; uses upsert (`MERGE`).
4. **Hard deletes** — not replicated; ghost rows may remain in SQL Server (by design).
5. **Performance** — increase batch size and parallel tables for large datasets.

## Admin password

Set on first launch. Change under **Settings → Admin password**. To reset without the current password, stop the app and run:

```sql
UPDATE settings SET admin_password_hash = '' WHERE id = 1;
```

on `%ProgramData%\MegaDBSync\megadbsync.db` (or legacy `mdas.db`), then restart.

## Oracle permissions

```sql
GRANT SELECT ON schema.table TO sync_user;
```

## CI

| Workflow | Trigger | Output |
|----------|---------|--------|
| **Build** | Push/PR to `main` | Artifact `megadbsync-windows-amd64` (30 days) |
| **Release** | Tag `v*` | **`megadbsync.exe` on GitHub Releases** (permanent) |
