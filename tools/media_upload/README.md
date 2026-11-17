# media_upload

OAuth-protected MCP server that hands agents and CLI tools a size-capped S3
presigned PUT URL plus a public URL, so demo media is embedded in PR
descriptions instead of committed to git. Product brief lives in krill
(`media_upload`); infrastructure (bucket, deployment) is owned by argok8s.

## Flow

1. Call MCP tool `request_upload {filename, size_bytes, content_type?, temporary?}`.
2. `PUT <upload_url>` with exactly the returned `headers` and a body of `size_bytes` bytes.
3. Embed `public_url`.

Tools: `request_upload`, `list_allowed_types`. Keys are
`<permanent|tmp>/<YYYY>/<MM>/<random id>/<name>`; the random id avoids CDN and
GitHub image-cache collisions. Objects under `tmp/` are expected to expire via a
bucket lifecycle rule (see argok8s). Objects get
`Cache-Control: public, max-age=31536000, immutable`.

## Layout

- `mcp/` — the server (`//tools/media_upload/mcp`), release app `media-upload-mcp`.
- `uploads/` — validation, allowlist, key + URL generation (unit tested).
- `BUILD.bazel` — `release_helm_chart` `media-upload` (`helm-media-upload-media-upload`).

Environment variables: [ENV.md](ENV.md).

## Verify

```
bazel test //tools/media_upload/...
bazel build //tools/media_upload:media_upload_chart
```
