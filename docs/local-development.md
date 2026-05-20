# Local Development

Run the server on your machine, optionally against a local cloud emulator (MinIO for S3, Azurite for Azure, fake-gcs-server for GCS).

## Prerequisites

- **Go 1.26+**
- **[Task](https://taskfile.dev)** (`brew install go-task` or see Taskfile install docs)
- **Docker** (only if you want to test against a cloud emulator)

## Run with the local filesystem

The fastest way to start: storage on disk, basic auth, two test users.

```sh
task setup   # creates .env from .env.example (no-op if .env exists)
task run
```

The server listens on `http://localhost:8080`. Try it:

```sh
# Upload
curl -T README.md http://localhost:8080/files/README.md -u alice:alice123

# List (PROPFIND)
curl -X PROPFIND http://localhost:8080/files/ -u alice:alice123

# Download
curl http://localhost:8080/files/README.md -u alice:alice123
```

### Quick uploads via `task upload`

A cross-platform helper task wraps the `curl -T` flow — works the same on Linux, macOS, and Windows (Taskfile uses an embedded shell interpreter and `curl` ships with Windows 10+). Required variable: `FILE`.

```sh
# Default user (alice:alice123) → /files


# Different user
task upload FILE=test.zip USER=bob:bob123

# Different folder or host
task upload FILE=foo.txt URL=http://localhost:8080/private
task upload FILE=foo.txt URL=https://files.example.com/files USER=alice:alice123
```

The task derives the destination filename from the source path automatically (`base` template function — no `basename` binary needed on Windows). It echoes the HTTP status code so you can spot 401/403/4xx/5xx without scrolling through verbose curl output.

## Tests, lint, coverage

```sh
task test       # all tests with -race
task lint       # golangci-lint
task quality    # vet + lint + tests + coverage (mirrors CI)
```

## Test against a cloud emulator

You can run the same server code against a local emulator instead of a real cloud account — useful for offline iteration and CI integration tests.

### S3 (MinIO)

```sh
task minio-up       # docker compose: MinIO + bucket creation
task run-minio      # server in S3 mode pointed at MinIO
# MinIO console: http://localhost:9001 (minioadmin / minioadmin)
task minio-down     # tear down (keeps volume)
task minio-reset    # tear down + wipe volume
```

### Azure (Azurite)

```sh
task azurite-up     # docker compose: Azurite + container creation
task run-azure      # server in Azure mode pointed at Azurite
task azurite-down
task azurite-reset
```

### GCS (fake-gcs-server)

```sh
task gcs-up         # docker compose: fake-gcs-server + bucket creation
task run-gcs        # server in GCS mode pointed at fake-gcs
task gcs-down
```

The `run-*` tasks set `STORAGE_TYPE` and the relevant credentials inline, so you don't need to touch `.env`.

## Available tasks

```sh
task                # list every task with descriptions
```

## Project layout

```
cmd/server/         # entry point (main.go)
internal/
  auth/             # basic, LDAP, OIDC authenticators
  config/           # env-var loading + validation
  permissions/      # /path:users:mode rule engine
  server/           # HTTP/WebDAV handler + middleware
  storage/          # local, s3, gcs, azure backends
kubernetes/         # Helm chart
docker-compose.*.yml  # local emulator stacks
```
