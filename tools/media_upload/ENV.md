# media_upload — Environment Variables

| Variable | Default | Description |
|---|---|---|
| `OIDC_ISSUER` | required | Keycloak realm issuer; also advertised as the OAuth authorization server. |
| `OIDC_CLIENT_ID` | required | Expected token audience/client. |
| `MCP_PUBLIC_URL` | required | This server's external URL (protected-resource metadata `resource`). |
| `MEDIA_REQUIRED_ROLE` | unset | If set, callers must carry this realm role. |
| `S3_BUCKET` | required | Media bucket. |
| `S3_REGION` | `us-east-1` | |
| `S3_ENDPOINT` | unset | Custom S3 endpoint (OVH, MinIO). |
| `S3_PUBLIC_ENDPOINT` | required | Endpoint embedded in presigned URLs; must be reachable by uploaders. |
| `S3_PUBLIC_PATH_STYLE` | `false` | `true` for MinIO (path-style routing). |
| `S3_ACCESS_KEY` / `S3_SECRET_KEY` | unset | Static credentials; needs PutObject on the bucket. |
| `MEDIA_PUBLIC_BASE_URL` | required | Public base URL objects are served from (bucket public URL or CDN). |
| `MEDIA_MAX_MB` | `50` | Max upload size in MB, pinned into the presigned Content-Length. |
| `MEDIA_TEMP_PREFIX` | `tmp` | Key prefix for `temporary` uploads (lifecycle-expired). |
| `MEDIA_PERMANENT_PREFIX` | `permanent` | Key prefix for permanent uploads. |
| `PORT` | `8081` | Listen port. |
