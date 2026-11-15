# hello-node

Reference Node.js/TypeScript app: a plain `node:http` server released through
`release_app(language = "node")`. Copy this layout for new Node services.

- `GET /health` → `{"status":"ok"}`
- `GET /` → greeting with app name and Node version
- Listens on `$PORT` (default `8080`); drains and exits on `SIGTERM`.

## Layout

| Target | What it is |
|---|---|
| `:ts` | `ts_project` — type-checks with TypeScript 7 and emits `.js` for tests |
| `:hello-node` | `esbuild` bundle → single `hello-node.mjs`, no externals (the release binary) |
| `:hello-node_bin` | `js_binary` wrapping the bundle, for local runs |
| `:server_test` | `js_test` running `node --test` over the compiled test |
| `:hello-node_image` | multi-arch image (amd64 + arm64) from `release_app` |

npm dependencies come from the root pnpm workspace (`//:pnpm-lock.yaml`); this
package's own `package.json` holds its devDependencies.

## Commands

```bash
bazel test //demo/hello_node/...
bazel run //demo/hello_node:hello-node_bin          # then: curl localhost:8080/health

# Image (needs a Docker daemon)
bazel run //demo/hello_node:hello-node_image_load
docker run --rm -p 8080:8080 demo-hello-node:latest
curl localhost:8080/health

# Helm chart (//demo:node_chart)
bazel build //demo:node_chart
helm template t bazel-bin/demo/helm-demo-hello-node_chart/demo-hello-node
```

## Image layout

`@ubuntu_base` + CA certs + Node 24 at `/opt/node/bin/node` (per-arch, from
`rules_nodejs`) + the bundle at `/app/main.mjs`. Entrypoint is
`/opt/node/bin/node /app/main.mjs`. There is no `node_modules` in the image, so
runtime dependencies must be bundled.
