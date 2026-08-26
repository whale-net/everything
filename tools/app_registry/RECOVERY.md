# App Registry — Recovery Runbook

Procedures for obtaining published CLI artifacts (release_helper_go and app-registry) when the normal acquisition path is unavailable. Validated end-to-end by an operator before the legacy path is deleted.

**Operator prerequisites:** This runbook is written for someone who understands the release pipeline and can read/write to the registry's database if needed, but may not have built the system from source. No CLI tool is assumed to be working.

---

## (i) The acquisition escape hatches

Three acquisition routes exist independently of the registry API server. At least one will work in almost every scenario.

### 1. Build-from-source acquisition

**When to use:** The fastest self-contained route when you have access to a development environment and the Git repository.

**Procedure:**
```bash
cd /path/to/everything
bazel build //tools/release_helper_go/cmd:release_helper_go
bazel build //tools/app_registry/cli:app-registry
```

The binaries are available at:
- `bazel-bin/tools/release_helper_go/cmd/release_helper_go_/release_helper_go`
- `bazel-bin/tools/app_registry/cli/app-registry_/app-registry`

**Validation:** Run each binary with `--version` or `--help` to confirm they execute.

### 2. Workflow-artifact tooling fallback

**When to use:** When a release run completed its build step but failed later (recording, promotion, or deployment), and you want to use the exact same binaries the run built.

**Procedure:**

1. Locate the GitHub Actions workflow run: GitHub → Actions → find the release run
2. Scroll to the **artifacts** section (appears after all jobs complete, whether they passed or failed)
3. Download the `release-tools` artifact
4. Extract it:
   ```bash
   unzip release-tools.zip -d /tmp/release-tools
   chmod +x /tmp/release-tools/release_helper_go
   chmod +x /tmp/release-tools/app-registry
   ```

**Validation:** Extracted binaries are already verified (the artifact was created at build time) and are executable immediately. Run each with `--version` to confirm.

### 3. Release workflow's source-tools safety valve

**When to use:** When the published binary acquisition path fails and you need to run the release or artifact operations through CI without intervention.

**Procedure:**

1. Set the `RELEASE_TOOLS_FALLBACK_TO_SOURCE` repository variable to `true`:
   - Go to GitHub → Settings → Secrets and variables → Actions → Variables
   - Add or edit `RELEASE_TOOLS_FALLBACK_TO_SOURCE` with value `true`
2. Dispatch the release workflow (or re-run it if already queued): GitHub → Actions → Release → Run workflow
3. The workflow will detect the fallback is enabled and build the tools from source via Bazel instead of downloading prebuilt binaries

**Important notes:**
- This only affects new workflow runs started after the variable is set; re-running a failed step of an already-started run does not pick it up
- Once the run completes, restore the variable to unset (or leave it `false`) to resume fast prebuilt download for subsequent releases
- The first safety-valve run will be slower than normal (Bazel compilation) but produces the same binaries

**Validation:** The workflow run will show a `Setup Build Environment (fallback safety valve)` step. If set to build from source, the step will be active instead of skipped.

---

## (ii) Registry-unreachable recovery route

**When to use:** The registry API server is completely down, but the database is up and you have SQL access to the registry's Postgres instance. This allows recovery of published artifacts via direct key lookup from the registry's database, followed by credentialed object-store access.

**Preconditions:**
- Registry Postgres database is reachable and responsive
- You have read access to the registry's Postgres database
- You have the registry's S3 object-store read credential (NFR-22 read grant)
- You know the binary's name, version, and OS/architecture target

**Important boundary:** If the database is also unreachable, there is no route to fetch the exact published bytes via this procedure. However, **bootstrap does not depend on this** — bootstrap rests entirely on acquisition routes (i), (ii) or (iii) above, which work independently of both the registry and its read-access posture (NFR-21, FR-68). What is unavailable in that scenario is "fetch this specific published artifact," not "obtain working tooling."

### Step 1: Discover the object key from the registry database

The registry stores the actual S3 key for each published version under each declared variant. No bucket enumeration is needed or possible (NFR-22 grants no list permission, and keys are opaque by design — FR-60).

1. Connect to the registry's Postgres database:
   ```bash
   psql "$PG_DATABASE_URL"
   ```

2. Query for the stored key:
   ```sql
   SELECT stored_object_key FROM stored_object_key
   WHERE app_id = (SELECT app_id FROM app WHERE domain = 'tools' AND name = 'release_helper_go')
     AND version = 'v1.2.3'
     AND variant = 'linux-amd64'
   ORDER BY created_at DESC LIMIT 1;
   ```

   Replace `'tools'`, `'release_helper_go'`, `'v1.2.3'`, and `'linux-amd64'` with your actual values.

3. The query returns a single string: the full S3 key, for example: `release_helper_go/v1.2.3/release_helper_go-linux-amd64`

**If the query returns no rows:** The version was not recorded by the registry (either because recording was disabled or because this is a pre-registry version). Proceed to the recovery-only appendix below if this is a pre-cutover version. Otherwise, fall back to route (i) or (iii).

### Step 2: Understand why bucket listing won't work

Bucket enumeration is not available as a fallback because:

1. **Keys are opaque** (FR-60): The registry does not publish the key structure as a contract. The shape of the key string is an internal implementation detail subject to change. Enumerating the bucket returns meaningless strings with no way to identify which one corresponds to your requested artifact/version/variant.

2. **NFR-22 grants no list permission**: The read credential the registry uses is scoped to allow reads over the whole namespace but does not grant `s3:ListBucket`. Even if keys were enumerable, you lack the capability to list.

3. **Result:** Bucket enumeration cannot work for finding published artifacts. Key discovery via the database is the only workable path during registry outage.

### Step 3: If pre-cutover (no stored key) — recovery-only appendix: H8 derivation

**This step applies only if Step 1's query returned no rows**, indicating a pre-cutover version not recorded by the registry.

**Disclaimer:** The following procedure is recovery-only and not contractual. It applies only to pre-registry versions and is documented here precisely because those versions have no stored key in the registry. **No consumer code is told this derivation, and no code outside the registry derives keys from this template.** This is an emergency procedure, not a forward-facing contract.

**H8 — Pre-cutover binary naming convention:**

- **For binaries:** `<binary-name>/<version>/<binary-name>-<os>-<arch>`
  - Example: `release_helper_go/v1.2.0/release_helper_go-linux-amd64`
- **For checksums:** `<binary-name>/<version>/checksums.txt`
  - Example: `release_helper_go/v1.2.0/checksums.txt`

If the version is pre-registry and you know it was published under this convention, you can construct the key and attempt to fetch it manually in Step 4. However:

- **Verify the version actually exists in your object store** (via S3 console, CLI, or a tool with the read credential) before building operational procedures around it
- **If the constructed key does not resolve,** the version was never published to S3, or was published under a different naming convention, and there is no recovery path for it
- For any pre-cutover version, the safest fallback is to rebuild it via routes (i) or (iii) rather than rely on this derivation

### Step 4: Fetch the artifact using the discovered key and the read credential

Once you have the key (from Step 1, or constructed via Step 3), fetch the binary and its checksum manifest from S3:

1. **Set credentials** (replace with your actual values):
   ```bash
   export AWS_ACCESS_KEY_ID="<registry-read-credential-access-key>"
   export AWS_SECRET_ACCESS_KEY="<registry-read-credential-secret-key>"
   export AWS_DEFAULT_REGION="<registry-s3-region>"  # e.g., us-east-1
   ```

2. **Download the binary** (replace `release_helper_go/v1.2.3/release_helper_go-linux-amd64` with your actual key):
   ```bash
   aws s3 cp s3://release-tools/release_helper_go/v1.2.3/release_helper_go-linux-amd64 ./release_helper_go
   chmod +x ./release_helper_go
   ```

3. **Download the checksums manifest:**
   ```bash
   aws s3 cp s3://release-tools/release_helper_go/v1.2.3/checksums.txt ./checksums.txt
   ```

4. **Verify the checksum** (using fail-closed semantics):
   ```bash
   sha256sum ./release_helper_go > computed.txt
   if grep -q "$(cut -d' ' -f1 computed.txt)" ./checksums.txt; then
     echo "Checksum verified"
   else
     echo "ERROR: Checksum mismatch - binary is corrupt or was tampered with"
     exit 1
   fi
   ```

**Result:** You now have the published binary, verified against the registry's declared checksum. It is safe to use for release operations.

---

## (iii) Manual publish-by-hand procedure

**When to use:** You have a specific build of the release tooling that needs to be published, and neither the normal CI recording path nor registry availability can be relied upon to publish it. This procedure allows you to place the binary in the object store and have the acquisition action resolve and download it afterward.

**Preconditions:**
- You have the compiled release_helper_go and/or app-registry binary (from route (i) above)
- You have write access to the registry's S3 object-store bucket (builder credential with NFR-23 grant)
- You know the version and variant (OS-arch) you are publishing
- You can execute the acquisition action (or equivalent) afterward to verify the publish

**Procedure:**

### Step 1: Determine the target version and variant

Decide what version and variant you are publishing. For example:
- Binary: `release_helper_go`
- Version: `v1.2.4`
- Variants: `linux-amd64`, `darwin-arm64`, etc.

### Step 2: Compute the checksum and prepare the checksums manifest

1. Compute the SHA256 hash of your binary:
   ```bash
   sha256sum release_helper_go > release_helper_go.sha256
   cat release_helper_go.sha256
   # Output: abc123def456...  release_helper_go
   ```

2. Create a `checksums.txt` file in the same format (space-separated hash and filename):
   ```bash
   echo "abc123def456...  release_helper_go-linux-amd64" > checksums.txt
   ```

   If publishing multiple variants, add a line for each:
   ```
   abc123...  release_helper_go-linux-amd64
   def456...  release_helper_go-darwin-arm64
   ```

### Step 3: Upload binary and checksums to S3 using the builder credential

Use the registry's S3 write credential (from DEPLOY.md or ENV.md, the builder credential with NFR-23 write grant over the allocated namespace):

```bash
export AWS_ACCESS_KEY_ID="<builder-credential-access-key>"
export AWS_SECRET_ACCESS_KEY="<builder-credential-secret-key>"
export AWS_DEFAULT_REGION="<registry-s3-region>"
export S3_BUCKET="release-tools"  # As defined in RELEASE_TOOLS_S3_BUCKET

# Upload the binary using the H8 convention
aws s3 cp release_helper_go \
  s3://${S3_BUCKET}/release_helper_go/v1.2.4/release_helper_go-linux-amd64

# Upload the checksums manifest
aws s3 cp checksums.txt \
  s3://${S3_BUCKET}/release_helper_go/v1.2.4/checksums.txt
```

**Important:** Use the H8 convention for the S3 key path: `<binary>/<version>/<binary>-<os>-<arch>` for the binary and `<binary>/<version>/checksums.txt` for the checksums.

### Step 4: Verify with the acquisition action

Once uploaded, verify the acquisition action can resolve and download it:

1. Use the `download-release-tools` GitHub action with explicit version overrides:
   ```yaml
   - uses: ./.github/actions/download-release-tools
     with:
       source: 'published'
       release_helper_version: 'v1.2.4'
       target_os: 'linux'
       target_arch: 'amd64'
   ```

   Or from the command line (outside CI):
   ```bash
   # Set up the registry credential as in step 3
   export RELEASE_TOOLS_S3_ENDPOINT="<internal S3 endpoint>"
   export RELEASE_TOOLS_S3_REGION="<region>"
   export RELEASE_TOOLS_S3_ACCESS_KEY="<credential>"
   export RELEASE_TOOLS_S3_SECRET_KEY="<secret>"
   export RELEASE_TOOLS_S3_PUBLIC_ENDPOINT="<public S3 endpoint>"
   export RELEASE_TOOLS_S3_BUCKET="release-tools"
   
   app-registry artifacts resolve \
     --owner tools-release_helper_go \
     --kind binary \
     --version v1.2.4 \
     --variant linux-amd64
   ```

2. The action or command will:
   - Call `ResolveBinaryURL` with your explicit version override
   - Retrieve the S3 key and generate a presigned download URL
   - Download the binary
   - Fetch the checksums manifest
   - Verify the binary against the checksums (fail-closed)
   - Export the binary path via `RELEASE_HELPER` environment variable

3. Confirm the binary is executable and working:
   ```bash
   ./release_helper_go --version
   ```

**Result:** The acquisition action returns the hand-published binary, confirming it is resolvable and downloadable through the normal acquisition path. You can now use this binary to continue the release process.

---

## FR-73(e) note: Understanding standing-check failures during bucket rollback

If the registry's artifact bucket is **re-opened for anonymous/public reads** (a deliberate rollback under operational pressure), the standing unsigned-fetch check (`TestFR73_UnsignedBinaryFetchIsRefused` in `tools/app_registry/conformance`) will **fail — this is expected and by design.**

The check verifies that an unsigned fetch (removing the request signature from a presigned URL) is refused. When the bucket grants public-read access:

- An unsigned GET will succeed (the bucket is readable without a signature)
- The check will detect this and fail, **correctly identifying a regression in access control**

**This is not a secondary fault or a test bug.** A red standing check during a temporary bucket re-opening is the intended outcome: it makes the rollback's consequence visible and auditable in CI logs, rather than silent. The standing check going red is proof that the security posture changed, not proof that something broke.

To recover:
1. Re-secure the bucket (remove anonymous/public read grants)
2. The standing check will return to green on the next CI run

If the check remains red after re-securing, investigate whether the bucket policy change propagated correctly or if another misconfiguration was introduced.

---

## Related

- [OPERATIONS.md](OPERATIONS.md) — Day-2 operations, promotion lifecycle, and disaster recovery for promotion rows stuck in publishing
- [DEPLOY.md](DEPLOY.md) — First-time setup, including S3 credential provisioning and bucket configuration
- [ENV.md](ENV.md) — Environment variables for the registry and its components, including S3 configuration for both API server and legacy publish-side
- [download-release-tools action](.github/actions/download-release-tools/action.yml) — The GitHub Actions integration that implements the three acquisition routes programmatically
