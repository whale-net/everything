import assert from "node:assert/strict";
import type { AddressInfo } from "node:net";
import { after, before, test } from "node:test";

import { createApp } from "./server.js";

const server = createApp();
let base = "";

before(async () => {
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});

after(() => {
  server.close();
});

test("GET /health returns ok", async () => {
  const res = await fetch(`${base}/health`);
  assert.equal(res.status, 200);
  assert.deepEqual(await res.json(), { status: "ok" });
});

test("GET / returns greeting", async () => {
  const res = await fetch(`${base}/`);
  assert.equal(res.status, 200);
  const body = (await res.json()) as { message: string };
  assert.equal(body.message, "Hello from Node.js");
});

test("unknown path returns 404", async () => {
  const res = await fetch(`${base}/nope`);
  assert.equal(res.status, 404);
});
