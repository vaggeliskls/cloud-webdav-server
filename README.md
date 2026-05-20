# Cloud Webdav Server — WebDAV Server for Cloud Storage

[![Code Quality](https://github.com/vaggeliskls/cloud-webdav-server/actions/workflows/quality.yml/badge.svg)](https://github.com/vaggeliskls/cloud-webdav-server/actions/workflows/quality.yml)
[![Go Version](https://img.shields.io/badge/go-1.26%2B-00ADD8?logo=go)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/vaggeliskls/cloud-webdav-server)](https://goreportcard.com/report/github.com/vaggeliskls/cloud-webdav-server)

A lightweight, production-ready WebDAV server written in Go.
Mount **Amazon S3**, **Google Cloud Storage**, **Azure Blob Storage**, or a **local directory** as a WebDAV drive with per-folder access control and multiple authentication methods.

> **Inspired by** [vaggeliskls/webdav-server](https://github.com/vaggeliskls/webdav-server) — if you don't need cloud storage, check out that project for a simpler Docker-based WebDAV server with Basic, LDAP, and OAuth/OIDC support.

> ⭐ Find this useful? [Star the repo](https://github.com/vaggeliskls/cloud-webdav-server) — it helps others discover the project.

---

## Features

| Feature | Description |
|---|---|
| ☁️ **Storage backends** | Local filesystem, Amazon S3 / MinIO, Google Cloud Storage, Azure Blob Storage |
| 🔒 **Path-based permissions** | Per-folder access rules with user lists, wildcards, and exclusions |
| 🔑 **Authentication** | HTTP Basic, LDAP / Active Directory, OpenID Connect (Bearer token) |
| 📖 **Read-only mode** | Lock folders to `ro` per-folder or per-user |
| 📁 **Auto-create folders** | Directories created at startup from the permission config |
| 🌐 **CORS** | Configurable cross-origin support for web clients |
| 🩺 **Health check** | Optional `/_health` endpoint for load-balancer probes |
| 🚫 **Browser block** | Prevents accidental access from browsers (optional) |
| 🐳 **Minimal Docker image** | Distroless `scratch` image, non-root user, ~10 MB |

---

## Quick Start

```sh
cp .env.example .env   # edit as needed
docker compose up --build
```

The server listens on `http://localhost:8080`. Try it:

```sh
# 📤 Upload
curl -T README.md http://localhost:8080/files/README.md -u alice:alice123

# 📥 Download
curl http://localhost:8080/files/README.md -u alice:alice123 -O

# 📂 List directory (PROPFIND)
curl -X PROPFIND http://localhost:8080/files/ -u alice:alice123
```

---

## Documentation

Full docs are published at **<https://vaggeliskls.github.io/cloud-webdav-server>**.

**Getting started**
- [Local Development](docs/local-development.md) — `task` commands, running against MinIO / Azurite / fake-gcs emulators
- [Docker](docs/docker.md) — compose, plain `docker run`, image tags, healthchecks
- [Kubernetes / Helm](docs/kubernetes.md) — install from `oci://ghcr.io/vaggeliskls/charts/cloud-webdav-server`, ingress, probes

**Cloud providers**
- [Amazon S3 / MinIO](docs/cloud-s3.md) — AWS bucket + IAM, plus R2 / B2 / Wasabi / DO Spaces / MinIO endpoints
- [Google Cloud Storage](docs/cloud-gcs.md) — bucket setup, service accounts, Workload Identity on GKE
- [Azure Blob Storage](docs/cloud-azure.md) — storage accounts, containers, Shared Key auth, sovereign clouds

**Reference**
- [Configuration](docs/configuration.md) — full environment-variable reference for every backend and auth provider
- [Examples](docs/examples.md) — ready-to-use `docker-compose.yml` snippets for every storage backend and auth pattern

---

## Storage backend at a glance

| `STORAGE_TYPE` | Required env vars                                                              | Details                                       |
|----------------|---------------------------------------------------------------------------------|-----------------------------------------------|
| `local`        | `LOCAL_DATA_PATH`                                                               | [Local Development](docs/local-development.md)|
| `s3`           | `S3_BUCKET`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` (+ `S3_ENDPOINT` for non-AWS) | [Amazon S3 / MinIO](docs/cloud-s3.md)         |
| `gcs`          | `GCS_BUCKET`, `GOOGLE_APPLICATION_CREDENTIALS` (or ADC on GKE)                  | [Google Cloud Storage](docs/cloud-gcs.md)     |
| `azure`        | `AZURE_CONTAINER`, `AZURE_STORAGE_ACCOUNT`, `AZURE_STORAGE_KEY`                 | [Azure Blob Storage](docs/cloud-azure.md)     |

---

## References

- [vaggeliskls/webdav-server](https://github.com/vaggeliskls/webdav-server) — the original Apache httpd-based WebDAV server that inspired this project.
- [golang.org/x/net/webdav](https://pkg.go.dev/golang.org/x/net/webdav) — Go standard WebDAV handler
- [WebDAV RFC 4918](https://www.rfc-editor.org/rfc/rfc4918)
