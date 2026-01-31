# S3 Logging Configuration

## Overview

The ManManV2 API uploads session logs to S3-compatible storage instead of storing them locally. This provides scalable, durable storage for game server logs.

**Supported Storage Providers:**
- Amazon S3
- OVH Object Storage
- DigitalOcean Spaces
- MinIO
- Any S3-compatible storage

## Environment Variables

Configure the S3 client using these environment variables:

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `S3_BUCKET` | S3 bucket name for log storage | `manman-logs` | Yes |
| `S3_REGION` | Region/location for the bucket | `us-east-1` | Yes |
| `S3_ENDPOINT` | Custom S3 endpoint (for non-AWS providers) | `""` (AWS) | No |

## Credentials

The S3 client uses the AWS SDK v2's default credential chain, which checks:

1. Environment variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`)
2. Shared credentials file (`~/.aws/credentials`)
3. IAM role (when running on EC2/ECS/EKS - AWS only)

### Example: AWS S3

```bash
export AWS_ACCESS_KEY_ID=AKIAXXXXXXXXXXXXXXXX
export AWS_SECRET_ACCESS_KEY=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
export S3_BUCKET=my-manman-logs
export S3_REGION=us-west-2
```

### Example: OVH Object Storage

```bash
export AWS_ACCESS_KEY_ID=<your-ovh-access-key>
export AWS_SECRET_ACCESS_KEY=<your-ovh-secret-key>
export S3_BUCKET=manman-logs
export S3_REGION=gra  # OVH region: gra, rbx, sbg, bhs, etc.
export S3_ENDPOINT=https://s3.gra.io.cloud.ovh.net
```

### Example: DigitalOcean Spaces

```bash
export AWS_ACCESS_KEY_ID=<your-spaces-access-key>
export AWS_SECRET_ACCESS_KEY=<your-spaces-secret-key>
export S3_BUCKET=manman-logs
export S3_REGION=nyc3
export S3_ENDPOINT=https://nyc3.digitaloceanspaces.com
```

### Example: MinIO

```bash
export AWS_ACCESS_KEY_ID=minioadmin
export AWS_SECRET_ACCESS_KEY=minioadmin
export S3_BUCKET=manman-logs
export S3_REGION=us-east-1
export S3_ENDPOINT=http://minio.local:9000
```

### Example: IAM Role (Recommended for Production)

When running in Kubernetes, use IAM Roles for Service Accounts (IRSA):

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: manmanv2-api
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/ManManV2LogsRole
```

## Storage Setup

### AWS S3

```bash
aws s3 mb s3://manman-logs --region us-east-1
```

### OVH Object Storage

1. Create a bucket via OVH Control Panel or API
2. Generate S3 credentials (Access Key + Secret Key)
3. Use the appropriate regional endpoint:
   - Gravelines (GRA): `https://s3.gra.io.cloud.ovh.net`
   - Roubaix (RBX): `https://s3.rbx.io.cloud.ovh.net`
   - Strasbourg (SBG): `https://s3.sbg.io.cloud.ovh.net`
   - Beauharnois (BHS): `https://s3.bhs.io.cloud.ovh.net`

### DigitalOcean Spaces

```bash
doctl spaces create manman-logs --region nyc3
```

### MinIO

```bash
mc mb local/manman-logs
```

### IAM Policy

The API service needs these permissions:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "s3:PutObject",
        "s3:GetObject",
        "s3:DeleteObject"
      ],
      "Resource": "arn:aws:s3:::manman-logs/*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "s3:ListBucket"
      ],
      "Resource": "arn:aws:s3:::manman-logs"
    }
  ]
}
```

### Bucket Policy (Optional - for cross-account access)

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "AWS": "arn:aws:iam::123456789012:role/ManManV2LogsRole"
      },
      "Action": [
        "s3:PutObject",
        "s3:GetObject",
        "s3:DeleteObject"
      ],
      "Resource": "arn:aws:s3:::manman-logs/*"
    }
  ]
}
```

## Log File Structure

Logs are stored with the following S3 key format:

```
logs/{session_id}/{timestamp}-{batch_id}.log
```

### Example

```
s3://manman-logs/logs/12345/1738281600-abc123.log
s3://manman-logs/logs/12345/1738281660-def456.log
s3://manman-logs/logs/67890/1738282000-ghi789.log
```

## Database Schema

The `log_references` table stores S3 URLs in the `file_path` column:

```sql
SELECT * FROM log_references WHERE session_id = 12345;

log_id | session_id | file_path                                          | start_time | end_time | line_count | source | created_at
-------|------------|---------------------------------------------------|------------|----------|------------|--------|-------------------
1      | 12345      | s3://manman-logs/logs/12345/1738281600-abc123.log | 2026-01-30 | 2026-01-30 | 150      | stdout | 2026-01-30 12:00:00
```

## Migration

Run the migration to update schema comments:

```bash
bazel run //manman/migrate:manmanv2-migration -- up
```

This applies migration `003_s3_logs.up.sql` which updates the `file_path` column documentation.

## Troubleshooting

### "Access Denied" errors

- Verify IAM permissions are correct
- Check bucket policy allows your IAM role
- Ensure the bucket exists in the specified region

### "Bucket not found" errors

- Verify `S3_BUCKET` environment variable is set
- Check bucket exists: `aws s3 ls s3://manman-logs`
- Ensure `S3_REGION` matches bucket region

### Credential errors

- Verify AWS credentials are configured
- Check IAM role annotations (if using IRSA)
- Test credentials: `aws sts get-caller-identity`
