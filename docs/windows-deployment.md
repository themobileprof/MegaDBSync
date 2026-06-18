# Windows deployment guide

Complete instructions for downloading MDAS, running it as a Windows service, and exposing the web UI through IIS with HTTPS.

---

## Overview

```
[Browser] --HTTPS:443--> [IIS reverse proxy] --HTTP--> [mdas.exe on 127.0.0.1:8080]
                                                              |
                              +-------------------------------+
                              |
                              v
                    [Oracle DB]     [SQL Server]
```

MDAS is a **single executable** (`mdas.exe`). It embeds the web UI and runs migration jobs in-process. Persistent state lives in **`%ProgramData%\MDAS`** (SQLite database, DPAPI-protected credentials).

| Component | Role |
|-----------|------|
| `mdas.exe` | HTTP server, job engine, scheduler |
| `%ProgramData%\MDAS\` | Config, connections, sync watermarks, job history |
| Windows Service (or NSSM) | Keeps MDAS running after logoff/reboot |
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
| Run MDAS | `mdas.exe` only (no Go runtime, no Oracle Instant Client) |
| Windows Service wrapper | [NSSM](https://nssm.cc/download) (recommended) or built-in `sc.exe` |
| HTTPS reverse proxy | IIS + **URL Rewrite** + **Application Request Routing (ARR)** |

### Firewall (typical production layout)

| Port | Exposure | Notes |
|------|----------|-------|
| **443** | External (via IIS) | HTTPS to the admin UI |
| **8080** | **Localhost only** | MDAS bind address `127.0.0.1:8080` — not reachable from the network |
| 1521 / 1433 | Outbound from server | To Oracle / SQL Server |

### Service account permissions

The account running MDAS needs:

- **Read/write** to `%ProgramData%\MDAS` (or your `-data` path)
- **Network** access to Oracle and SQL Server
- If using **Windows integrated SQL auth**: the service account must be a valid SQL Server login (Windows user/group mapped in SQL Server)
- If using **SQL login**: credentials are stored in MDAS (DPAPI-encrypted)

Use a dedicated domain account (e.g. `DOMAIN\svc-mdas`) rather than LocalSystem when using integrated SQL auth or Kerberos-heavy environments.

---

## 2. Download the binary

### Option A — GitHub Actions artifact (recommended)

Every push to `main` triggers a build on `windows-latest`. The artifact is retained **30 days**.

1. Open the repository on GitHub: `https://github.com/themobileprof/mdas`
2. Click **Actions**
3. Select the latest **Build** workflow run with a green checkmark
4. Scroll to **Artifacts**
5. Download **`mdas-windows-amd64`**
6. Extract `mdas.exe` from the zip

Verify the file is unblocked (Windows may mark downloaded files):

```powershell
Unblock-File -Path C:\MDAS\mdas.exe
```

Check version/build by running once in a console (Ctrl+C to stop):

```powershell
C:\MDAS\mdas.exe
# Expect: "MDAS — Migration & Daily Sync (Windows)"
#         "MDAS listening on http://127.0.0.1:8080"
```

### Option B — Build from source

Requires **Go 1.22+** on a Windows machine:

```powershell
git clone https://github.com/themobileprof/mdas.git
cd mdas
go mod tidy
go build -trimpath -ldflags="-s -w" -o mdas.exe .\cmd\mdas
```

---

## 3. Install on the server

### Recommended folder layout

```powershell
New-Item -ItemType Directory -Force -Path C:\MDAS
Copy-Item .\mdas.exe C:\MDAS\mdas.exe
```

| Path | Contents |
|------|----------|
| `C:\MDAS\mdas.exe` | Application binary |
| `C:\ProgramData\MDAS\` | Created automatically on first run — **do not delete** |

State directory resolution order:

1. `-data` flag on command line
2. `%MDAS_DATA%` environment variable
3. `%ProgramData%\MDAS`
4. `%LOCALAPPDATA%\MDAS`
5. `data\` next to the executable

### Command-line flags

| Flag | Default | Description |
|------|---------|-------------|
| `-addr` | `127.0.0.1:8080` | HTTP listen address. Use `127.0.0.1` when IIS terminates TLS in front. |
| `-data` | `%ProgramData%\MDAS` | SQLite and DPAPI state directory |

### First run (interactive smoke test)

Before installing as a service, run manually:

```powershell
cd C:\MDAS
.\mdas.exe -addr 127.0.0.1:8080
```

1. Open **http://127.0.0.1:8080**
2. Create the **admin password** (minimum 8 characters)
3. Add Oracle source and SQL Server destination connections — use **Test**
4. Stop with Ctrl+C when satisfied

---

## 4. Run as a Windows Service

MDAS is a standard console application. It does **not** implement the Windows Service Control Manager protocol internally. For production, use **NSSM** (recommended) so the process restarts on failure and stops cleanly.

### Option A — NSSM (recommended)

1. Download NSSM from [nssm.cc](https://nssm.cc/download) and extract `nssm.exe` (64-bit) to `C:\MDAS\`

2. Install the service:

```powershell
cd C:\MDAS
.\nssm.exe install MDAS "C:\MDAS\mdas.exe"
.\nssm.exe set MDAS AppDirectory "C:\MDAS"
.\nssm.exe set MDAS AppParameters "-addr 127.0.0.1:8080 -data C:\ProgramData\MDAS"
.\nssm.exe set MDAS DisplayName "MDAS Database Sync"
.\nssm.exe set MDAS Description "Oracle to SQL Server migration and incremental sync"
.\nssm.exe set MDAS Start SERVICE_AUTO_START
.\nssm.exe set MDAS AppStdout "C:\ProgramData\MDAS\mdas.log"
.\nssm.exe set MDAS AppStderr "C:\ProgramData\MDAS\mdas-error.log"
.\nssm.exe set MDAS AppRotateFiles 1
.\nssm.exe set MDAS AppRotateBytes 10485760
```

3. Set the service account (important for integrated SQL auth):

```powershell
# GUI — easiest for domain account + password
.\nssm.exe edit MDAS
# Log on tab → This account → DOMAIN\svc-mdas

# Or via services.msc: Win+R → services.msc → MDAS → Properties → Log On
```

4. Grant the account modify rights on the data folder:

```powershell
icacls "C:\ProgramData\MDAS" /grant "DOMAIN\svc-mdas:(OI)(CI)M" /T
```

5. Start the service:

```powershell
.\nssm.exe start MDAS
# Or:
Start-Service MDAS
```

6. Verify:

```powershell
Get-Service MDAS
Invoke-WebRequest -Uri http://127.0.0.1:8080/api/bootstrap -UseBasicParsing
```

**Manage NSSM service:**

```powershell
.\nssm.exe stop MDAS
.\nssm.exe restart MDAS
.\nssm.exe remove MDAS confirm
```

### Option B — sc.exe (minimal)

Works for simple setups; less graceful shutdown and no automatic restart on crash.

```powershell
sc.exe create MDAS binPath= "C:\MDAS\mdas.exe -addr 127.0.0.1:8080 -data C:\ProgramData\MDAS" start= auto DisplayName= "MDAS Database Sync"
sc.exe description MDAS "Oracle to SQL Server migration and incremental sync"
sc.exe config MDAS obj= "DOMAIN\svc-mdas" password= "YourPassword"
sc.exe start MDAS
```

> **Note:** `binPath=` must be followed by a **space** before the opening quote. Configure the service account under `services.msc` if `sc config` password handling is awkward.

---

## 5. IIS reverse proxy (HTTPS)

Expose the UI on port **443** while MDAS stays on localhost **8080**.

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

Example: host name `mdas.contoso.local`, site root `C:\inetpub\mdas-proxy` (only holds `web.config`).

```powershell
New-Item -ItemType Directory -Force -Path C:\inetpub\mdas-proxy
New-Website -Name "MDAS" -Port 443 -PhysicalPath "C:\inetpub\mdas-proxy" -HostHeader "mdas.contoso.local" -Ssl
```

Bind an HTTPS certificate:

- **IIS Manager** → site **MDAS** → **Bindings** → HTTPS → select your certificate
- Or use `New-SelfSignedCertificate` for lab/testing only

### 5.4 web.config — reverse proxy + SSE support

MDAS uses **Server-Sent Events** (`/api/events`) for the live dashboard. IIS must not buffer the proxied response.

Create `C:\inetpub\mdas-proxy\web.config`:

Copy from [`docs/iis-web.config.example`](iis-web.config.example) in this repository, or use:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<configuration>
  <system.webServer>
    <webSocket enabled="false" />
    <proxy enabled="true" preserveHostHeader="false" reverseRewriteHostInResponseHeaders="false" />

    <rewrite>
      <rules>
        <rule name="MDAS reverse proxy" stopProcessing="true">
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
Invoke-WebRequest -Uri https://mdas.contoso.local/api/bootstrap -UseBasicParsing

# From a client with DNS/hosts entry pointing to the server
# Open https://mdas.contoso.local in a browser
```

Sign in with the admin password created during first run.

---

## 6. Security checklist

| Item | Recommendation |
|------|----------------|
| MDAS bind address | `127.0.0.1:8080` — not `0.0.0.0` in production |
| External access | Only via IIS on **443** |
| TLS | Valid certificate on IIS (internal CA or public) |
| Admin password | Strong password; change under **Settings → Admin password** |
| Service account | Least privilege; SQL permissions only on required databases |
| DPAPI | Connection passwords tied to the machine/user that encrypted them — use the **same service account** consistently |
| Firewall | Block inbound 8080; allow 443 to IIS |

---

## 7. Network and database connectivity

### Oracle (source)

- TCP **1521** (or your listener port) from the MDAS server to Oracle
- Connection uses **service name** in the **Database** field (e.g. `ORCL`, `XEPDB1`)
- Oracle user needs `SELECT` on migrated schemas

Test from the MDAS server:

```powershell
Test-NetConnection -ComputerName oracle-host.contoso.local -Port 1521
```

### SQL Server (destination)

- TCP **1433** (or named instance port)
- **SQL authentication**: username/password in MDAS Connections tab
- **Windows integrated auth**: enable checkbox; service account must be a SQL login

Test:

```powershell
Test-NetConnection -ComputerName sql-host.contoso.local -Port 1433
```

MDAS uses `encrypt=true` and `TrustServerCertificate=true` for SQL Server connections (typical on-prem setups).

---

## 8. Operations

### Logs

| Source | Location |
|--------|----------|
| NSSM stdout/stderr | `C:\ProgramData\MDAS\mdas.log`, `mdas-error.log` |
| IIS | Event Viewer → Windows Logs → Application |
| MDAS activity | Web UI → **Dashboard** → Activity log |

### Backup

Back up the entire state folder while MDAS is stopped (or copy `mdas.db` during a quiet period):

```powershell
Stop-Service MDAS
Copy-Item -Recurse C:\ProgramData\MDAS C:\Backups\MDAS-$(Get-Date -Format yyyyMMdd)
Start-Service MDAS
```

Includes: admin settings, connection profiles, sync watermarks, job history.

### Upgrade procedure

1. Stop the service: `Stop-Service MDAS` or `nssm stop MDAS`
2. Replace `C:\MDAS\mdas.exe` with the new build
3. `Unblock-File C:\MDAS\mdas.exe`
4. Start the service
5. `%ProgramData%\MDAS\mdas.db` is preserved — no reconfiguration required

### Reset admin password

Stop MDAS, then with [DB Browser for SQLite](https://sqlitebrowser.org/) or `sqlite3`:

```sql
UPDATE settings SET admin_password_hash = '' WHERE id = 1;
```

Restart and open the UI — you will get the first-run password setup again.

---

## 9. Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| Service starts then stops | Wrong path, blocked exe, or port in use | Check `mdas-error.log`; run `mdas.exe` interactively |
| Cannot connect to Oracle/SQL | Firewall, wrong port, credentials | `Test-NetConnection`; test connections in UI |
| Integrated SQL auth fails | Service account not a SQL login | Add Windows login in SSMS; grant DB access |
| UI loads but dashboard frozen | IIS buffering SSE | ARR response buffer threshold = 0 |
| 502.3 Bad Gateway | MDAS not running on 8080 | `Get-Service MDAS`; curl `http://127.0.0.1:8080/api/bootstrap` |
| DPAPI decrypt errors after account change | Passwords encrypted under different user | Re-enter connection passwords in UI |
| Bulk job blocked | Destination not empty | Use empty SQL Server DB or new database |
| Download artifact missing | Build failed or artifact expired (>30 days) | Re-run workflow on `main` or build locally |

### Confirm MDAS is listening

```powershell
netstat -ano | findstr :8080
Invoke-WebRequest http://127.0.0.1:8080/api/bootstrap -UseBasicParsing
```

### Confirm IIS proxy

```powershell
Invoke-WebRequest https://mdas.contoso.local/api/bootstrap -UseBasicParsing
```

---

## 10. Quick reference

```powershell
# Download & install layout
C:\MDAS\mdas.exe
C:\ProgramData\MDAS\mdas.db

# Manual run
C:\MDAS\mdas.exe -addr 127.0.0.1:8080 -data C:\ProgramData\MDAS

# NSSM service
nssm install MDAS C:\MDAS\mdas.exe
nssm set MDAS AppParameters "-addr 127.0.0.1:8080 -data C:\ProgramData\MDAS"
nssm start MDAS

# Local health check
Invoke-WebRequest http://127.0.0.1:8080/api/bootstrap -UseBasicParsing
```

For application behaviour (bulk vs incremental sync, watermarks, performance tuning), see the main [README.md](../README.md).
