# portlight

portlight exposes a local HTTP service through a public HTTPS URL that you control.

Run a small server behind a wildcard domain, then run the CLI on your laptop or dev box:

```bash
portlight expose --port 3000
```

The CLI prints a URL such as:

```text
https://k9m2x4qa.preview.example.com
```

Opening that URL from anywhere proxies to:

```text
http://127.0.0.1:3000
```

## Why

portlight is meant for development previews, demos, webhook testing, and agent-driven local web app work. It is intentionally small:

- one Go binary;
- no database;
- no account system;
- tunnel lifetime is the CLI process lifetime;
- HTTPS and wildcard certificates are handled by your reverse proxy.

## Self-host DNS

If you run your own portlight server, configure both the base host and the
wildcard host before starting the server:

```text
preview.example.com
*.preview.example.com
```

For a single server, point both records at the same machine:

```text
preview.example.com     A      203.0.113.10
*.preview.example.com   A      203.0.113.10
```

If your DNS provider supports it, the wildcard can also be a CNAME to the base
host:

```text
preview.example.com     A      203.0.113.10
*.preview.example.com   CNAME  preview.example.com
```

The base host serves health checks and control endpoints. Each exposed tunnel
uses a subdomain under the wildcard, such as:

```text
https://k9m2x4qa.preview.example.com
https://myapp.preview.example.com
```

Terminate HTTPS for both the base host and wildcard host at your reverse proxy,
then proxy requests to the portlight server. See [docs/deploy.md](docs/deploy.md)
for a Docker Compose and Caddy example.

## Quick Start

Start the public server:

```bash
export PORTLIGHT_TOKEN="$(openssl rand -base64 32)"
portlight-server \
  --listen 127.0.0.1:8789 \
  --public-base https://portlight.616.pub
```

Expose a local port:

```bash
export PORTLIGHT_TOKEN='<the same long random token>'
portlight expose --port 3000
```

You can also pass the token directly:

```bash
portlight expose --token '<the same long random token>' --port 3000
```

Close the tunnel by stopping the command. To close it automatically, set a TTL:

```bash
portlight expose --port 3000 --ttl 30m
```

Machine-readable output:

```bash
portlight expose --port 3000 --json
```

```json
{"status":"ready","name":"k9m2x4qa","url":"https://k9m2x4qa.preview.example.com","target":"http://127.0.0.1:3000"}
```

## Agent handoff prompt

Copy this to a remote coding agent when it needs to open or test a local service:

```text
Use portlight when you need a public HTTPS URL for a local HTTP service.

If portlight is already installed, run `portlight skill` for the full agent
guide.

If portlight is missing, install it:
macOS/Linux: curl -fsSL https://portlight.616.pub/install.sh | sh
Windows PowerShell: irm https://portlight.616.pub/install.ps1 | iex

Use PORTLIGHT_TOKEN if it is already set. If the user gives you a token, pass it
with --token <token> or set PORTLIGHT_TOKEN for this shell.

Start the local service first, then run:
portlight expose --port <port> --ttl 30m --json

Read the JSON ready event and use the url field. Keep the command running while
the URL is needed. Stop the process, or wait for TTL, to close the URL. Do not
print the token in logs.
```

## Named URLs

Request a specific subdomain:

```bash
portlight expose --port 3000 --name myapp
```

This yields:

```text
https://myapp.preview.example.com
```

Names must use lowercase letters, numbers, and dashes, with length 3-48. If a name is already active, portlight rejects the new tunnel.

JSON conflict output:

```json
{"status":"error","error":"name_in_use","name":"myapp"}
```

When the original CLI process exits or disconnects, the name is released.

## Commands

```bash
portlight expose --help
portlight update
portlight skill
portlight --version
portlight-server --help
```

`expose` and `update` default to `https://portlight.616.pub`. Use `--server`
only when you run a separate portlight deployment.

`portlight` is the client CLI. Self-hosted deployments use the separate
`portlight-server` binary or Docker image.

`portlight skill` prints concise agent and CI guidance for exposing a local
service, reading the JSON ready URL, setting a TTL, and cleaning up the tunnel.

Important environment variable:

```text
PORTLIGHT_TOKEN
```

CLI connections to the server require this bearer token. Set it with an
environment variable, or pass `--token <token>` to `portlight-server` or
`portlight expose`.
Public tunnel URLs are anonymous by default; anyone with the URL can access the
exposed local HTTP service. Use a long random value in production and keep it
out of source control.

Linux/macOS:

```bash
export PORTLIGHT_TOKEN='<server token>'
portlight expose --port 3000
```

Windows PowerShell:

```powershell
$env:PORTLIGHT_TOKEN='<server token>'
portlight expose --port 3000
```

Command-line token:

```bash
portlight expose --token '<server token>' --port 3000
```

## Uninstall

Remove the installed CLI:

```bash
portlight uninstall
```

On Linux/macOS, use `sudo portlight uninstall` if the binary was installed to a
system directory such as `/usr/local/bin`.

On Windows, `portlight uninstall` schedules removal of `portlight.exe` and
removes the install directory from the user `PATH` when it matches the directory
used by the installer.

portlight does not create persistent logs, caches, or config directories during
normal CLI use.

## Release versions

Client releases start at `0.1.0`. Patch releases increment as `0.1.1`,
`0.1.2`, and so on for client CLI updates.

## Limits in v1

- HTTP only.
- Browser WebSocket/HMR is not supported yet.
- TCP forwarding is not supported.
- No persistent aliases; names exist only while the CLI is connected.
- No multi-user account system or dashboard.

## Deploy

See [docs/deploy.md](docs/deploy.md).

## Website

The static product website lives in [site/](site/). Open
[site/index.html](site/index.html) directly in a browser or serve the folder
from any static host.

## License

MIT
