#!/usr/bin/env python3
"""
Seed production App Registry with pre-existing images and Helm charts using `AdoptArtifact`.

This script automates:
1. Reconciling apps/charts manifest definitions into App Registry (optional).
2. Adopting all image artifacts (owner, version, repository, sha256 digest).
3. Adopting all Helm chart artifacts with their pinned image references (--contains JSON).
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile

# Complete specification of target charts and pinned images extracted from https://charts.whalenet.dev
SEED_DATA = {
    "images": [
        # app-registry
        {
            "owner": "app-registry-api",
            "repository": "ghcr.io/whale-net/app-registry-api",
            "version": "v0.0.28",
            "digest": "sha256:9f91ce8180138f3b8a2889db4979b22839d71b2a5d789d5b5c163a26ff3477e3",
        },
        {
            "owner": "app-registry-migration",
            "repository": "ghcr.io/whale-net/app-registry-migration",
            "version": "v0.0.29",
            "digest": "sha256:ff0f2a62ea63037a9a4be8fdc3b65136112be2aab6aeab25930f52c5147d08bf",
        },
        {
            "owner": "app-registry-ui",
            "repository": "ghcr.io/whale-net/app-registry-ui",
            "version": "v0.0.1",
            "digest": "sha256:f1d0129a48c1e61933f5c3bfc68497aae18c20437fe4dca7746b420d876389a6",
        },
        {
            "owner": "app-registry-worker",
            "repository": "ghcr.io/whale-net/app-registry-worker",
            "version": "v0.0.25",
            "digest": "sha256:2caf856c97746a3bdbc33c72180229e343231cc3400ccb3b08e882f031cf747f",
        },
        # manmanv2
        {
            "owner": "manmanv2-control-api",
            "repository": "ghcr.io/whale-net/manmanv2-control-api",
            "version": "v0.2.19",
            "digest": "sha256:140f601fb3209a846c56b14f86eb86cbe1d308b1988b2901dddbf948af99c166",
        },
        {
            "owner": "manmanv2-control-migration",
            "repository": "ghcr.io/whale-net/manmanv2-control-migration",
            "version": "v0.2.19",
            "digest": "sha256:428e4855a92a1f76f3dea2604e44b4917960fb6d3b2b8e01677edd99d1e047c7",
        },
        {
            "owner": "manmanv2-event-processor",
            "repository": "ghcr.io/whale-net/manmanv2-event-processor",
            "version": "v0.2.19",
            "digest": "sha256:8eb7c354c52b2ac3aec27f248b796819b218971aae1674833dfa4ff8e9dfb94b",
        },
        {
            "owner": "manmanv2-log-processor",
            "repository": "ghcr.io/whale-net/manmanv2-log-processor",
            "version": "v0.2.19",
            "digest": "sha256:67ec2dec094a2c52246f44b97035014c8b242a6a034f1003388ffe91ddd12b64",
        },
        {
            "owner": "manmanv2-manmanv2-ui",
            "repository": "ghcr.io/whale-net/manmanv2-manmanv2-ui",
            "version": "v0.2.19",
            "digest": "sha256:67af5990e236b189383520679c1b8c0c137e225ea2af546f2ef0c99e049a984f",
        },
        # leaflab
        {
            "owner": "leaflab-migrate",
            "repository": "ghcr.io/whale-net/leaflab-migrate",
            "version": "v0.0.6",
            "digest": "sha256:6a0c1e26454161c49e7a26bcb8ab30fc62794946ac6768ab3cf7abfa80962568",
        },
        {
            "owner": "leaflab-processor",
            "repository": "ghcr.io/whale-net/leaflab-processor",
            "version": "v0.0.6",
            "digest": "sha256:a8852bb7cefcf697e548a851d29f9c8247b838e3b671c5c7a138e3ad2735cb3e",
        },
        # friendly-computing-machine
        {
            "owner": "friendly-computing-machine-bot",
            "repository": "ghcr.io/whale-net/friendly-computing-machine-bot",
            "version": "v0.1.0",
            "digest": "sha256:c337a4b9a9ef48a1ea3e06db7b8997756e05348f03319bd9308b12130839b7de",
        },
        {
            "owner": "friendly-computing-machine-migration",
            "repository": "ghcr.io/whale-net/friendly-computing-machine-migration",
            "version": "v0.1.0",
            "digest": "sha256:0822ab3850110c99113f84e359f4edebb978156b6706a383bd52e30371ee3b55",
        },
        {
            "owner": "friendly-computing-machine-subscribe",
            "repository": "ghcr.io/whale-net/friendly-computing-machine-subscribe",
            "version": "v0.1.0",
            "digest": "sha256:1ce79d540686e78416eb86cfe74ebd063c788899bd8596e0b5d6ee8008b77a2b",
        },
        {
            "owner": "friendly-computing-machine-taskpool",
            "repository": "ghcr.io/whale-net/friendly-computing-machine-taskpool",
            "version": "v0.1.1",
            "digest": "sha256:882485ffec6c2520e70dd137c8746ed5da216c7d4e7e9778ef5cd862fd4d3fa2",
        },
        {
            "owner": "friendly-computing-machine-worker",
            "repository": "ghcr.io/whale-net/friendly-computing-machine-worker",
            "version": "v0.1.0",
            "digest": "sha256:4013382e5933de576c615e30302b04b8e33f45b07dbb36321322feb86f150173",
        },
    ],
    "charts": [
        {
            "owner": "app-registry-app-registry",
            "repository": "https://charts.whalenet.dev/app-registry",
            "version": "v0.0.34",
            "digest": "sha256:4af6bf319a94832b5493ced0804989800a99892a5d5c041caa1a2fffdc55113b",
            "contains": [
                {
                    "app_full_name": "app-registry-api",
                    "repository": "ghcr.io/whale-net/app-registry-api",
                    "version": "v0.0.28",
                    "digest": "sha256:9f91ce8180138f3b8a2889db4979b22839d71b2a5d789d5b5c163a26ff3477e3",
                },
                {
                    "app_full_name": "app-registry-migration",
                    "repository": "ghcr.io/whale-net/app-registry-migration",
                    "version": "v0.0.29",
                    "digest": "sha256:ff0f2a62ea63037a9a4be8fdc3b65136112be2aab6aeab25930f52c5147d08bf",
                },
                {
                    "app_full_name": "app-registry-ui",
                    "repository": "ghcr.io/whale-net/app-registry-ui",
                    "version": "v0.0.1",
                    "digest": "sha256:f1d0129a48c1e61933f5c3bfc68497aae18c20437fe4dca7746b420d876389a6",
                },
                {
                    "app_full_name": "app-registry-worker",
                    "repository": "ghcr.io/whale-net/app-registry-worker",
                    "version": "v0.0.25",
                    "digest": "sha256:2caf856c97746a3bdbc33c72180229e343231cc3400ccb3b08e882f031cf747f",
                },
            ],
        },
        {
            "owner": "manmanv2-control-services",
            "repository": "https://charts.whalenet.dev/control-services",
            "version": "v0.2.21",
            "digest": "sha256:fa162ad71929ccd7ad42489326c0d46561be76031b1430f9437a3e5e596dbf67",
            "contains": [
                {
                    "app_full_name": "manmanv2-control-api",
                    "repository": "ghcr.io/whale-net/manmanv2-control-api",
                    "version": "v0.2.19",
                    "digest": "sha256:140f601fb3209a846c56b14f86eb86cbe1d308b1988b2901dddbf948af99c166",
                },
                {
                    "app_full_name": "manmanv2-control-migration",
                    "repository": "ghcr.io/whale-net/manmanv2-control-migration",
                    "version": "v0.2.19",
                    "digest": "sha256:428e4855a92a1f76f3dea2604e44b4917960fb6d3b2b8e01677edd99d1e047c7",
                },
                {
                    "app_full_name": "manmanv2-event-processor",
                    "repository": "ghcr.io/whale-net/manmanv2-event-processor",
                    "version": "v0.2.19",
                    "digest": "sha256:8eb7c354c52b2ac3aec27f248b796819b218971aae1674833dfa4ff8e9dfb94b",
                },
                {
                    "app_full_name": "manmanv2-log-processor",
                    "repository": "ghcr.io/whale-net/manmanv2-log-processor",
                    "version": "v0.2.19",
                    "digest": "sha256:67ec2dec094a2c52246f44b97035014c8b242a6a034f1003388ffe91ddd12b64",
                },
                {
                    "app_full_name": "manmanv2-manmanv2-ui",
                    "repository": "ghcr.io/whale-net/manmanv2-manmanv2-ui",
                    "version": "v0.2.19",
                    "digest": "sha256:67af5990e236b189383520679c1b8c0c137e225ea2af546f2ef0c99e049a984f",
                },
            ],
        },
        {
            "owner": "leaflab-leaflab",
            "repository": "https://charts.whalenet.dev/leaflab",
            "version": "v0.0.10",
            "digest": "sha256:9868dba32b5c63764e764060694beb49834c4d90ced743815c1abcc23c383f4b",
            "contains": [
                {
                    "app_full_name": "leaflab-migrate",
                    "repository": "ghcr.io/whale-net/leaflab-migrate",
                    "version": "v0.0.6",
                    "digest": "sha256:6a0c1e26454161c49e7a26bcb8ab30fc62794946ac6768ab3cf7abfa80962568",
                },
                {
                    "app_full_name": "leaflab-processor",
                    "repository": "ghcr.io/whale-net/leaflab-processor",
                    "version": "v0.0.6",
                    "digest": "sha256:a8852bb7cefcf697e548a851d29f9c8247b838e3b671c5c7a138e3ad2735cb3e",
                },
            ],
        },
        {
            "owner": "friendly-computing-machine-bot-services",
            "repository": "https://charts.whalenet.dev/bot-services",
            "version": "v0.1.2",
            "digest": "sha256:9204845c4df0a6ba818db32ab9f6e43410bdc9eae14653bb041c10ef34d7f1f9",
            "contains": [
                {
                    "app_full_name": "friendly-computing-machine-bot",
                    "repository": "ghcr.io/whale-net/friendly-computing-machine-bot",
                    "version": "v0.1.0",
                    "digest": "sha256:c337a4b9a9ef48a1ea3e06db7b8997756e05348f03319bd9308b12130839b7de",
                },
                {
                    "app_full_name": "friendly-computing-machine-migration",
                    "repository": "ghcr.io/whale-net/friendly-computing-machine-migration",
                    "version": "v0.1.0",
                    "digest": "sha256:0822ab3850110c99113f84e359f4edebb978156b6706a383bd52e30371ee3b55",
                },
                {
                    "app_full_name": "friendly-computing-machine-subscribe",
                    "repository": "ghcr.io/whale-net/friendly-computing-machine-subscribe",
                    "version": "v0.1.0",
                    "digest": "sha256:1ce79d540686e78416eb86cfe74ebd063c788899bd8596e0b5d6ee8008b77a2b",
                },
                {
                    "app_full_name": "friendly-computing-machine-taskpool",
                    "repository": "ghcr.io/whale-net/friendly-computing-machine-taskpool",
                    "version": "v0.1.1",
                    "digest": "sha256:882485ffec6c2520e70dd137c8746ed5da216c7d4e7e9778ef5cd862fd4d3fa2",
                },
                {
                    "app_full_name": "friendly-computing-machine-worker",
                    "repository": "ghcr.io/whale-net/friendly-computing-machine-worker",
                    "version": "v0.1.0",
                    "digest": "sha256:4013382e5933de576c615e30302b04b8e33f45b07dbb36321322feb86f150173",
                },
            ],
        },
    ],
}


def run_cli_cmd(cmd_args, dry_run=False):
    cmd_str = " ".join(cmd_args)
    if dry_run:
        print(f"[DRY-RUN] {cmd_str}")
        return True

    print(f"--> Executing: {cmd_str}")
    res = subprocess.run(cmd_args, text=True, capture_output=True)
    if res.returncode != 0:
        print(f"❌ Error (exit {res.returncode}):\n{res.stderr.strip() or res.stdout.strip()}")
        return False
    else:
        if res.stdout.strip():
            print(f"✔ Success:\n{res.stdout.strip()}")
        return True


def main():
    parser = argparse.ArgumentParser(description="Seed App Registry with adopted images and charts")
    parser.add_argument("--address", default=os.getenv("APP_REGISTRY_ADDRESS", "localhost:50051"),
                        help="App Registry API address host:port (default: $APP_REGISTRY_ADDRESS or localhost:50051)")
    parser.add_argument("--reason", default="Seed production registry with pre-existing release artifacts",
                        help="Reason string for AdoptArtifact")
    parser.add_argument("--cli-bin", default=None,
                        help="Path to prebuilt app-registry binary (if omitted, uses bazel run //tools/app_registry/cli:app-registry --)")
    parser.add_argument("--dry-run", action="store_true",
                        help="Print adoption commands without executing them")
    parser.add_argument("--skip-reconcile", action="store_true",
                        help="Skip running 'apps reconcile' before adopting")
    args = parser.parse_args()

    if args.cli_bin:
        base_cmd = [args.cli_bin, "--address", args.address]
    else:
        base_cmd = ["bazel", "run", "//tools/app_registry/cli:app-registry", "--", "--address", args.address]

    print("=" * 70)
    print("🌱 App Registry Artifact Adoption Seeder")
    print(f"Target Address: {args.address}")
    print(f"Reason:         {args.reason}")
    print(f"Dry Run:        {args.dry_run}")
    print("=" * 70)

    # 1. Apps Reconcile
    if not args.skip_reconcile:
        print("\n--- Step 1: Reconciling Apps Manifest into App Registry ---")
        reconcile_cmd = base_cmd + ["apps", "reconcile"]
        if not run_cli_cmd(reconcile_cmd, dry_run=args.dry_run):
            print("⚠️ App manifest reconciliation returned non-zero (continuing with adoption)...")

    # 2. Adopt Images
    print("\n--- Step 2: Adopting Image Artifacts ---")
    image_success = 0
    for img in SEED_DATA["images"]:
        cmd = base_cmd + [
            "artifacts", "adopt",
            "--kind", "image",
            "--owner", img["owner"],
            "--repository", img["repository"],
            "--version", img["version"],
            "--digest", img["digest"],
            "--reason", args.reason,
        ]
        if run_cli_cmd(cmd, dry_run=args.dry_run):
            image_success += 1

    # 3. Adopt Charts
    print("\n--- Step 3: Adopting Helm Chart Artifacts ---")
    chart_success = 0
    with tempfile.TemporaryDirectory() as tmp_dir:
        for chart in SEED_DATA["charts"]:
            contains_path = os.path.join(tmp_dir, f"{chart['owner']}-contains.json")
            with open(contains_path, "w") as f:
                json.dump(chart["contains"], f, indent=2)

            cmd = base_cmd + [
                "artifacts", "adopt",
                "--kind", "chart",
                "--owner", chart["owner"],
                "--repository", chart["repository"],
                "--version", chart["version"],
                "--digest", chart["digest"],
                "--contains", contains_path,
                "--reason", args.reason,
            ]
            if run_cli_cmd(cmd, dry_run=args.dry_run):
                chart_success += 1

    print("\n" + "=" * 70)
    print(f"🎉 Seeding Complete! Adopted {image_success}/{len(SEED_DATA['images'])} images, {chart_success}/{len(SEED_DATA['charts'])} charts.")
    print("=" * 70)


if __name__ == "__main__":
    main()
