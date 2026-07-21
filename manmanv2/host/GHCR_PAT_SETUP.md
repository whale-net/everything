# GHCR Read-Packages PAT + Docker Credential (Scaffold)

**Status:** Scaffold -- deferred to later issue (Watchtower auto-update on manman-host).
**Related:** Issue #453 closed/descoped. Future work will add Watchtower and use this credential for pulling `ghcr.io/whale-net/manmanv2-host-manager`.

## 1. Create Fine-Grained PAT

On GitHub (personal settings): **Settings > Developer Settings > Personal access tokens > Fine-grained tokens**

- Name: `watchtower-manman-host` (or whatever the future issue names it)
- Expiration: 90 days (rotation cadence — see section 4)
- Repository permissions: **Only repositories** → select none (this token never writes)
- **Scopes:** check only **`read:packages`** (no `contents`, no `write`, no other scopes)

Record the PAT string securely (`secret-tool store ghcr-manman-host <token>` or a vault). Do NOT commit it.

## 2. Install Docker Credential on manman-host

SSH to manman-host, then run:

```bash
# Write ~/.docker/config.json with GHCR entry
mkdir -p ~/.docker
cat > ~/.docker/config.json <<'EOF'
{
  "auths": {
    "ghcr.io": {
      "auth": "<base64(github_user:GHCR_PAT_STRING)>"
    }
  }
}
EOF
```

Compute the auth string:

```bash
echo -n 'github-user:ghcp_...' | base64 -w0
# Result is used in place of <auth> above
```

Verify:

```bash
docker login ghcr.io -u github-user --password-stdin <<< "ghcp_..."
docker pull ghcr.io/whale-net/manmanv2-host-manager:prod
```

## 3. Watchtower Integration (Future)

When the Watchtower issue lands, the credential in `~/.docker/config.json` is already what Watchtower reads automatically. No separate config is needed — just point Watchtower at the GHCR registry and it will use this auth entry for pulls.

Alternatively, if you prefer a standalone approach:

```bash
watchtower --http-api-token <token> \
  --auth none \
  --interval 300 \
  --filter-repo "manmanv2-host-manager" \
  ghcr.io/whale-net/manmanv2-host-manager
```

Watchtower uses Docker's own credential store for registry auth.

## 4. Rotation Runbook

PAT expires in **90 days**. Set a calendar reminder or use an issue template to track rotation.

Rotation steps:

1. Create a new fine-grained PAT with the same scopes (`read:packages` only).
2. SSH to manman-host and update `~/.docker/config.json` with the new base64 auth string.
3. Run `docker login ghcr.io` against manman-host to verify.
4. Test a pull: `docker pull ghcr.io/whale-net/manmanv2-host-manager:prod`.
5. Delete the old PAT from GitHub settings.

Update this doc's creation date when rotating.

---

**PAT created:** (fill in on implementation)
**Created by:** (fill in)
**Next rotation due:** +90 days from creation date
