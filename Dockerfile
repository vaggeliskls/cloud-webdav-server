FROM golang:1.27-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /webdav-server ./cmd/server

# Pre-create the data dir so fresh named volumes inherit non-root ownership.
RUN mkdir /data

# ---- final image ----
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /webdav-server /webdav-server
COPY --from=builder --chown=1000:1000 /data /data

# Non-root user (numeric UID required by scratch)
USER 1000:1000

EXPOSE 8080

# Uses the binary's built-in probe; scratch has no shell or curl.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD ["/webdav-server", "--healthcheck"]

ENTRYPOINT ["/webdav-server"]
