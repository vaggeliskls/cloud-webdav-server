# Configuration Reference

All configuration is via environment variables. The Helm chart and `docker-compose.yml` translate their values into these vars.

## Server

| Variable      | Default     | Description                              |
|---------------|-------------|------------------------------------------|
| `SERVER_NAME` | `localhost` | Cosmetic — used in some response headers |
| `SERVER_PORT` | `8080`      | TCP port the HTTP server binds to        |
| `LOG_LEVEL`   | `info`      | `info` or `debug`                        |

## Storage

| Variable          | Default          | Description                                 |
|-------------------|------------------|---------------------------------------------|
| `STORAGE_TYPE`    | `local`          | `local` · `s3` · `gcs` · `azure`            |
| `LOCAL_DATA_PATH` | `./webdav-data`  | Root dir when `STORAGE_TYPE=local`          |

### S3 (`STORAGE_TYPE=s3`)

| Variable                | Default     | Description                                       |
|-------------------------|-------------|---------------------------------------------------|
| `S3_BUCKET`             | (required)  | Bucket name                                       |
| `S3_REGION`             | `us-east-1` | AWS region (or `auto` for R2)                     |
| `S3_PREFIX`             | (empty)     | Optional key prefix inside the bucket             |
| `S3_ENDPOINT`           | (empty)     | Custom endpoint URL for S3-compatible services    |
| `AWS_ACCESS_KEY_ID`     | (empty)     | Access key (omit on EKS with IRSA)                |
| `AWS_SECRET_ACCESS_KEY` | (empty)     | Secret key (omit on EKS with IRSA)                |

### GCS (`STORAGE_TYPE=gcs`)

| Variable                          | Default    | Description                                              |
|-----------------------------------|------------|----------------------------------------------------------|
| `GCS_BUCKET`                      | (required) | Bucket name                                              |
| `GCS_PREFIX`                      | (empty)    | Optional object name prefix                              |
| `GOOGLE_APPLICATION_CREDENTIALS`  | (empty)    | Path to service-account JSON; empty = ADC fallback       |
| `STORAGE_EMULATOR_HOST`           | (empty)    | Set to point the SDK at fake-gcs-server                  |

### Azure (`STORAGE_TYPE=azure`)

| Variable                  | Default    | Description                                                                |
|---------------------------|------------|----------------------------------------------------------------------------|
| `AZURE_CONTAINER`         | (required) | Container name (the "bucket" equivalent)                                   |
| `AZURE_PREFIX`            | (empty)    | Optional blob name prefix                                                  |
| `AZURE_STORAGE_ACCOUNT`   | (empty)    | Storage account name (required unless using a connection string)           |
| `AZURE_STORAGE_KEY`       | (empty)    | Account access key (required unless using a connection string)             |
| `AZURE_STORAGE_ENDPOINT`  | (empty)    | Override the service URL — sovereign clouds, Azure Stack                   |
| `AZURE_STORAGE_CONNECTION_STRING` | (empty) | Full connection string; takes precedence over account + key (mostly dev)|

## Folder permissions

| Variable               | Default        | Description                              |
|------------------------|----------------|------------------------------------------|
| `FOLDER_PERMISSIONS`   | `/files:*:rw`  | Comma-separated permission rules         |
| `AUTO_CREATE_FOLDERS`  | `true`         | Create configured folders at startup     |

### Format

`/path:users:mode[,/path:users:mode,...]`

| Field   | Values                                                                                |
|---------|---------------------------------------------------------------------------------------|
| `path`  | URL prefix, e.g. `/files`, `/team/docs`                                               |
| `users` | `public` (no auth) · `*` (any auth) · `alice bob` (specific) · `* !charlie` (exclude) |
| `mode`  | `ro` (read-only) · `rw` (read-write)                                                  |

**Longest prefix wins** — `/private/secret` takes precedence over `/private`.

### Examples

```ini
# Public read-only + per-user folders
FOLDER_PERMISSIONS=/public:public:ro,/alice:alice:rw,/bob:bob:rw

# Shared folder for everyone except charlie
FOLDER_PERMISSIONS=/shared:* !charlie:rw

# Mixed: public read, authenticated read-write, admin-only delete dir
FOLDER_PERMISSIONS=/public:public:ro,/files:*:rw,/admin:alice:rw
```

## HTTP methods

| Variable     | Default                                                                  |
|--------------|--------------------------------------------------------------------------|
| `RO_METHODS` | `GET HEAD OPTIONS PROPFIND`                                              |
| `RW_METHODS` | `GET HEAD OPTIONS PROPFIND PUT DELETE MKCOL COPY MOVE LOCK UNLOCK PROPPATCH` |

These define what `ro` and `rw` modes allow. Override only if you have very specific compliance needs — the defaults match the WebDAV RFC.

> **Locking is in-memory.** `LOCK`/`UNLOCK` are handled via `webdav.NewMemLS()` — locks don't persist across restarts and aren't shared across replicas. For HA deployments where WebDAV locking matters, stick to a single instance.

## Authentication

### Basic auth

| Variable             | Default | Description                                  |
|----------------------|---------|----------------------------------------------|
| `BASIC_AUTH_ENABLED` | `true`  | Toggle Basic auth                            |
| `BASIC_USERS`        | (empty) | Space-separated `user:pass` pairs            |

Passwords are bcrypt-hashed at startup; the comparison is constant-time.

### LDAP / Active Directory

| Variable             | Default | Description                                          |
|----------------------|---------|------------------------------------------------------|
| `LDAP_ENABLED`       | `false` | Toggle LDAP auth                                     |
| `LDAP_URL`           | (empty) | `ldap://` or `ldaps://` URL                          |
| `LDAP_BASE_DN`       | (empty) | Base DN for user searches                            |
| `LDAP_BIND_DN`       | (empty) | Service-account DN used for the search bind         |
| `LDAP_BIND_PASSWORD` | (empty) | Service-account password                             |
| `LDAP_ATTRIBUTE`     | `uid`   | Username attribute (`sAMAccountName` for AD)         |
| `LDAP_STARTTLS`      | `false` | Upgrade plain LDAP to TLS via STARTTLS               |

### OpenID Connect (Bearer)

| Variable               | Default                  | Description                                  |
|------------------------|--------------------------|----------------------------------------------|
| `OAUTH_ENABLED`        | `false`                  | Toggle OIDC auth                             |
| `OIDC_PROVIDER_URL`    | (empty)                  | Issuer URL (must match `iss` claim)          |
| `OIDC_CLIENT_ID`       | (empty)                  | Client ID for audience validation            |
| `OIDC_USERNAME_CLAIM`  | `preferred_username`     | Claim used as the WebDAV user identity       |
| `OIDC_SCOPES`          | `openid email profile`   | Scopes (space-separated)                     |

The server validates the JWT signature against the provider's JWKS, verifies `iss`/`aud`/`exp`, and uses the configured username claim to match against `FOLDER_PERMISSIONS`.

## CORS

| Variable               | Default                                                                      |
|------------------------|------------------------------------------------------------------------------|
| `CORS_ENABLED`         | `false`                                                                      |
| `CORS_ORIGIN`          | `*`                                                                          |
| `CORS_ALLOWED_METHODS` | `GET,HEAD,PUT,DELETE,MKCOL,COPY,MOVE,OPTIONS,PROPFIND,PROPPATCH,LOCK,UNLOCK` |
| `CORS_ALLOWED_HEADERS` | `Authorization,Content-Type,Depth,If,Lock-Token,Overwrite,Timeout,Destination,X-Requested-With` |

Enable CORS only if you have a browser-based WebDAV client. Don't leave `CORS_ORIGIN=*` if you use credentials — most browsers reject that combo.

## Misc

| Variable                  | Default | Description                                                |
|---------------------------|---------|------------------------------------------------------------|
| `BROWSER_ACCESS_BLOCKED`  | `false` | Return 403 when User-Agent matches `Mozilla/*`             |

Useful when you want to prevent accidental web-browser access to private shares.
