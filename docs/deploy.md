# Deploy portlight

portlight is designed to run on small servers. The recommended deployment is a standalone binary behind Caddy.

## DNS

Point both the base host and wildcard host to your server:

```text
preview.example.com
*.preview.example.com
```

## Binary + systemd

Install the binary:

```bash
sudo install -d -m 0755 /opt/portlight
sudo install -m 0755 portlight /opt/portlight/portlight
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
ExecStart=/opt/portlight/portlight server --listen 127.0.0.1:8789 --public-base https://preview.example.com
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

## Caddy

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

## Client Usage

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

Request a name:

```bash
portlight expose --server https://preview.example.com --port 3000 --name myapp
```

## Optional Docker Compose

Binary + systemd uses less memory and is recommended for small servers. Docker Compose is still possible:

```yaml
services:
  portlight:
    image: portlight:latest
    restart: unless-stopped
    environment:
      PORTLIGHT_TOKEN: change-me
    command:
      - server
      - --listen
      - 0.0.0.0:8789
      - --public-base
      - https://preview.example.com
    ports:
      - "127.0.0.1:8789:8789"
```

Keep Caddy on the host and reverse proxy to `127.0.0.1:8789`.

## Build Release Binaries

Build release binaries with Go 1.26.4 or newer. Earlier Go 1.26 patch
releases include standard library vulnerabilities that affect this code path.

From a Windows development machine:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build-release.ps1 -Version 0.1.0
```

The script builds:

- `linux-amd64`
- `linux-arm64`
- `darwin-amd64`
- `darwin-arm64`
- `windows-amd64`
- `windows-arm64`

Outputs are written under `dist/<os>-<arch>/`.
