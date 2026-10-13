# Amazon S3 / S3-compatible

The S3 backend uses [`github.com/aws/aws-sdk-go-v2`](https://github.com/aws/aws-sdk-go-v2). It works with **AWS S3** out of the box, and with any **S3-compatible** service (MinIO, Cloudflare R2, Backblaze B2, Wasabi, DigitalOcean Spaces, Ceph, SeaweedFS) by setting `S3_ENDPOINT`.

## AWS S3

### 1. Create the bucket

```sh
aws s3api create-bucket \
  --bucket my-webdav-bucket \
  --region us-east-1
```

Pick a region close to your server. Cross-region traffic is slow and incurs egress costs.

### 2. Create an IAM user with restricted permissions

Don't use root credentials. Create a dedicated IAM user with this least-privilege policy:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "s3:ListBucket",
        "s3:GetBucketLocation"
      ],
      "Resource": "arn:aws:s3:::my-webdav-bucket"
    },
    {
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:PutObject",
        "s3:DeleteObject"
      ],
      "Resource": "arn:aws:s3:::my-webdav-bucket/*"
    }
  ]
}
```

Generate access keys for that user and store them somewhere safe (Secrets Manager, Vault, sealed secrets, etc).

### 3. Configure the server

```env
STORAGE_TYPE=s3
S3_BUCKET=my-webdav-bucket
S3_REGION=us-east-1
S3_PREFIX=webdav/                 # optional — namespace blobs under a prefix
AWS_ACCESS_KEY_ID=AKIA...
AWS_SECRET_ACCESS_KEY=...
```

In Kubernetes:

```yaml
storage:
  type: s3
  s3:
    bucket: "my-webdav-bucket"
    region: "us-east-1"
    prefix: "webdav/"
    accessKeyId: "AKIA..."        # in Secret
    secretAccessKey: "..."        # in Secret
```

### 4. EKS pod identity / IRSA (preferred for production on AWS)

For pods running on EKS, prefer **IAM Roles for Service Accounts (IRSA)** or **EKS Pod Identity** over baked-in keys: the SDK picks up the role's temporary credentials from instance metadata automatically. Leave `accessKeyId` / `secretAccessKey` empty in `values.yaml` and annotate the chart's ServiceAccount with the role ARN.

> The chart doesn't yet generate a ServiceAccount — open an issue or contribute a PR if you need it for an IRSA setup.

## S3-compatible services

These all speak the S3 API and work via `S3_ENDPOINT`. Use **path-style** addressing — that's the SDK default in this server when an endpoint is set.

### Cloudflare R2

```env
STORAGE_TYPE=s3
S3_BUCKET=my-bucket
S3_REGION=auto
S3_ENDPOINT=https://<account-id>.r2.cloudflarestorage.com
AWS_ACCESS_KEY_ID=<R2 access key>
AWS_SECRET_ACCESS_KEY=<R2 secret>
```

R2 has zero egress fees — good fit for serving large files.

### Backblaze B2 (S3 API)

```env
S3_ENDPOINT=https://s3.<region>.backblazeb2.com
S3_REGION=us-west-002
```

### Wasabi

```env
S3_ENDPOINT=https://s3.<region>.wasabisys.com
S3_REGION=us-east-1
```

### DigitalOcean Spaces

```env
S3_ENDPOINT=https://<region>.digitaloceanspaces.com
S3_REGION=us-east-1
```

### MinIO (self-hosted)

```env
S3_ENDPOINT=http://minio:9000
S3_REGION=us-east-1
AWS_ACCESS_KEY_ID=minioadmin
AWS_SECRET_ACCESS_KEY=minioadmin
```

For local dev with MinIO via the bundled compose file, see [Local Development](docs/local-development).

## Notes on behavior

- **No native rename**: WebDAV `MOVE` is implemented as `CopyObject` + `DeleteObject`. Large folder moves multiply by the number of objects.
- **Directory markers**: empty objects with names ending in `/` represent directories. Standard pattern, supported by all real S3-compatibles.
- **Streaming uploads**: PUTs are streamed to S3 through the SDK transfer manager, which switches to multipart upload for large files — no local buffering.
- **Implicit folders**: uploading into a folder that doesn't exist creates it implicitly (see [Configuration → Storage](docs/configuration?id=storage)).
