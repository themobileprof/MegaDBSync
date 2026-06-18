# IIS reverse proxy (HTTPS)

Expose the MegaDBSync UI on **HTTPS :443** while the app stays on **http://127.0.0.1:8080** (localhost only).

**Prerequisite:** MegaDBSync installed and running as a service — see [Windows deployment guide](windows-deployment.md) for download, NSSM, firewall, and database connectivity.

```
[Browser] --HTTPS:443--> [IIS] --HTTP--> [megadbsync.exe @ 127.0.0.1:8080]
```

---

## 1. Install (once per server)

| Component | Link |
|-----------|------|
| IIS | Windows feature **Web Server (IIS)** |
| [URL Rewrite 2](https://www.iis.net/downloads/microsoft/url-rewrite) | Required |
| [ARR 3](https://www.iis.net/downloads/microsoft/application-request-routing) | Required |

Enable the ARR proxy:

1. IIS Manager → **server node** (top level)
2. **Application Request Routing Cache** → **Server Proxy Settings**
3. Check **Enable proxy** → **Apply**

Or PowerShell:

```powershell
Import-Module WebAdministration
Set-WebConfigurationProperty -pspath 'MACHINE/WEBROOT/APPHOST' -filter "system.webServer/proxy" -name "enabled" -value "True"
```

---

## 2. Create the proxy site

```powershell
New-Item -ItemType Directory -Force -Path C:\inetpub\megadbsync-proxy
Copy-Item .\docs\iis-web.config.example C:\inetpub\megadbsync-proxy\web.config
```

Create an HTTPS site in IIS Manager (or `New-Website`) pointing at `C:\inetpub\megadbsync-proxy` and bind your TLS certificate.

Allow rewrite server variables (first time only):

1. IIS Manager → server node → **URL Rewrite** → **View Server Variables**
2. Add: `HTTP_X_FORWARDED_PROTO`, `HTTP_X_FORWARDED_FOR`

---

## 3. SSE / live dashboard (required)

MegaDBSync streams dashboard updates via **Server-Sent Events** (`/api/events`). ARR must not buffer the response.

**Application Request Routing Cache** → **Server Proxy Settings** → set **Response buffer threshold** to **0** → Apply.

If the UI loads but the dashboard never updates while jobs run, this is almost always the cause.

---

## 4. Verify

```powershell
# MegaDBSync direct (on the server)
Invoke-WebRequest http://127.0.0.1:8080/api/bootstrap -UseBasicParsing

# Through IIS
Invoke-WebRequest https://your-hostname/api/bootstrap -UseBasicParsing
```

Open `https://your-hostname` in a browser and sign in.

---

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| **502.3 Bad Gateway** | MegaDBSync not running on 8080 — check the Windows service |
| **Dashboard frozen** | ARR **Response buffer threshold = 0** |
| **500 / rewrite error** | Allow server variables (step 2) |
| **Certificate warning** | Bind a valid cert on the IIS site (lab: self-signed) |

`web.config` template: [`iis-web.config.example`](iis-web.config.example)
