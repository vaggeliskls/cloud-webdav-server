# Docker

The published image is `ghcr.io/vaggeliskls/cloud-webdav-server:latest` — distroless `scratch`, non-root, ~10 MB. The default container reads configuration from environment variables.

## Quick start (docker compose)

The repository ships a [docker-compose.yml](https://github.com/vaggeliskls/cloud-webdav-server/blob/main/docker-compose.yml) that runs the published image with a local-filesystem backend on `./webdav-data`.

```sh
cp .env.example .env
docker compose up -d
```

The server is on `http://localhost:8080`. Stop with `docker compose down`.

## Production compose examples

Per-backend `docker-compose.yml` snippets meant for real deployments — not the local emulator stacks shipped at the repo root. Keep credentials in an `.env` file alongside the compose, and never commit that `.env` to source control.

Common `.env`:

```dotenv
# Auth — rotate by editing and re-running `docker compose up -d`
BASIC_USERS=alice:alice123 bob:bob456

# Permissions
FOLDER_PERMISSIONS=/files:*:rw
```

### Local filesystem

```yaml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    ports:
      - "8080:8080"
    environment:
      STORAGE_TYPE: local
      LOCAL_DATA_PATH: /data
      BASIC_AUTH_ENABLED: "true"
      BASIC_USERS: ${BASIC_USERS}
      FOLDER_PERMISSIONS: ${FOLDER_PERMISSIONS}
      AUTO_CREATE_FOLDERS: "true"
    volumes:
      - webdav-data:/data
    healthcheck:
      test: ["CMD", "/webdav-server", "--healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 5s
    restart: unless-stopped

volumes:
  webdav-data:
```

### Amazon S3

Add to `.env`:

```dotenv
S3_BUCKET=my-webdav-bucket
S3_REGION=us-east-1
AWS_ACCESS_KEY_ID=AKIA...
AWS_SECRET_ACCESS_KEY=...
```

```yaml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    ports:
      - "8080:8080"
    environment:
      STORAGE_TYPE: s3
      S3_BUCKET: ${S3_BUCKET}
      S3_REGION: ${S3_REGION}
      S3_PREFIX: ${S3_PREFIX:-}
      S3_ENDPOINT: ${S3_ENDPOINT:-}
      AWS_ACCESS_KEY_ID: ${AWS_ACCESS_KEY_ID}
      AWS_SECRET_ACCESS_KEY: ${AWS_SECRET_ACCESS_KEY}
      BASIC_AUTH_ENABLED: "true"
      BASIC_USERS: ${BASIC_USERS}
      FOLDER_PERMISSIONS: ${FOLDER_PERMISSIONS}
    healthcheck:
      test: ["CMD", "/webdav-server", "--healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 5s
    restart: unless-stopped
```

The same compose works for any S3-compatible service (R2, B2, Wasabi, DO Spaces, MinIO) — set `S3_ENDPOINT` in `.env`.

### Google Cloud Storage

Service-account JSON is mounted read-only into the container; the container reads it via `GOOGLE_APPLICATION_CREDENTIALS`.

Add to `.env`:

```dotenv
GCS_BUCKET=my-webdav-bucket
GCS_SA_KEY_PATH=./gcs-sa.json   # path on the host, relative to compose file
```

```yaml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    ports:
      - "8080:8080"
    environment:
      STORAGE_TYPE: gcs
      GCS_BUCKET: ${GCS_BUCKET}
      GCS_PREFIX: ${GCS_PREFIX:-}
      GOOGLE_APPLICATION_CREDENTIALS: /secrets/gcs/sa.json
      BASIC_AUTH_ENABLED: "true"
      BASIC_USERS: ${BASIC_USERS}
      FOLDER_PERMISSIONS: ${FOLDER_PERMISSIONS}
    volumes:
      - ${GCS_SA_KEY_PATH}:/secrets/gcs/sa.json:ro
    healthcheck:
      test: ["CMD", "/webdav-server", "--healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 5s
    restart: unless-stopped
```

### Azure Blob Storage

Add to `.env`:

```dotenv
AZURE_CONTAINER=webdav
AZURE_STORAGE_ACCOUNT=mystorageacct
AZURE_STORAGE_KEY=...
```

```yaml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    ports:
      - "8080:8080"
    environment:
      STORAGE_TYPE: azure
      AZURE_CONTAINER: ${AZURE_CONTAINER}
      AZURE_PREFIX: ${AZURE_PREFIX:-}
      AZURE_STORAGE_ACCOUNT: ${AZURE_STORAGE_ACCOUNT}
      AZURE_STORAGE_KEY: ${AZURE_STORAGE_KEY}
      AZURE_STORAGE_ENDPOINT: ${AZURE_STORAGE_ENDPOINT:-}
      BASIC_AUTH_ENABLED: "true"
      BASIC_USERS: ${BASIC_USERS}
      FOLDER_PERMISSIONS: ${FOLDER_PERMISSIONS}
    healthcheck:
      test: ["CMD", "/webdav-server", "--healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 5s
    restart: unless-stopped
```

Set `AZURE_STORAGE_ENDPOINT` only for sovereign clouds (Azure Government, Azure China) or Azure Stack — leave empty for public Azure.

## Plain docker run

Local filesystem:

```sh
docker run -d --name webdav \
  -p 8080:8080 \
  -e STORAGE_TYPE=local \
  -e LOCAL_DATA_PATH=/data \
  -e BASIC_AUTH_ENABLED=true \
  -e BASIC_USERS="alice:alice123" \
  -e FOLDER_PERMISSIONS="/files:*:rw" \
  -v $(pwd)/webdav-data:/data \
  ghcr.io/vaggeliskls/cloud-webdav-server:latest
```

S3 backend:

```sh
docker run -d --name webdav \
  -p 8080:8080 \
  -e STORAGE_TYPE=s3 \
  -e S3_BUCKET=my-webdav-bucket \
  -e S3_REGION=us-east-1 \
  -e AWS_ACCESS_KEY_ID=... \
  -e AWS_SECRET_ACCESS_KEY=... \
  -e BASIC_USERS="alice:alice123" \
  ghcr.io/vaggeliskls/cloud-webdav-server:latest
```

For GCS and Azure, see the per-cloud pages — they cover credential mounting in detail.

## Healthcheck

The image bundles a `--healthcheck` subcommand that hits `/_health` and exits 0/1. Use it in a `HEALTHCHECK` directive or compose stanza:

```yaml
healthcheck:
  test: ["CMD", "/webdav-server", "--healthcheck"]
  interval: 30s
  timeout: 5s
  retries: 3
  start_period: 5s
```

## Building from source

```sh
docker build -t cloud-webdav-server:dev .
```

The Dockerfile is a multi-stage build: a Go builder layer, then a `scratch`-based final image with the static binary, CA bundle, and a non-root user.

## Image tags

| Tag      | Meaning                                  |
|----------|------------------------------------------|
| `X.Y.Z`  | Tagged release (e.g. `2.1.0`)            |
| `latest` | Most recent release                      |

Images are published only when a GitHub release is created. Pin a version tag in production — never `latest`. The Helm chart does this automatically: each chart version deploys the image of the same version.

## Logs

The server emits structured JSON to stdout. Set `LOG_LEVEL=debug` for verbose output (default `info`). Pipe through `jq` or your log aggregator of choice — every request is logged with method, path, status, duration, and authenticated user.

```json
{"time":"2026-05-01T18:46:54Z","level":"INFO","msg":"request","method":"PUT","path":"/files/README.md","status":201,"duration":"95ms","user":"alice"}
```
