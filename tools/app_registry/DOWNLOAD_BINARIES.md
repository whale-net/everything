# Downloading CLI binaries from the App Registry

Some CLIs are published as plain binaries instead of container images. The
registry hands out short-lived download links for them, and the files
themselves are fetched from public S3 with no further auth. CI uses this to get
`release_helper_go` and `app-registry` (see
[`.github/actions/download-release-tools`](../../.github/actions/download-release-tools/action.yml)).
This page walks through the same flow by hand, using the `media-upload` CLI as
the example.

Binaries the registry can resolve today: `release_helper_go`, `app-registry`,
`media-upload`. Anything else is rejected with `InvalidArgument`.

## What you need

- `grpcurl`, `curl`, `jq`
- The registry address (`APP_REGISTRY_ADDRESS`, as in [ENV.md](ENV.md)) and the
  Keycloak token URL. Resolving a URL needs any authenticated caller; no
  special role.
- A Keycloak client-credentials client (id + secret) for that caller.

```bash
export APP_REGISTRY_ADDRESS=<registry-host:443>
export TOKEN_URL=<keycloak token endpoint>
export CLIENT_ID=<client id>
export CLIENT_SECRET=<client secret>
```

## 1. Get a token

```bash
TOKEN=$(curl -sS -X POST "$TOKEN_URL" \
  -d grant_type=client_credentials -d client_id="$CLIENT_ID" -d client_secret="$CLIENT_SECRET" \
  | jq -r .access_token)
```

## 2. Pick a version

Use a version you know (from the release run or GitHub release), or ask which
one is promoted to an environment, the way CI does:

```bash
APP_ID=$(grpcurl -H "authorization: Bearer $TOKEN" -d '{"full_name":"tools-media-upload"}' \
  "$APP_REGISTRY_ADDRESS" appregistry.v1.AppRegistry/GetApp | jq -r '.app.appId')

VERSION=$(grpcurl -H "authorization: Bearer $TOKEN" -d '{"environment_key":"prod"}' \
  "$APP_REGISTRY_ADDRESS" appregistry.v1.PromotionRegistry/GetEnvironmentState \
  | jq -r --arg id "$APP_ID" '[.entries[] | select(.artifact.appId==$id and .artifact.kind=="ARTIFACT_KIND_BINARY")][0].artifact.version')
```

(Add `-plaintext` to `grpcurl` for a non-TLS local registry.)

## 3. Resolve the download URL

```bash
OS=linux ARCH=amd64   # linux|darwin, amd64|arm64
RESP=$(grpcurl -H "authorization: Bearer $TOKEN" \
  -d "{\"binary\":\"media-upload\",\"version\":\"$VERSION\",\"os\":\"$OS\",\"arch\":\"$ARCH\"}" \
  "$APP_REGISTRY_ADDRESS" appregistry.v1.ArtifactRegistry/ResolveBinaryURL)
```

The response has `downloadUrl` and `checksumManifestUrl`. Both are presigned and
expire after 15 minutes. An unpublished binary/version returns `NotFound`.

## 4. Download and verify

The file is named `<binary>-<os>-<arch>`; `checksums.txt` lists every platform.

```bash
NAME=media-upload-$OS-$ARCH
curl -fsSL -o "$NAME" "$(jq -r .downloadUrl <<<"$RESP")"
curl -fsSL -o checksums.txt "$(jq -r .checksumManifestUrl <<<"$RESP")"
grep " $NAME\$" checksums.txt | sha256sum -c -     # use `shasum -a 256 -c -` on macOS
install -m 0755 "$NAME" ~/.local/bin/media-upload
```

## 5. Set up and use `media-upload`

```bash
export MEDIA_UPLOAD_MCP_URL=<media-upload MCP endpoint>
export MEDIA_UPLOAD_TOKEN=<bearer token>
# or client credentials instead of a token:
#   MEDIA_UPLOAD_TOKEN_URL, MEDIA_UPLOAD_CLIENT_ID, MEDIA_UPLOAD_CLIENT_SECRET

media-upload demo.gif              # prints the public URL
media-upload --markdown demo.gif   # prints ![demo.gif](url) for a PR description
media-upload --temporary clip.mp4  # stored under the expiring tmp/ prefix
```

Full variable list: [`tools/media_upload/ENV.md`](../media_upload/ENV.md).

## Publishing a new CLI this way

1. Add a `release_app` with `app_type = "cli"`, `domain = "tools"` and
   `app_name = "<binary>"` (see `tools/media_upload/cli/BUILD.bazel`). A release
   publishes `<binary>/<version>/<binary>-<os>-<arch>` and `checksums.txt` to S3.
2. Add `"<binary>": "tools-<binary>"` to `binaryOwnerFullName` in
   `server/handlers/artifact.go` and a test in
   `server/handlers/resolve_binary_url_test.go`. Without this entry the RPC
   refuses the name. Deploy the registry after merging.
