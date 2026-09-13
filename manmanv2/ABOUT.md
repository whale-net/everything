# About ManManV2

ManManV2 is a game server management platform: it lets a small operator run
and manage third-party and custom game servers (Minecraft, Steam, custom
images) across a fleet of bare-metal hosts from one control plane — spin up
servers, deploy configurable instances of them, watch logs and status live,
back up and restore save data, and mod servers through a Steam-Workshop-style
addon system, all without hand-managing Docker on each box.

## Why it exists

Its predecessor, [ManMan v1](../manman/TOC.md), proved the concept in Python
but accumulated the constraints that motivated a rewrite: a split-plane
design (cloud control plane, per-host managers) that could scale host
capacity independently, Go end-to-end for a single deployable story, and a
container-first execution model built on Docker attach rather than wrapper
processes. V2 is that rewrite — v1 is in maintenance mode and v2 is the
active system.

## How it works, in one paragraph

A cloud control plane (API, event processor, log processor, UI) owns state
and the user-facing surface; a Go host manager runs on each bare-metal box,
owns Docker execution for game containers, and talks to the control plane
over RabbitMQ. Sessions are the unit of execution: starting a deployment
creates and attaches to a game container, streams its output, and survives
host-manager restarts via label-based recovery. Hosts keep themselves up to
date by polling App Registry promotion through a resolver sidecar — no
SSH-and-restart rollouts.

## Who it's for

- **Admin** — keeps the platform up; wants fleet-wide visibility into
  crashes and failures.
- **Server Manager** — sets up images, deploys and restarts servers, manages
  backups and mods.
- **Gamer** — not technical; just wants to check the server is up and grab
  the connection IP.

## Status

The core platform is live in production: API, UI, host managers, event
processing, log streaming, backups, and Workshop management. The roadmap
now focuses on operator convenience and fleet scale — per-deployment env
overrides, Workshop caching across hosts, and the UI redesign. For the
capability map, load-bearing decisions, and milestone roadmap, see
[PRODUCT.md](PRODUCT.md).

## Where to go next

- [TOC.md](TOC.md) — index of all documentation
- [ARCHITECTURE.md](ARCHITECTURE.md) — how the pieces fit
- [README.md](README.md) — run it locally
