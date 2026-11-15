import { createApp } from "./server.js";

const port = Number.parseInt(process.env.PORT ?? "8080", 10);
const server = createApp();

server.listen(port, () => {
  console.log(JSON.stringify({ level: "INFO", msg: "listening", port }));
});

// Kubernetes sends SIGTERM on pod shutdown; stop accepting and drain.
function shutdown(signal: string): void {
  console.log(JSON.stringify({ level: "INFO", msg: "shutting down", signal }));
  server.close(() => process.exit(0));
  server.closeIdleConnections();
  setTimeout(() => process.exit(1), 10_000).unref();
}

process.on("SIGTERM", () => shutdown("SIGTERM"));
process.on("SIGINT", () => shutdown("SIGINT"));
