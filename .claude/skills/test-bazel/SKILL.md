---
name: test-bazel
description: Run Bazel tests and return structured failure data. Use when asked to run tests or check build health.
context: fork
agent: test-bazel
model: haiku
---

Invokes the test-bazel subagent to run Bazel tests and report failures.
