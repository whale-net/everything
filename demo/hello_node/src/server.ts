import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";

function sendJSON(res: ServerResponse, status: number, body: unknown): void {
  const payload = JSON.stringify(body);
  res.writeHead(status, {
    "content-type": "application/json",
    "content-length": Buffer.byteLength(payload),
  });
  res.end(payload);
}

export function handle(req: IncomingMessage, res: ServerResponse): void {
  const path = new URL(req.url ?? "/", "http://localhost").pathname;
  if (req.method !== "GET") {
    sendJSON(res, 405, { error: "method not allowed" });
    return;
  }
  switch (path) {
    case "/health":
      sendJSON(res, 200, { status: "ok" });
      return;
    case "/":
      sendJSON(res, 200, {
        message: "Hello from Node.js",
        app: process.env.APP_NAME ?? "hello-node",
        node: process.version,
      });
      return;
    default:
      sendJSON(res, 404, { error: "not found" });
  }
}

export function createApp(): Server {
  return createServer(handle);
}
