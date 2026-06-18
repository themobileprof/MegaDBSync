# Windows deployment guide

Complete instructions for downloading MegaDBSync, running it as a Windows service, and exposing the web UI through IIS with HTTPS.

---

## Overview

```
[Browser] --HTTPS:443--> [IIS reverse proxy] --HTTP--> [megadbsync.exe on 127.0.0.1:8080]
                                                              |
                              +-------------------------------+
                              |
                              v
                    [Oracle DB]     [SQL Server]
```

MegaDBSync is a **single executable** (`megadbsync.exe`). It embeds the web UI and runs migration jobs in-process. Persistent state lives in **`%ProgramData%\MegaDBSync`** (SQLite database, DPAPI-protected credentials).

| Component | Role |
|-----------|------|
| `megadbsync.exe` | HTTP server, job engine, scheduler |
| `%ProgramData%\MegaDBSync\` | Config, connections, sync watermarks, job history |
| Windows Service (or NSSM) | Keeps MegaDBSync running after logoff/reboot |
| IIS + ARR | HTTPS, reverse proxy, optional domain access |

---

## 1. Prerequisites

### Server

- Windows 10/11 or Windows Server 2016+
- Outbound network access to **Oracle** (typically TCP **1521**) and **SQL Server** (TCP **1433**)
- Administrator rights for service install and IIS configuration

### Software (pick what you need)

| Purpose | Install |
|---------|---------|
| Run MegaDBSync | `megadbsync.exe` only (no Go runtime, no Oracle Instant Client) |
| Windows Service wrapper | [NSSM](https://nssm.cc/download) (recommended) or built-in `sc.exe` |
| HTTPS reverse proxy | IIS + **URL Rewrite** + **Application Request Routing (ARR)** |

### Firewall (typical production layout)

| Port | Exposure | Notes |
|------|----------|-------|
| **443** | External (via IIS) | HTTPS to the admin UI |
| **8080** | **Localhost only** | MegaDBSync bind address `127.0.0.1:8080` — not reachable from the network |
| 1521 / 1433 | Outbound from server | To Oracle / SQL Server |

### Service account permissions

The account running MegaDBSync needs:

- **Read/write** to `%ProgramData%\MegaDBSync` (or your `-data` path)
- **Network** access to Oracle and SQL Server
- If using **Windows integrated SQL auth**: the service account must be a valid SQL Server login (Windows user/group mapped in SQL Server)
- If using **SQL login**: credentials are stored in MegaDBSync (DPAPI-encrypted)

Use a dedicated domain account (e.g. `DOMAIN\svc-megadbsync`) rather than LocalSystem when using integrated SQL auth or Kerberos-heavy environments.

---

## 2. Download the binary

### Option A — GitHub Release (recommended, permanent)

Tagged releases attach **`megadbsync.exe`** indefinitely (not subject to the 30-day artifact limit).

1. Open **Releases**: `https://github.com/themobileprof/mdas/releases`
2. Download **`megadbsync.exe`** from the latest release (e.g. `v1.0.0`)
3. Copy to `C:\MegaDBSync\megadbsync.exe`

To publish a release, push a version tag:

```powershell
git tag v1.0.0
git push origin v1.0.0
```

The **Release** workflow builds and uploads the binary automatically.

### Option B — GitHub Actions artifact (30 days)

Every push to `main` also uploads a build artifact retained **30 days**:

1. Open `https://github.com/themobileprof/mdas` → **Actions**
2. Latest green **Build** run → **Artifacts** → **`megadbsync-windows-amd64`**
3. Extract `megadbsync.exe`

Verify the file is unblocked (Windows may mark downloaded files):

```powershell
Unblock-File -Path C:\MegaDBSync\megadbsync.exe
```

Check version/build by running once in a console (Ctrl+C to stop):

```powershell
C:\MegaDBSync\megadbsync.exe
# Expect: "MegaDBSync — Oracle to SQL Server migration & sync (Windows)"
#         "MegaDBSync listening on http://127.0.0.1:8080"
```

### Option C — Build from source

Requires **Go 1.22+** on a Windows machine:

```powershell
git clone https://github.com/themobileprof/mdas.git
cd mdas
go mod tidy
go build -trimpath -ldflags="-s -w" -o megadbsync.exe .\cmd\megadbsync
```

---

## 3. Install on the server

### Recommended folder layout

```powershell
New-Item -ItemType Directory -Force -Path C:\MegaDBSync
Copy-Item .\megadbsync.exe C:\MegaDBSync\megadbsync.exe
```

| Path | Contents |
|------|----------|
| `C:\MegaDBSync\megadbsync.exe` | Application binary |
| `C:\ProgramData\MegaDBSync\` | Created automatically on first run — **do not delete** |

State directory resolution order:

1. `-data` flag on command line
2. `%MEGADBSYNC_DATA%` environment variable (legacy: `%MDAS_DATA%`)
3. `%ProgramData%\MegaDBSync`
4. `%LOCALAPPDATA%\MegaDBSync`
5. `data\` next to the executable

### Command-line flags

| Flag | Default | Description |
|------|---------|-------------|
| `-addr` | `127.0.0.1:8080` | HTTP listen address. Use `127.0.0.1` when IIS terminates TLS in front. |
| `-data` | `%ProgramData%\MegaDBSync` | SQLite and DPAPI state directory |

### First run (interactive smoke test)

Before installing as a service, run manually:

```powershell
cd C:\MegaDBSync
.\megadbsync.exe -addr 127.0.0.1:8080
```

1. Open **http://127.0.0.1:8080**
2. Create the **admin password** (minimum 8 characters)
3. Add Oracle source and SQL Server destination connections — use **Test**
4. Stop with Ctrl+C when satisfied

---

## 4. Run as a Windows Service

MegaDBSync is a standard console application. It does **not** implement the Windows Service Control Manager protocol internally. For production, use **NSSM** (recommended) so the process restarts on failure and stops cleanly.

### Option A — NSSM (recommended)

1. Download NSSM from [nssm.cc](https://nssm.cc/download) and extract `nssm.exe` (64-bit) to `C:\MegaDBSync\`

2. Install the service:

```powershell
cd C:\MegaDBSync
.\nssm.exe install MegaDBSync "C:\MegaDBSync\megadbsync.exe"
.\nssm.exe set MegaDBSync AppDirectory "C:\MegaDBSync"
.\nssm.exe set MegaDBSync AppParameters "-addr 127.0.0.1:8080 -data C:\ProgramData\MegaDBSync"
.\nssm.exe set MegaDBSync DisplayName "MegaDBSync Database Sync"
.\nssm.exe set MegaDBSync Description "Oracle to SQL Server migration and incremental sync"
.\nssm.exe set MegaDBSync Start SERVICE_AUTO_START
.\nssm.exe set MegaDBSync AppStdout "C:\ProgramData\MegaDBSync\megadbsync.log"
.\nssm.exe set MegaDBSync AppStderr "C:\ProgramData\MegaDBSync\megadbsync-error.log"
.\nssm.exe set MegaDBSync AppRotateFiles 1
.\nssm.exe set MegaDBSync AppRotateBytes 10485760
```

3. Set the service account (important for integrated SQL auth):

```powershell
# GUI — easiest for domain account + password
.\nssm.exe edit MegaDBSync
# Log on tab → This account → DOMAIN\svc-megadbsync

# Or via services.msc: Win+R → services.msc → MegaDBSync → Properties → Log On
```

4. Grant the account modify rights on the data folder:

```powershell
icacls "C:\ProgramData\MegaDBSync" /grant "DOMAIN\svc-megadbsync:(OI)(CI)M" /T
```

5. Start the service:

```powershell
.\nssm.exe start MegaDBSync
# Or:
Start-Service MegaDBSync
```

6. Verify:

```powershell
Get-Service MegaDBSync
Invoke-WebRequest -Uri http://127.0.0.1:8080/api/bootstrap -UseBasicParsing
```

**Manage NSSM service:**

```powershell
.\nssm.exe stop MegaDBSync
.\nssm.exe restart MegaDBSync
.\nssm.exe remove MegaDBSync confirm
```

### Option B — sc.exe (minimal)

Works for simple setups; less graceful shutdown and no automatic restart on crash.

```powershell
sc.exe create MegaDBSync binPath= "C:\MegaDBSync\megadbsync.exe -addr 127.0.0.1:8080 -data C:\ProgramData\MegaDBSync" start= auto DisplayName= "MegaDBSync Database Sync"
sc.exe description MegaDBSync "Oracle to SQL Server migration and incremental sync"
sc.exe config MegaDBSync obj= "DOMAIN\svc-megadbsync" password= "YourPassword"
sc.exe start MegaDBSync
```

> **Note:** `binPath=` must be followed by a **space** before the opening quote. Configure the service account under `services.msc` if `sc config` password handling is awkward.

---

## 5. IIS reverse proxy (HTTPS)

Expose the UI on port **443** while MegaDBSync stays on localhost **8080**.

### 5.1 Install IIS features

On Windows Server (PowerShell as Administrator):

```powershell
Install-WindowsFeature Web-Server, Web-WebSockets, Web-Mgmt-Console
```

On Windows 10/11 (optional components):

```powershell
Enable-WindowsOptionalFeature -Online -FeatureName IIS-WebServerRole, IIS-WebServer, IIS-ManagementConsole -All
```

### 5.2 Install URL Rewrite and ARR

1. Install **IIS URL Rewrite Module 2**: [download](https://www.iis.net/downloads/microsoft/url-rewrite)
2. Install **Application Request Routing 3**: [download](https://www.iis.net/downloads/microsoft/application-request-routing)

Enable the ARR proxy (once per server):

1. Open **IIS Manager**
2. Click the **server node** (top level, not a site)
3. Open **Application Request Routing Cache**
4. Click **Server Proxy Settings** (right panel)
5. Check **Enable proxy**
6. Click **Apply**

Or via PowerShell (after ARR is installed):

```powershell
Import-Module WebAdministration
Set-WebConfigurationProperty -pspath 'MACHINE/WEBROOT/APPHOST' -filter "system.webServer/proxy" -name "enabled" -value "True"
```

### 5.3 Create the IIS site

Example: host name `megadbsync.contoso.local`, site root `C:\inetpub\megadbsync-proxy` (only holds `web.config`).

```powershell
New-Item -ItemType Directory -Force -Path C:\inetpub\megadbsync-proxy
New-Website -Name "MegaDBSync" -Port 443 -PhysicalPath "C:\inetpub\megadbsync-proxy" -HostHeader "megadbsync.contoso.local" -Ssl
```

Bind an HTTPS certificate:

- **IIS Manager** → site **MegaDBSync** → **Bindings** → HTTPS → select your certificate
- Or use `New-SelfSignedCertificate` for lab/testing only

### 5.4 web.config — reverse proxy + SSE support

MegaDBSync uses **Server-Sent Events** (`/api/events`) for the live dashboard. IIS must not buffer the proxied response.

Create `C:\inetpub\megadbsync-proxy\web.config`:

Copy from [`docs/iis-web.config.example`](iis-web.config.example) in this repository, or use:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<configuration>
  <system.webServer>
    <webSocket enabled="false" />
    <proxy enabled="true" preserveHostHeader="false" reverseRewriteHostInResponseHeaders="false" />

    <rewrite>
      <rules>
        <rule name="MegaDBSync reverse proxy" stopProcessing="true">
          <match url="(.*)" />
          <action type="Rewrite" url="http://127.0.0.1:8080/{R:1}" />
          <serverVariables>
            <set name="HTTP_X_FORWARDED_PROTO" value="https" />
            <set name="HTTP_X_FORWARDED_FOR" value="{REMOTE_ADDR}" />
          </serverVariables>
        </rule>
      </rules>
    </rewrite>

    <!-- Allow server variables (first time only — see step 5.5) -->
    <security>
      <requestFiltering allowDoubleEscaping="true" />
    </security>
  </system.webServer>
</configuration>
```

Allow the rewrite module to set server variables. In IIS Manager:

1. Server node → **URL Rewrite**
2. **View Server Variables** (right panel) → Add:
   - `HTTP_X_FORWARDED_PROTO`
   - `HTTP_X_FORWARDED_FOR`

Or add to `%windir%\System32\inetsrv\config\applicationHost.config` under `<rewrite><allowedServerVariables>` (use IIS Manager when possible).

### 5.5 Disable response buffering for SSE (important)

For the live dashboard to update, ARR must stream `/api/events` without buffering.

1. IIS Manager → server node → **Application Request Routing Cache** → **Server Proxy Settings**
2. Set **Response buffer threshold** to **0** (zero)
3. Apply

If the dashboard appears frozen but jobs run, this setting is the most common fix.

### 5.6 Verify through IIS

```powershell
# From the server
Invoke-WebRequest -Uri https://megadbsync.contoso.local/api/bootstrap -UseBasicParsing

# From a client with DNS/hosts entry pointing to the server
# Open https://megadbsync.contoso.local in a browser
```

Sign in with the admin password created during first run.

---

## 6. Security checklist

| Item | Recommendation |
|------|----------------|
| MegaDBSync bind address | `127.0.0.1:8080` — not `0.0.0.0` in production |
| External access | Only via IIS on **443** |
| TLS | Valid certificate on IIS (internal CA or public) |
| Admin password | Strong password; change under **Settings → Admin password** |
| Service account | Least privilege; SQL permissions only on required databases |
| DPAPI | Connection passwords tied to the machine/user that encrypted them — use the **same service account** consistently |
| Firewall | Block inbound 8080; allow 443 to IIS |

---

## 7. Network and database connectivity

### Oracle (source)

- TCP **1521** (or your listener port) from the MegaDBSync server to Oracle
- Connection uses **service name** in the **Database** field (e.g. `ORCL`, `XEPDB1`)
- Oracle user needs `SELECT` on migrated schemas

Test from the MegaDBSync server:

```powershell
Test-NetConnection -ComputerName oracle-host.contoso.local -Port 1521
```

### SQL Server (destination)

- TCP **1433** (or named instance port)
- **SQL authentication**: username/password in MegaDBSync Connections tab
- **Windows integrated auth**: enable checkbox; service account must be a SQL login

Test:

```powershell
Test-NetConnection -ComputerName sql-host.contoso.local -Port 1433
```

MegaDBSync uses `encrypt=true` and `TrustServerCertificate=true` for SQL Server connections by default (typical on-prem setups). Adjust under **Settings → Database connectivity** if your SQL Server requires different TLS settings.

| Setting | Default | When to change |
|---------|---------|----------------|
| Connect timeout | 30 sec | Slow networks or distant DB hosts |
| SQL Server encrypt | on | Turn off only for legacy lab SQL instances without TLS |
| Trust server certificate | on | Turn off when using a proper CA-signed cert and strict validation |

### Common Windows gotchas

| Issue | Mitigation |
|-------|------------|
| Downloaded exe blocked | `Unblock-File C:\MegaDBSync\megadbsync.exe` |
| DPAPI passwords unreadable after service account change | Re-enter connection passwords in the UI under the same account that runs the service |
| Oracle **Database** field | Use **service name** (e.g. `XEPDB1`), not SID |
| SQL integrated auth fails | Service account must be a Windows login in SQL Server |
| Connection hangs then fails | Increase connect timeout in Settings; verify firewall with `Test-NetConnection` |
| Dashboard frozen behind IIS | ARR response buffer threshold = 0 (see section 5.5) |

---

## 8. Operations

### Logs

| Source | Location |
|--------|----------|
| NSSM stdout/stderr | `C:\ProgramData\MegaDBSync\megadbsync.log`, `megadbsync-error.log` |
| IIS | Event Viewer → Windows Logs → Application |
| MegaDBSync activity | Web UI → **Dashboard** → Activity log |

### Backup

Back up the entire state folder while MegaDBSync is stopped (or copy `megadbsync.db` during a quiet period):

```powershell
Stop-Service MegaDBSync
Copy-Item -Recurse C:\ProgramData\MegaDBSync C:\Backups\MegaDBSync-$(Get-Date -Format yyyyMMdd)
Start-Service MegaDBSync
```

Includes: admin settings, connection profiles, sync watermarks, job history.

### Upgrade procedure

1. Stop the service: `Stop-Service MegaDBSync` or `nssm stop MegaDBSync`
2. Replace `C:\MegaDBSync\megadbsync.exe` with the new build
3. `Unblock-File C:\MegaDBSync\megadbsync.exe`
4. Start the service
5. `%ProgramData%\MegaDBSync\megadbsync.db` is preserved — no reconfiguration required

### Reset admin password

Stop MegaDBSync, then with [DB Browser for SQLite](https://sqlitebrowser.org/) or `sqlite3`:

```sql
UPDATE settings SET admin_password_hash = '' WHERE id = 1;
```

Restart and open the UI — you will get the first-run password setup again.

---

## 9. Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| Service starts then stops | Wrong path, blocked exe, or port in use | Check `megadbsync-error.log`; run `megadbsync.exe` interactively |
| Cannot connect to Oracle/SQL | Firewall, wrong port, credentials | `Test-NetConnection`; test connections in UI |
| Integrated SQL auth fails | Service account not a SQL login | Add Windows login in SSMS; grant DB access |
| UI loads but dashboard frozen | IIS buffering SSE | ARR response buffer threshold = 0 |
| 502.3 Bad Gateway | MegaDBSync not running on 8080 | `Get-Service MegaDBSync`; curl `http://127.0.0.1:8080/api/bootstrap` |
| DPAPI decrypt errors after account change | Passwords encrypted under different user | Re-enter connection passwords in UI |
| Bulk job blocked | Destination not empty | Use empty SQL Server DB or new database |
| Download artifact missing | Build failed or artifact expired (>30 days) | Re-run workflow on `main` or build locally |

### Confirm MegaDBSync is listening

```powershell
netstat -ano | findstr :8080
Invoke-WebRequest http://127.0.0.1:8080/api/bootstrap -UseBasicParsing
```

### Confirm IIS proxy

```powershell
Invoke-WebRequest https://megadbsync.contoso.local/api/bootstrap -UseBasicParsing
```

---

## 10. Quick reference

```powershell
# Download & install layout
C:\MegaDBSync\megadbsync.exe
C:\ProgramData\MegaDBSync\megadbsync.db

# Manual run
C:\MegaDBSync\megadbsync.exe -addr 127.0.0.1:8080 -data C:\ProgramData\MegaDBSync

# NSSM service
nssm install MegaDBSync C:\MegaDBSync\megadbsync.exe
nssm set MegaDBSync AppParameters "-addr 127.0.0.1:8080 -data C:\ProgramData\MegaDBSync"
nssm start MegaDBSync

# Local health check
Invoke-WebRequest http://127.0.0.1:8080/api/bootstrap -UseBasicParsing
```

For application behaviour (bulk vs incremental sync, watermarks, performance tuning), see the main [README.md](../README.md).
