#!/usr/bin/env python3
"""
Seed a local Tilt App Registry (tools/app_registry/Tiltfile) with a
realistic, browsable dataset: multiple real domains, multi-version release
history, an environment that's behind, one deliberate drift entry, one
rollback, and a mix of ADOPTED vs. OBSERVED artifact provenance.

Unlike scripts/seed_prod_artifacts.py (production disaster-recovery
adoption of a fixed point-in-time snapshot), this script is meant to be run
repeatedly against a local `tilt up` instance to produce a populated
registry for UI walkthroughs / demos. It is purely additive:

- Never calls `apps reconcile` (full-replace; would flag every already
  registered app as MISSING). Everything below targets apps/charts that are
  either already reconciled in a normal local Tilt session (the real
  `//...` manifests, adopted the same way TESTING.md's worked examples do)
  or is skipped with a warning if they aren't there yet.
- Every write uses a deterministic --idempotency-key derived from the
  target + version, so re-running this script against the same database is
  a no-op (idempotent replay), not an error pile-up.

Baseline versions/digests for the four product bundles below are taken from
scripts/seed_prod_artifacts.py's real, previously-adopted production
snapshot -- reused here as realistic "what prod is running" starting
points. The "next release" versions layered on top (promoted to dev/stage,
left off prod) are synthetic, with digests derived deterministically from
their own version strings so the script never needs a fixture file.

Usage:
    bazel build //tools/app_registry/cli:app-registry
    python3 tools/app_registry/scripts/seed_tilt_walkthrough.py

Requires `tilt up` (or `tilt ci`) already healthy -- see TESTING.md.
"""

import argparse
import hashlib
import json
import os
import subprocess
import tempfile

DEFAULT_ADDRESS = os.getenv("APP_REGISTRY_ADDRESS", "localhost:50061")


def fake_digest(label: str) -> str:
    """Deterministic, obviously-synthetic sha256 for a version that was
    never really built -- same label always produces the same digest, so
    re-running this script is idempotent at the artifact layer too."""
    return "sha256:" + hashlib.sha256(label.encode()).hexdigest()


# ---------------------------------------------------------------------------
# Product bundles: (domain-qualified) app full names, a chart that composes
# some of them, a "baseline" version (what's promoted to prod) and a "next"
# version (promoted to dev + stage, leaving prod behind) -- the exact shape
# the README's own example describes: "stage is running v1.4.0, prod is two
# versions behind."
# ---------------------------------------------------------------------------

PRODUCTS = [
    {
        "name": "app-registry",
        "chart_owner": "app-registry-app-registry",
        "chart_repo": "https://charts.whalenet.dev/app-registry",
        "baseline_chart_version": "v0.0.34",
        "baseline_chart_digest": "sha256:4af6bf319a94832b5493ced0804989800a99892a5d5c041caa1a2fffdc55113b",
        "next_chart_version": "v0.0.35",
        "images": [
            {
                "owner": "app-registry-api",
                "repository": "ghcr.io/whale-net/app-registry-api",
                "baseline_version": "v0.0.28",
                "baseline_digest": "sha256:9f91ce8180138f3b8a2889db4979b22839d71b2a5d789d5b5c163a26ff3477e3",
                "next_version": "v0.0.29",
                "bumped": True,
            },
            {
                "owner": "app-registry-migration",
                "repository": "ghcr.io/whale-net/app-registry-migration",
                "baseline_version": "v0.0.29",
                "baseline_digest": "sha256:ff0f2a62ea63037a9a4be8fdc3b65136112be2aab6aeab25930f52c5147d08bf",
                "next_version": "v0.0.29",
                "bumped": False,
            },
            {
                "owner": "app-registry-ui",
                "repository": "ghcr.io/whale-net/app-registry-ui",
                "baseline_version": "v0.0.1",
                "baseline_digest": "sha256:f1d0129a48c1e61933f5c3bfc68497aae18c20437fe4dca7746b420d876389a6",
                "next_version": "v0.0.2",
                "bumped": True,
            },
            {
                "owner": "app-registry-worker",
                "repository": "ghcr.io/whale-net/app-registry-worker",
                "baseline_version": "v0.0.25",
                "baseline_digest": "sha256:2caf856c97746a3bdbc33c72180229e343231cc3400ccb3b08e882f031cf747f",
                "next_version": "v0.0.25",
                "bumped": False,
            },
        ],
        "standalone": [],
        "drift": None,
        "rollback": None,
    },
    {
        "name": "manmanv2",
        "chart_owner": "manmanv2-control-services",
        "chart_repo": "https://charts.whalenet.dev/control-services",
        "baseline_chart_version": "v0.2.21",
        "baseline_chart_digest": "sha256:fa162ad71929ccd7ad42489326c0d46561be76031b1430f9437a3e5e596dbf67",
        "next_chart_version": "v0.2.22",
        "images": [
            {
                "owner": "manmanv2-control-api",
                "repository": "ghcr.io/whale-net/manmanv2-control-api",
                "baseline_version": "v0.2.19",
                "baseline_digest": "sha256:140f601fb3209a846c56b14f86eb86cbe1d308b1988b2901dddbf948af99c166",
                "next_version": "v0.2.20",
                "bumped": True,
            },
            {
                "owner": "manmanv2-control-migration",
                "repository": "ghcr.io/whale-net/manmanv2-control-migration",
                "baseline_version": "v0.2.19",
                "baseline_digest": "sha256:428e4855a92a1f76f3dea2604e44b4917960fb6d3b2b8e01677edd99d1e047c7",
                "next_version": "v0.2.19",
                "bumped": False,
            },
            {
                "owner": "manmanv2-event-processor",
                "repository": "ghcr.io/whale-net/manmanv2-event-processor",
                "baseline_version": "v0.2.19",
                "baseline_digest": "sha256:8eb7c354c52b2ac3aec27f248b796819b218971aae1674833dfa4ff8e9dfb94b",
                "next_version": "v0.2.19",
                "bumped": False,
            },
            {
                "owner": "manmanv2-log-processor",
                "repository": "ghcr.io/whale-net/manmanv2-log-processor",
                "baseline_version": "v0.2.19",
                "baseline_digest": "sha256:67ec2dec094a2c52246f44b97035014c8b242a6a034f1003388ffe91ddd12b64",
                "next_version": "v0.2.19",
                "bumped": False,
            },
            {
                "owner": "manmanv2-manmanv2-ui",
                "repository": "ghcr.io/whale-net/manmanv2-manmanv2-ui",
                "baseline_version": "v0.2.19",
                "baseline_digest": "sha256:67af5990e236b189383520679c1b8c0c137e225ea2af546f2ef0c99e049a984f",
                "next_version": "v0.2.20",
                "bumped": True,
            },
        ],
        # manmanv2-host-manager deploys as a bare image (DEPLOY_UNIT_IMAGE),
        # no chart -- standalone-promotable, and the one place we demo a
        # rollback.
        "standalone": [
            {
                "owner": "manmanv2-host-manager",
                "repository": "ghcr.io/whale-net/manmanv2-host-manager",
                "baseline_version": "v1.0.0",
                "next_version": "v1.1.0",
            },
        ],
        # Promote the OLD control-api image directly into stage, bypassing
        # its chart -- stage's chart pins v0.2.20, so this creates a real
        # drift entry for the Drift Audit screen.
        "drift": {
            "owner": "manmanv2-control-api",
            "env": "stage",
            "version": "v0.2.19",
        },
        # Roll dev's host-manager back from v1.1.0 to v1.0.0 after
        # promoting both, for the Rollback screen + a rollback event in
        # History.
        "rollback": {"owner": "manmanv2-host-manager", "env": "dev"},
    },
    {
        "name": "leaflab",
        "chart_owner": "leaflab-leaflab",
        "chart_repo": "https://charts.whalenet.dev/leaflab",
        "baseline_chart_version": "v0.0.10",
        "baseline_chart_digest": "sha256:9868dba32b5c63764e764060694beb49834c4d90ced743815c1abcc23c383f4b",
        "next_chart_version": "v0.0.11",
        "images": [
            {
                "owner": "leaflab-migrate",
                "repository": "ghcr.io/whale-net/leaflab-migrate",
                "baseline_version": "v0.0.6",
                "baseline_digest": "sha256:6a0c1e26454161c49e7a26bcb8ab30fc62794946ac6768ab3cf7abfa80962568",
                "next_version": "v0.0.6",
                "bumped": False,
            },
            {
                "owner": "leaflab-processor",
                "repository": "ghcr.io/whale-net/leaflab-processor",
                "baseline_version": "v0.0.6",
                "baseline_digest": "sha256:a8852bb7cefcf697e548a851d29f9c8247b838e3b671c5c7a138e3ad2735cb3e",
                "next_version": "v0.0.7",
                "bumped": True,
            },
        ],
        "standalone": [],
        "drift": None,
        "rollback": None,
    },
    {
        "name": "friendly-computing-machine",
        "chart_owner": "friendly-computing-machine-bot-services",
        "chart_repo": "https://charts.whalenet.dev/bot-services",
        "baseline_chart_version": "v0.1.2",
        "baseline_chart_digest": "sha256:9204845c4df0a6ba818db32ab9f6e43410bdc9eae14653bb041c10ef34d7f1f9",
        "next_chart_version": "v0.1.3",
        "images": [
            {
                "owner": "friendly-computing-machine-bot",
                "repository": "ghcr.io/whale-net/friendly-computing-machine-bot",
                "baseline_version": "v0.1.0",
                "baseline_digest": "sha256:c337a4b9a9ef48a1ea3e06db7b8997756e05348f03319bd9308b12130839b7de",
                "next_version": "v0.1.1",
                "bumped": True,
                # This one is recorded via `builds record` + `artifacts
                # record` (the real CI path) instead of `artifacts adopt`,
                # so the walkthrough has a genuinely OBSERVED artifact next
                # to everything else's ADOPTED provenance.
                "record_next_via_build": True,
            },
            {
                "owner": "friendly-computing-machine-migration",
                "repository": "ghcr.io/whale-net/friendly-computing-machine-migration",
                "baseline_version": "v0.1.0",
                "baseline_digest": "sha256:0822ab3850110c99113f84e359f4edebb978156b6706a383bd52e30371ee3b55",
                "next_version": "v0.1.0",
                "bumped": False,
            },
            {
                "owner": "friendly-computing-machine-subscribe",
                "repository": "ghcr.io/whale-net/friendly-computing-machine-subscribe",
                "baseline_version": "v0.1.0",
                "baseline_digest": "sha256:1ce79d540686e78416eb86cfe74ebd063c788899bd8596e0b5d6ee8008b77a2b",
                "next_version": "v0.1.0",
                "bumped": False,
            },
            {
                "owner": "friendly-computing-machine-taskpool",
                "repository": "ghcr.io/whale-net/friendly-computing-machine-taskpool",
                "baseline_version": "v0.1.1",
                "baseline_digest": "sha256:882485ffec6c2520e70dd137c8746ed5da216c7d4e7e9778ef5cd862fd4d3fa2",
                "next_version": "v0.1.1",
                "bumped": False,
            },
            {
                "owner": "friendly-computing-machine-worker",
                "repository": "ghcr.io/whale-net/friendly-computing-machine-worker",
                "baseline_version": "v0.1.0",
                "baseline_digest": "sha256:4013382e5933de576c615e30302b04b8e33f45b07dbb36321322feb86f150173",
                "next_version": "v0.1.1",
                "bumped": True,
            },
        ],
        "standalone": [],
        "drift": None,
        "rollback": None,
    },
]

REASON = "seed_tilt_walkthrough.py: populate local registry for a UI walkthrough"


def run(cli, args, dry_run=False):
    cmd = cli + args
    if dry_run:
        print(f"[DRY-RUN] {' '.join(cmd)}")
        return True
    res = subprocess.run(cmd, text=True, capture_output=True)
    if res.returncode != 0:
        msg = (res.stderr or res.stdout).strip()
        print(f"  x  {' '.join(args[:3])} ... -> {msg}")
        return False
    return True


def run_json(cli, args) -> dict:
    """Like run(), but always executes (never dry-run) and returns the
    parsed JSON response body -- for call sites that need a returned id
    (e.g. `builds record`'s build_id) rather than just success/failure."""
    res = subprocess.run(cli + args, text=True, capture_output=True)
    if res.returncode != 0:
        msg = (res.stderr or res.stdout).strip()
        print(f"  x  {' '.join(args[:3])} ... -> {msg}")
        return {}
    try:
        return json.loads(res.stdout)
    except json.JSONDecodeError:
        return {}


def adopt(cli, kind, owner, repository, version, digest, contains=None, dry_run=False):
    cmd = [
        "artifacts", "adopt",
        "--kind", kind,
        "--owner", owner,
        "--repository", repository,
        "--version", version,
        "--digest", digest,
        "--reason", REASON,
        "--idempotency-key", f"seed-adopt-{owner}-{kind}-{version}",
    ]
    if contains:
        cmd += ["--contains", contains]
    return run(cli, cmd, dry_run=dry_run)


def promote(cli, owner, version, env, kind="image", allow_override=False, dry_run=False):
    cmd = [
        "promote", owner, version,
        "--env", env,
        "--kind", kind,
        "--reason", REASON,
        "--idempotency-key", f"seed-promote-{owner}-{kind}-{version}-{env}",
    ]
    if allow_override:
        cmd.append("--allow-override")
    return run(cli, cmd, dry_run=dry_run)


def rollback(cli, owner, env, kind="image", dry_run=False):
    cmd = [
        "rollback", owner,
        "--env", env,
        "--kind", kind,
        "--reason", REASON,
        "--idempotency-key", f"seed-rollback-{owner}-{kind}-{env}",
    ]
    return run(cli, cmd, dry_run=dry_run)


def record_build_and_artifact(cli, owner, repository, version, digest, dry_run=False):
    """The real CI write path (`builds record` -> `artifacts record`),
    used for the one artifact per run we want OBSERVED provenance on
    instead of ADOPTED."""
    run_id = f"seed-{hashlib.sha256((owner + version).encode()).hexdigest()[:10]}"
    build_cmd = [
        "builds", "record",
        "--git-sha", hashlib.sha256(f"{owner}-{version}".encode()).hexdigest()[:40],
        "--git-ref", "refs/heads/main",
        "--workflow-run-id", run_id,
        "--actor", "seed-tilt-walkthrough",
        "--idempotency-key", f"seed-build-{owner}-{version}",
    ]
    if dry_run:
        print(f"[DRY-RUN] {' '.join(cli + build_cmd)}")
        return True
    build = run_json(cli, build_cmd)
    build_id = build.get("build", {}).get("buildId")
    if not build_id:
        print(f"  x  builds record for {owner} {version} did not return a buildId; skipping artifacts record")
        return False
    return run(cli, [
        "artifacts", "record",
        "--build-id", build_id,
        "--kind", "image",
        "--owner", owner,
        "--repository", repository,
        "--version", version,
        "--digest", digest,
        "--idempotency-key", f"seed-record-{owner}-image-{version}",
    ], dry_run=dry_run)


def seed_product(cli, product, dry_run):
    name = product["name"]
    print(f"\n=== {name} ===")

    baseline_contains = []
    next_contains = []
    for img in product["images"]:
        baseline_contains.append({
            "app_full_name": img["owner"],
            "repository": img["repository"],
            "version": img["baseline_version"],
            "digest": img["baseline_digest"],
        })
        next_version = img["next_version"]
        next_digest = (
            img["baseline_digest"] if not img["bumped"]
            else fake_digest(f"{img['owner']}-{next_version}")
        )
        next_contains.append({
            "app_full_name": img["owner"],
            "repository": img["repository"],
            "version": next_version,
            "digest": next_digest,
        })

        # Baseline image -- always via adopt (this is "what's already out
        # there").
        adopt(cli, "image", img["owner"], img["repository"], img["baseline_version"], img["baseline_digest"], dry_run=dry_run)

        if img["bumped"]:
            if img.get("record_next_via_build"):
                record_build_and_artifact(cli, img["owner"], img["repository"], next_version, next_digest, dry_run=dry_run)
            else:
                adopt(cli, "image", img["owner"], img["repository"], next_version, next_digest, dry_run=dry_run)

    with tempfile.TemporaryDirectory() as tmp:
        baseline_path = os.path.join(tmp, "baseline-contains.json")
        next_path = os.path.join(tmp, "next-contains.json")
        with open(baseline_path, "w") as f:
            json.dump(baseline_contains, f)
        with open(next_path, "w") as f:
            json.dump(next_contains, f)

        adopt(cli, "chart", product["chart_owner"], product["chart_repo"],
              product["baseline_chart_version"], product["baseline_chart_digest"],
              contains=baseline_path, dry_run=dry_run)
        next_chart_digest = fake_digest(f"{product['chart_owner']}-{product['next_chart_version']}")
        adopt(cli, "chart", product["chart_owner"], product["chart_repo"],
              product["next_chart_version"], next_chart_digest,
              contains=next_path, dry_run=dry_run)

    # Prod stays on the baseline; stage and dev get the next release --
    # the "prod is behind" story.
    promote(cli, product["chart_owner"], product["baseline_chart_version"], "prod", kind="chart", dry_run=dry_run)
    promote(cli, product["chart_owner"], product["next_chart_version"], "stage", kind="chart", dry_run=dry_run)
    promote(cli, product["chart_owner"], product["next_chart_version"], "dev", kind="chart", dry_run=dry_run)

    for s in product["standalone"]:
        base_digest = fake_digest(f"{s['owner']}-{s['baseline_version']}")
        next_digest = fake_digest(f"{s['owner']}-{s['next_version']}")
        adopt(cli, "image", s["owner"], s["repository"], s["baseline_version"], base_digest, dry_run=dry_run)
        adopt(cli, "image", s["owner"], s["repository"], s["next_version"], next_digest, dry_run=dry_run)
        promote(cli, s["owner"], s["baseline_version"], "prod", kind="image", dry_run=dry_run)
        promote(cli, s["owner"], s["next_version"], "stage", kind="image", dry_run=dry_run)
        # dev gets baseline first, then next -- so a rollback demo (below)
        # has a prior promotion to roll back to.
        promote(cli, s["owner"], s["baseline_version"], "dev", kind="image", dry_run=dry_run)
        promote(cli, s["owner"], s["next_version"], "dev", kind="image", dry_run=dry_run)

    if product["drift"]:
        d = product["drift"]
        print(f"  ~  deliberate drift: {d['owner']} {d['version']} direct into {d['env']}")
        promote(cli, d["owner"], d["version"], d["env"], kind="image", allow_override=True, dry_run=dry_run)

    if product["rollback"]:
        r = product["rollback"]
        print(f"  ~  rollback demo: {r['owner']} in {r['env']}")
        rollback(cli, r["owner"], r["env"], kind="image", dry_run=dry_run)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--address", default=DEFAULT_ADDRESS, help=f"app-registry-api address (default: {DEFAULT_ADDRESS})")
    parser.add_argument("--cli-bin", default=None, help="Path to a prebuilt app-registry binary (default: bazel-bin/tools/app_registry/cli/app-registry_/app-registry, built if missing)")
    parser.add_argument("--dry-run", action="store_true", help="Print commands without executing them")
    args = parser.parse_args()

    cli_bin = args.cli_bin
    if not cli_bin:
        default_bin = os.path.join("bazel-bin", "tools", "app_registry", "cli", "app-registry_", "app-registry")
        if not os.path.exists(default_bin):
            print("Building //tools/app_registry/cli:app-registry ...")
            subprocess.run(["bazel", "build", "//tools/app_registry/cli:app-registry"], check=True)
        cli_bin = default_bin

    cli = [cli_bin, "--address", args.address]

    print("=" * 70)
    print("Seeding local App Registry (Tilt) for a UI walkthrough")
    print(f"Address: {args.address}")
    print(f"Dry run: {args.dry_run}")
    print("=" * 70)

    for product in PRODUCTS:
        seed_product(cli, product, args.dry_run)

    print("\nDone. Try:")
    print(f"  {cli_bin} --address {args.address} status prod")
    print(f"  {cli_bin} --address {args.address} diff prod dev")
    print("  open http://localhost:8090")


if __name__ == "__main__":
    main()
