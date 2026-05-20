# Examples

A collection of ready-to-use `docker-compose.yml` configurations for common deployment scenarios — covering every storage backend (local, S3, GCS, Azure) and the main authentication and permission patterns.

Every example uses the published image `ghcr.io/vaggeliskls/cloud-webdav-server:latest` and exposes the server on `http://localhost:8080`. Drop any block into a `docker-compose.yml` and run `docker compose up --build`.

---

## 1. Public read-only server (no auth)

Expose a single folder publicly with read-only access. No credentials required.

```yaml
# docker-compose.yml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    ports:
      - "8080:8080"
    volumes:
      - ./webdav-data:/data
    environment:
      STORAGE_TYPE: local
      LOCAL_DATA_PATH: /data
      FOLDER_PERMISSIONS: "/files:public:ro"
      AUTO_CREATE_FOLDERS: "true"
```

Anyone can `GET http://localhost:8080/files/`. `PUT`, `DELETE`, and `MKCOL` return `403`.

---

## 2. Basic auth — single private folder

All authenticated users share read-write access to `/files`. Passwords are matched via bcrypt against the `BASIC_USERS` list.

```yaml
# docker-compose.yml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    ports:
      - "8080:8080"
    volumes:
      - ./webdav-data:/data
    environment:
      STORAGE_TYPE: local
      LOCAL_DATA_PATH: /data
      FOLDER_PERMISSIONS: "/files:*:rw"
      AUTO_CREATE_FOLDERS: "true"
      BASIC_AUTH_ENABLED: "true"
      BASIC_USERS: "alice:alice123 bob:bob123"
```

```sh
# List
curl -u alice:alice123 -X PROPFIND http://localhost:8080/files/

# Upload
curl -u alice:alice123 -T myfile.txt http://localhost:8080/files/myfile.txt

# Create a directory
curl -u alice:alice123 -X MKCOL http://localhost:8080/files/newfolder/
```

---

## 3. Mixed public + private folders

A public read-only area alongside a private read-write folder restricted to a single user.

```yaml
# docker-compose.yml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    ports:
      - "8080:8080"
    volumes:
      - ./webdav-data:/data
    environment:
      STORAGE_TYPE: local
      LOCAL_DATA_PATH: /data
      FOLDER_PERMISSIONS: "/public:public:ro,/private:alice:rw"
      AUTO_CREATE_FOLDERS: "true"
      BASIC_AUTH_ENABLED: "true"
      BASIC_USERS: "alice:alice123"
```

- `GET /public/` → accessible without credentials
- `GET /private/` → requires `alice:alice123`
- `PUT /private/file.txt` → allowed for alice
- `PUT /public/file.txt` → blocked (read-only)

---

## 4. Per-user folder isolation

Each user owns a private folder; a shared area is read-only for everyone authenticated.

```yaml
# docker-compose.yml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    ports:
      - "8080:8080"
    volumes:
      - ./webdav-data:/data
    environment:
      STORAGE_TYPE: local
      LOCAL_DATA_PATH: /data
      FOLDER_PERMISSIONS: "/shared:*:ro,/alice:alice:rw,/bob:bob:rw"
      AUTO_CREATE_FOLDERS: "true"
      BASIC_AUTH_ENABLED: "true"
      BASIC_USERS: "alice:alice123 bob:bob123"
```

- `/shared` — read-only for any authenticated user
- `/alice` — read-write for `alice` only
- `/bob` — read-write for `bob` only

---

## 5. Exclude specific users from a folder

Allow all authenticated users except an explicit deny-list.

```yaml
# docker-compose.yml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    ports:
      - "8080:8080"
    volumes:
      - ./webdav-data:/data
    environment:
      STORAGE_TYPE: local
      LOCAL_DATA_PATH: /data
      FOLDER_PERMISSIONS: "/shared:* !charlie:ro,/private:alice bob:rw"
      AUTO_CREATE_FOLDERS: "true"
      BASIC_AUTH_ENABLED: "true"
      BASIC_USERS: "alice:alice123 bob:bob123 charlie:charlie123"
```

- `GET /shared/` with `alice` or `bob` → allowed
- `GET /shared/` with `charlie` → `403 Forbidden`
- Multiple exclusions: `"* !charlie !dave"`

---

## 6. Amazon S3 backend

Back the WebDAV server with an S3 bucket. Works with any AWS region.

```yaml
# docker-compose.yml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    ports:
      - "8080:8080"
    environment:
      STORAGE_TYPE: s3
      S3_BUCKET: my-webdav-bucket
      S3_REGION: us-east-1
      S3_PREFIX: webdav/                 # optional key prefix
      AWS_ACCESS_KEY_ID: ${AWS_ACCESS_KEY_ID}
      AWS_SECRET_ACCESS_KEY: ${AWS_SECRET_ACCESS_KEY}
      FOLDER_PERMISSIONS: "/files:*:rw"
      BASIC_AUTH_ENABLED: "true"
      BASIC_USERS: "alice:alice123"
```

Keep `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` in a sibling `.env` file (or your secret manager). See [Amazon S3 / MinIO](cloud-s3.md) for bucket creation and IAM policies.

---

## 7. S3-compatible service (MinIO, R2, B2, Wasabi)

The same backend works with any S3-compatible service by setting `S3_ENDPOINT`. Example with self-hosted MinIO:

```yaml
# docker-compose.yml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    ports:
      - "8080:8080"
    environment:
      STORAGE_TYPE: s3
      S3_BUCKET: webdav
      S3_REGION: us-east-1
      S3_ENDPOINT: http://minio:9000
      AWS_ACCESS_KEY_ID: minioadmin
      AWS_SECRET_ACCESS_KEY: minioadmin
      FOLDER_PERMISSIONS: "/files:*:rw"
      BASIC_AUTH_ENABLED: "true"
      BASIC_USERS: "alice:alice123"
    depends_on:
      - minio

  minio:
    image: minio/minio:latest
    command: server /data --console-address ":9001"
    environment:
      MINIO_ROOT_USER: minioadmin
      MINIO_ROOT_PASSWORD: minioadmin
    ports:
      - "9001:9001"
    volumes:
      - minio-data:/data

volumes:
  minio-data:
```

MinIO console: `http://localhost:9001`. Endpoint URLs for Cloudflare R2, Backblaze B2, Wasabi, and DigitalOcean Spaces are listed in [Amazon S3 / MinIO](cloud-s3.md).

---

## 8. Google Cloud Storage

GCS uses a service-account JSON key mounted into the container.

```yaml
# docker-compose.yml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    ports:
      - "8080:8080"
    environment:
      STORAGE_TYPE: gcs
      GCS_BUCKET: my-webdav-bucket
      GCS_PREFIX: webdav/                                # optional
      GOOGLE_APPLICATION_CREDENTIALS: /secrets/sa.json
      FOLDER_PERMISSIONS: "/files:*:rw"
      BASIC_AUTH_ENABLED: "true"
      BASIC_USERS: "alice:alice123"
    volumes:
      - ./gcs-sa.json:/secrets/sa.json:ro
```

On GKE with **Workload Identity** enabled, omit the volume mount and leave `GOOGLE_APPLICATION_CREDENTIALS` unset — the SDK falls back to Application Default Credentials. See [Google Cloud Storage](cloud-gcs.md) for the full setup.

---

## 9. Azure Blob Storage

Connect to an Azure storage account using Shared Key authentication.

```yaml
# docker-compose.yml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    ports:
      - "8080:8080"
    environment:
      STORAGE_TYPE: azure
      AZURE_CONTAINER: webdav
      AZURE_PREFIX: webdav/                  # optional
      AZURE_STORAGE_ACCOUNT: mywebdavacct
      AZURE_STORAGE_KEY: ${AZURE_STORAGE_KEY}
      FOLDER_PERMISSIONS: "/files:*:rw"
      BASIC_AUTH_ENABLED: "true"
      BASIC_USERS: "alice:alice123"
```

For sovereign clouds (Azure Government, China, Azure Stack), set `AZURE_STORAGE_ENDPOINT` — see [Azure Blob Storage](cloud-azure.md).

---

## 10. LDAP / Active Directory authentication

Authenticate users against an LDAP or AD server. Any authenticated user gets read-write access to `/files`.

```yaml
# docker-compose.yml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    ports:
      - "8080:8080"
    volumes:
      - ./webdav-data:/data
    environment:
      STORAGE_TYPE: local
      LOCAL_DATA_PATH: /data
      FOLDER_PERMISSIONS: "/files:*:rw"
      AUTO_CREATE_FOLDERS: "true"
      LDAP_ENABLED: "true"
      LDAP_URL: "ldaps://ldap.company.com"
      LDAP_BASE_DN: "ou=users,dc=company,dc=com"
      LDAP_BIND_DN: "uid=searchuser,ou=users,dc=company,dc=com"
      LDAP_BIND_PASSWORD: ${LDAP_BIND_PASSWORD}
      LDAP_ATTRIBUTE: "uid"           # sAMAccountName for Active Directory
```

---

## 11. OpenID Connect (Bearer token)

Validate Bearer tokens issued by an OIDC provider (Keycloak, Auth0, Okta, Entra ID).

```yaml
# docker-compose.yml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    ports:
      - "8080:8080"
    volumes:
      - ./webdav-data:/data
    environment:
      STORAGE_TYPE: local
      LOCAL_DATA_PATH: /data
      FOLDER_PERMISSIONS: "/files:*:rw"
      AUTO_CREATE_FOLDERS: "true"
      OAUTH_ENABLED: "true"
      OIDC_PROVIDER_URL: "https://keycloak.example.com/realms/myrealm"
      OIDC_CLIENT_ID: "webdav-client"
      OIDC_CLIENT_SECRET: ${OIDC_CLIENT_SECRET}
      OIDC_USERNAME_CLAIM: "preferred_username"
```

Clients send `Authorization: Bearer <token>`. The server verifies the signature against the provider's JWKS and matches `OIDC_USERNAME_CLAIM` against the `FOLDER_PERMISSIONS` users.

---

## 12. Behind a Traefik reverse proxy

Expose the server through Traefik with TLS termination. The `webdav` container is not directly port-exposed.

```yaml
# docker-compose.yml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    volumes:
      - ./webdav-data:/data
    networks:
      - proxy
    environment:
      STORAGE_TYPE: local
      LOCAL_DATA_PATH: /data
      FOLDER_PERMISSIONS: "/files:*:rw"
      AUTO_CREATE_FOLDERS: "true"
      BASIC_AUTH_ENABLED: "true"
      BASIC_USERS: "alice:alice123"
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.webdav.rule=Host(`files.example.com`)"
      - "traefik.http.routers.webdav.entrypoints=websecure"
      - "traefik.http.routers.webdav.tls.certresolver=le"
      - "traefik.http.services.webdav.loadbalancer.server.port=8080"

  traefik:
    image: traefik:v3.6
    command:
      - "--providers.docker=true"
      - "--providers.docker.exposedbydefault=false"
      - "--entrypoints.web.address=:80"
      - "--entrypoints.websecure.address=:443"
      - "--certificatesresolvers.le.acme.email=admin@example.com"
      - "--certificatesresolvers.le.acme.storage=/letsencrypt/acme.json"
      - "--certificatesresolvers.le.acme.httpchallenge.entrypoint=web"
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - ./letsencrypt:/letsencrypt
    networks:
      - proxy

networks:
  proxy:
    driver: bridge
```

---

## 13. CORS + healthcheck

Enable CORS for browser-based WebDAV clients and wire up the built-in healthcheck for orchestrators.

```yaml
# docker-compose.yml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    ports:
      - "8080:8080"
    volumes:
      - ./webdav-data:/data
    environment:
      STORAGE_TYPE: local
      LOCAL_DATA_PATH: /data
      FOLDER_PERMISSIONS: "/files:*:rw"
      AUTO_CREATE_FOLDERS: "true"
      BASIC_AUTH_ENABLED: "true"
      BASIC_USERS: "alice:alice123"
      CORS_ENABLED: "true"
      CORS_ORIGIN: "https://myapp.example.com"
    healthcheck:
      test: ["CMD", "/webdav-server", "--healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 5s
    restart: unless-stopped
```

The image bundles a `--healthcheck` subcommand that hits `/_health` and exits 0/1 — no `curl` needed inside the distroless container.

---

## Using an `.env` file

Any example above can pull credentials and other values from a sibling `.env` file via `env_file`:

```dotenv
# .env
STORAGE_TYPE=s3
S3_BUCKET=my-webdav-bucket
S3_REGION=us-east-1
AWS_ACCESS_KEY_ID=AKIA...
AWS_SECRET_ACCESS_KEY=...
BASIC_AUTH_ENABLED=true
BASIC_USERS=alice:alice123 bob:bob123
FOLDER_PERMISSIONS=/files:*:rw
```

```yaml
# docker-compose.yml
services:
  webdav:
    image: ghcr.io/vaggeliskls/cloud-webdav-server:latest
    ports:
      - "8080:8080"
    env_file:
      - .env
```

Keep production `.env` files out of source control — the repo's tracked `.env` is for local quick-start only.
