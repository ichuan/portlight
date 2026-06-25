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

## Quick Start

Start the public server:

```bash
export PORTLIGHT_TOKEN="$(openssl rand -base64 32)"
portlight server \
  --listen 127.0.0.1:8789 \
  --public-base https://portlight.616.pub
```

Expose a local port:

```bash
export PORTLIGHT_TOKEN='<the same long random token>'
portlight expose --port 3000
```

Machine-readable output:

```bash
portlight expose --port 3000 --json
```

```json
{"status":"ready","name":"k9m2x4qa","url":"https://k9m2x4qa.preview.example.com","target":"http://127.0.0.1:3000"}
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
portlight server --help
portlight expose --help
portlight update
portlight --version
```

`expose` and `update` default to `https://portlight.616.pub`. Use `--server`
only when you run a separate portlight deployment.

Important environment variable:

```text
PORTLIGHT_TOKEN
```

CLI connections to the server require this bearer token. Public tunnel URLs are anonymous by default; anyone with the URL can access the exposed local HTTP service.
Use a long random value in production and keep it out of source control.

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
