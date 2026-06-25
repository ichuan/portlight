# Deploy portlight

portlight is designed to run on small servers. This document covers the current
production deployment for `portlight.616.pub` and a generic self-hosting setup.

## Production: portlight.616.pub

This is the live deployment used by the public CLI defaults.

- SSH alias: `ind`
- Server IP: `160.191.28.85`
- Deploy directory: `/home/yc/portlight`
- Deployment method: Docker Compose for portlight, behind the host Caddy service
- Caddy config: `/etc/caddy/Caddyfile`
- Public base URL: `https://portlight.616.pub`
- DNS: `portlight.616.pub` and `*.portlight.616.pub` point to the same server

The deploy directory contains `.env`. This file is required on the server and
must not be committed or copied into logs:

```text
PORTLIGHT_VERSION=0.1.3
PORTLIGHT_TOKEN=<secret>
```

To verify that the token exists without printing it:

```bash
ssh ind "cd /home/yc/portlight && grep -q '^PORTLIGHT_TOKEN=' .env && printf 'PORTLIGHT_TOKEN is set\n'"
```

If an operator needs the token for a local smoke test, read it from
`/home/yc/portlight/.env` over SSH and keep it out of shared logs, shell history,
issues, and commits.

### Runtime Shape

Docker Compose runs two containers:

- `portlight-server-1` builds from this repo and listens on
  `127.0.0.1:8789`;
- `portlight-site-1` runs `caddy:2.10.0-alpine` as a static file server on
  `127.0.0.1:8790`.

The host Caddy service terminates HTTPS and routes requests:

```caddyfile
portlight.616.pub {
    encode zstd gzip

    @backend path /_control/* /healthz /readyz
    handle @backend {
        reverse_proxy 127.0.0.1:8789
    }

    handle {
        reverse_proxy 127.0.0.1:8790
    }
}

*.portlight.616.pub {
    encode zstd gzip
    reverse_proxy 127.0.0.1:8789
}
```

Do not replace the full host Caddyfile when updating portlight; the server also
hosts other sites.

### Verify Production

```bash
ssh ind "cd /home/yc/portlight && docker compose ps"
curl -fsS https://portlight.616.pub/healthz
curl -fsS https://portlight.616.pub/readyz
curl -fsS https://portlight.616.pub/releases/latest.json
```

Expected result:

- both Compose services are `Up`;
- `/healthz` returns `{"ok":true}`;
- `/readyz` returns `{"ready":true}`;
- `latest.json` reports the intended release version.

For an end-to-end smoke test, set `PORTLIGHT_TOKEN` locally, expose a local HTTP
service, open the printed public URL, then stop the CLI process and confirm the
URL no longer proxies.

### Update Production

Run the full local checks before deploying a new version:

```bash
go test ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
bash -n site/install.sh
```

PowerShell syntax check for the Windows installer:

```powershell
$null = [System.Management.Automation.PSParser]::Tokenize((Get-Content -Raw site/install.ps1), [ref]$null)
```

Build and publish release downloads into `site/downloads/` and
`site/releases/`:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build-release.ps1 -Version 0.1.3 -PublishSite
```

Upload the working tree to the server while preserving the server `.env`:

```bash
tar --exclude=.git --exclude=dist --exclude=tmp --exclude=.env --exclude=site/assets/prompts -cf - . \
  | ssh ind "mkdir -p /home/yc/portlight && tar -xf - -C /home/yc/portlight"
```

Update the server release version and rebuild:

```bash
ssh ind "cd /home/yc/portlight && sed -i 's/^PORTLIGHT_VERSION=.*/PORTLIGHT_VERSION=0.1.3/' .env"
ssh ind "cd /home/yc/portlight && docker compose up -d --build"
ssh ind "cd /home/yc/portlight && docker compose ps"
```

Only reload host Caddy when `/etc/caddy/Caddyfile` changes:

```bash
ssh ind "caddy validate --config /etc/caddy/Caddyfile && systemctl reload caddy"
```

After deploy, run the production verification commands above and one real tunnel
smoke test.

### Debug Production

Container state:

```bash
ssh ind "cd /home/yc/portlight && docker compose ps"
ssh ind "cd /home/yc/portlight && docker compose logs --tail=200 server"
ssh ind "cd /home/yc/portlight && docker compose logs --tail=200 site"
```

Follow server logs while reproducing a problem:

```bash
ssh ind "cd /home/yc/portlight && docker compose logs -f --tail=100 server"
```

Check local ports on the host:

```bash
ssh ind "ss -ltnp | grep -E ':(8789|8790) '"
```

Check Caddy:

```bash
ssh ind "caddy validate --config /etc/caddy/Caddyfile"
ssh ind "journalctl -u caddy -n 200 --no-pager"
```

Check backend and site from inside the host:

```bash
ssh ind "curl -fsS http://127.0.0.1:8789/healthz"
ssh ind "curl -fsS http://127.0.0.1:8790/releases/latest.json"
```

Common failure modes:

- `401` from `/_control/*`: client token does not match `PORTLIGHT_TOKEN`.
- Public base URL works but subdomains fail: wildcard DNS or wildcard Caddy
  routing is missing.
- Installer or `portlight update` sees the old version: `site/releases/` or
  `site/downloads/` was not regenerated or uploaded.
- Root site works but `/healthz` fails: host Caddy is routing backend paths to
  the static site instead of `127.0.0.1:8789`.
- Tunnel works locally but not from the internet: confirm the public URL uses a
  wildcard subdomain and that host Caddy proxies `*.portlight.616.pub` to the
  backend.

### Rotate The Production Token

Rotating the token disconnects existing clients and requires users to update
their local `PORTLIGHT_TOKEN` or `--token` value.

```bash
ssh ind "cd /home/yc/portlight && cp .env .env.bak"
ssh ind 'cd /home/yc/portlight && TOKEN=$(openssl rand -base64 32) && sed -i "s|^PORTLIGHT_TOKEN=.*|PORTLIGHT_TOKEN=${TOKEN}|" .env'
ssh ind "cd /home/yc/portlight && docker compose up -d --force-recreate server"
```

Do not paste the new token into public issues, logs, screenshots, or commits.

### Roll Back

This deployment builds the server image locally on the host rather than pulling
immutable image tags. To roll back, redeploy the previous checked-out source and
release files, set `PORTLIGHT_VERSION` in `.env` to the previous version, then
run:

```bash
ssh ind "cd /home/yc/portlight && docker compose up -d --build"
```

Verify with `/healthz`, `/readyz`, `latest.json`, and a real tunnel smoke test.

## Self-hosting

### DNS

Point both the base host and wildcard host to your server:

```text
preview.example.com
*.preview.example.com
```

### Docker Compose

Create a deployment directory on the server:

```bash
mkdir -p /home/yc/portlight
cd /home/yc/portlight
printf 'PORTLIGHT_VERSION=%s\n' '0.1.3' > .env
printf 'PORTLIGHT_TOKEN=%s\n' "$(openssl rand -base64 32)" >> .env
chmod 0600 .env
```

Copy the repository contents to that directory, build release downloads, and
start the containers:

```bash
docker compose up -d --build
```

The compose file exposes only localhost ports:

- `127.0.0.1:8789` for the tunnel backend;
- `127.0.0.1:8790` for the static website and release downloads.

Add these blocks to the existing host Caddyfile without replacing unrelated
sites:

```caddyfile
portlight.616.pub {
    encode zstd gzip

    @backend path /_control/* /healthz /readyz
    handle @backend {
        reverse_proxy 127.0.0.1:8789
    }

    handle {
        reverse_proxy 127.0.0.1:8790
    }
}

*.portlight.616.pub {
    encode zstd gzip
    reverse_proxy 127.0.0.1:8789
}
```

Check the deployment:

```bash
curl -fsS https://portlight.616.pub/healthz
curl -fsS https://portlight.616.pub/readyz
curl -fsS https://portlight.616.pub/releases/latest.json
```

### Binary + systemd

Install the server binary:

```bash
curl -fL -o portlight-server https://portlight.616.pub/downloads/portlight-server-linux-amd64
chmod +x portlight-server
sudo install -d -m 0755 /opt/portlight
sudo install -m 0755 portlight-server /opt/portlight/portlight-server
```

Create an environment file:

```bash
sudo install -d -m 0750 /etc/portlight
PORTLIGHT_TOKEN="$(openssl rand -base64 32)"
printf 'PORTLIGHT_TOKEN=%s\n' "$PORTLIGHT_TOKEN" | sudo tee /etc/portlight/portlight.env >/dev/null
sudo chmod 0640 /etc/portlight/portlight.env
```

Create `/etc/systemd/system/portlight.service`:

```ini
[Unit]
Description=portlight tunnel server
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/portlight/portlight.env
ExecStart=/opt/portlight/portlight-server --listen 127.0.0.1:8789 --public-base https://preview.example.com
Restart=always
RestartSec=3
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

Start it:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now portlight
```

### Caddy

Terminate HTTPS in Caddy and proxy both the base host and wildcard host to the local server:

```caddyfile
preview.example.com, *.preview.example.com {
    encode gzip
    reverse_proxy 127.0.0.1:8789
}
```

Check the server:

```bash
curl -fsS https://preview.example.com/healthz
curl -fsS https://preview.example.com/readyz
```

### Client Usage

Linux/macOS:

```bash
export PORTLIGHT_TOKEN='<the same long random token>'
./portlight expose --server https://preview.example.com --port 3000 --json
```

Windows PowerShell:

```powershell
$env:PORTLIGHT_TOKEN='<the same long random token>'
.\portlight.exe expose --server https://preview.example.com --port 3000 --json
```

You can also pass the token explicitly:

```bash
./portlight expose --server https://preview.example.com --token '<the same long random token>' --port 3000
```

Close a tunnel by stopping the CLI. To set an automatic lifetime:

```bash
./portlight expose --server https://preview.example.com --port 3000 --ttl 30m
```

Uninstall the CLI:

```bash
portlight uninstall
```

Request a name:

```bash
portlight expose --server https://preview.example.com --port 3000 --name myapp
```

### Build Release Binaries

Build release binaries with Go 1.26.4 or newer. Earlier Go 1.26 patch
releases include standard library vulnerabilities that affect this code path.

From a Windows development machine:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build-release.ps1 -Version 0.1.0
```

To publish downloads into the static website:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build-release.ps1 -Version 0.1.3 -PublishSite
```

Client release versions start at `0.1.0`; use `0.1.1`, `0.1.2`, and so on for
subsequent CLI updates.

The script builds client binaries for:

- `linux-amd64`
- `linux-arm64`
- `darwin-amd64`
- `darwin-arm64`
- `windows-amd64`
- `windows-arm64`

It also builds `portlight-server` for `linux-amd64` and `linux-arm64`.

Outputs are written under `dist/client-<os>-<arch>/` and
`dist/server-<os>-<arch>/`.
