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

---

## Download the release binary

Permanent builds are on **[GitHub Releases](https://github.com/themobileprof/mdas/releases)** (tagged `v*`, e.g. `v1.0.0`).

1. Open **Releases** → latest version (e.g. `v1.0.0`)
2. Download one of:
   - **`megadbsync-setup.exe`** — small installer; downloads `megadbsync.exe` into `C:\MegaDBSync` on first run (recommended for most users)
   - **`megadbsync.exe`** — standalone app; copy anywhere and run
3. **Setup** (recommended):

```powershell
.\megadbsync-setup.exe
# Installs to C:\MegaDBSync, registers Windows uninstall, optional logon task.
# Re-run the same file (or C:\MegaDBSync\MegaDBSync-Setup.exe) to upgrade or uninstall.
# Interactive mode shows: Run / Reinstall-upgrade / Uninstall / Exit

.\megadbsync-setup.exe -uninstall
.\megadbsync-setup.exe -upgrade
.\megadbsync-setup.exe -start -autostart -auto-start-engine   # non-interactive
```

**Uninstall:** Settings → Apps → MegaDBSync, Start Menu → Uninstall MegaDBSync, or `MegaDBSync-Setup.exe -uninstall`.

4. **Standalone** (manual path):

```powershell
New-Item -ItemType Directory -Force -Path C:\MegaDBSync\data
# Copy megadbsync.exe into C:\MegaDBSync\
Unblock-File -Path C:\MegaDBSync\megadbsync.exe
cd C:\MegaDBSync
.\megadbsync.exe -addr 127.0.0.1:8080 -data C:\MegaDBSync\data
```

> Short-lived CI artifacts (30 days) are also on every `main` build; **Releases** are the long-term download.

---

## Deploy on Windows (production checklist)

| Step | Action |
|------|--------|
| 1 | Download `megadbsync.exe` (above) to `C:\MegaDBSync\` |
| 2 | Smoke test: `.\megadbsync.exe -addr 127.0.0.1:8080` → open http://127.0.0.1:8080, set admin password, test connections |
| 3 | Install as a service (NSSM recommended) — see [Windows deployment guide](docs/windows-deployment.md#4-run-as-a-windows-service) |
| 4 | Service args: `-addr 127.0.0.1:8080 -data C:\ProgramData\MegaDBSync` |
| 5 | Grant service account read/write on `C:\ProgramData\MegaDBSync` and network access to Oracle + SQL Server |
| 6 | (Optional) HTTPS via [IIS reverse proxy](docs/iis-reverse-proxy.md) |
| 7 | Dashboard → **Start engine** → run **bulk migration** → configure **Settings** schedule for incremental sync |

Full detail (NSSM commands, firewall, SQL integrated auth, backup, troubleshooting): **[Windows deployment guide](docs/windows-deployment.md)**

### Build from source (optional)

```powershell
git clone https://github.com/themobileprof/mdas.git
cd mdas
go mod tidy
go build -trimpath -ldflags="-s -w" -o megadbsync.exe .\cmd\megadbsync
```

---

## Quick start (local dev)

```powershell
go mod tidy
go build -o megadbsync.exe .\cmd\megadbsync
.\megadbsync.exe -addr 127.0.0.1:8080
```

Open http://127.0.0.1:8080 — create an admin password, add connections, **Start engine**, then start a job.

State is stored under **`%ProgramData%\MegaDBSync\`** by default (override with `-data` or `%MEGADBSYNC_DATA%`).

### Stop the server

This stops the **HTTP process** (`megadbsync.exe`), not just the migration engine.

| How you run it | How to stop |
|----------------|-------------|
| **Terminal / dev** (foreground) | Press **Ctrl+C** in that console window |
| **Background / unknown PID** | `Get-NetTCPConnection -LocalPort 8080 \| Select OwningProcess` then `Stop-Process -Id <pid> -Force` |
| **Windows service (NSSM)** | `nssm stop MegaDBSync` or `Stop-Service MegaDBSync` |
| **Windows service (`sc`)** | `Stop-Service MegaDBSync` |

```powershell
# Quick stop when something is listening on 8080
# (Do not use $pid — PowerShell reserves $PID as the current shell's process ID.)
$procId = (Get-NetTCPConnection -LocalPort 8080 -ErrorAction SilentlyContinue | Select-Object -First 1 -ExpandProperty OwningProcess)
if ($procId) { Stop-Process -Id $procId -Force; Write-Host "Stopped process $procId" }
```

To **pause jobs but keep the UI running**, use **Dashboard → Stop engine** instead (running migrations are paused; the web server stays up).

---

## Usage notes

1. **Migration engine** — stopped by default; use **Dashboard → Start engine** before jobs or scheduled sync.
2. **Bulk migration** — destination must be completely empty (zero user tables). Establishes sync baselines for incremental.
3. **Incremental sync** — one pass per job: checks each table for changes since the last watermark/SCN and upserts. **Not** a always-on daemon — use **Settings → schedule** + **engine running** for automatic repeats.
4. **Hard deletes** — not replicated; ghost rows may remain in SQL Server (by design).
5. **Performance** — increase batch size and parallel tables for large datasets.

### How incremental sync works

```
Bulk migration (once)  →  loads all rows + saves watermark/SCN per table
Incremental sync job   →  reads Oracle WHERE watermark/SCN > last value → MERGE into SQL Server
Scheduled sync         →  engine running + cron in Settings creates incremental jobs automatically
```

**First incremental run** on a table with no baseline: sets the baseline from current Oracle high-water mark (0 rows copied). **Second run** after you change data in Oracle: copies only new/changed rows.

### Test incremental sync

1. **Bulk migrate** one small table (or full schema) into an empty SQL Server database.
2. **Dashboard → Start engine**.
3. Run **Incremental sync** — Activity log should show `no changes` or `baseline set` per table.
4. In Oracle, update or insert a row in a synced table (e.g. change a `UPDATED_AT` watermark column).
5. Run **Incremental sync** again (or wait for the schedule) — Activity log should show `N row(s) upserted` for that table.
6. Confirm the row in SQL Server (Explore tab → SQL Server connection → sample query).

Tables without a watermark column use **ORA_ROWSCN** or **max primary key** automatically (see sync mode in Activity log).

---

## Admin password

Set on first launch. Change under **Settings → Admin password**. To reset without the current password, stop the app and run:

```sql
UPDATE settings SET admin_password_hash = '' WHERE id = 1;
```

on `%ProgramData%\MegaDBSync\megadbsync.db`, then restart.

## Oracle permissions

```sql
GRANT SELECT ON schema.table TO sync_user;
```

## CI

| Workflow | Trigger | Output |
|----------|---------|--------|
| **Build** | Push/PR to `main` | Artifact `megadbsync-windows-amd64` (30 days) |
| **Release** | Tag `v*` | **`megadbsync.exe` on GitHub Releases** (permanent) |
