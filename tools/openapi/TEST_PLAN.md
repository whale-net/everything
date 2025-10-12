# Test Plan: OpenAPI Domain Naming

## Objective
Verify that OpenAPI specification files are generated with domain prefixes in their filenames.

## Test Cases

### Test Case 1: Single Domain App
**Given:**
- App: `hello-fastapi`
- Domain: `demo`
- Target: `//demo/hello_fastapi:hello-fastapi_openapi_spec`

**Expected Output:**
- Bazel target generates: `demo-hello-fastapi_openapi_spec.json`
- File location: `bazel-bin/demo/hello_fastapi/demo-hello-fastapi_openapi_spec.json`

**Verification Command:**
```bash
bazel build //demo/hello_fastapi:hello-fastapi_openapi_spec
ls -la bazel-bin/demo/hello_fastapi/demo-hello-fastapi_openapi_spec.json
```

### Test Case 2: Multiple Apps Same Name, Different Domains
**Given:**
- App 1: `api` in domain `demo`
- App 2: `api` in domain `manman`

**Expected Output:**
- Domain `demo`: `demo-api_openapi_spec.json`
- Domain `manman`: `manman-api_openapi_spec.json`

**Verification:**
- No filename conflicts
- Both files can be built simultaneously

### Test Case 3: ManMan Experience API
**Given:**
- App: `experience-api`
- Domain: `manman`
- Target: `//manman:experience-api_openapi_spec`

**Expected Output:**
- File: `manman-experience-api_openapi_spec.json`
- Location: `bazel-bin/manman/manman-experience-api_openapi_spec.json`

**Verification Command:**
```bash
bazel build //manman:experience-api_openapi_spec
ls -la bazel-bin/manman/manman-experience-api_openapi_spec.json
```

### Test Case 4: OpenAPI Client Generation Still Works
**Given:**
- Client target: `//manman:experience_api_client`
- Spec reference: `:experience_api_spec` (alias to `:experience-api_openapi_spec`)

**Expected Behavior:**
- Client generation succeeds
- Uses Bazel label resolution (filename is irrelevant)

**Verification Command:**
```bash
bazel build //manman:experience_api_client
# Should succeed without errors
```

### Test Case 5: GitHub Workflow Integration
**Given:**
- Matrix app: `experience-api`
- Matrix domain: `manman`
- OpenAPI target: `//manman:experience-api_openapi_spec`

**Expected Workflow Behavior:**
1. Builds target successfully
2. Finds file at: `bazel-bin/manman/manman-experience-api_openapi_spec.json`
3. Copies to: `/tmp/openapi-specs/manman-experience-api-openapi.json`
4. Uploads artifact successfully

**Verification:**
- Check GitHub Actions workflow logs
- Verify artifact contains correct filename

## Edge Cases

### Edge Case 1: No Domain Provided (Backward Compatibility)
**Given:**
- `openapi_spec` called without `domain` parameter

**Expected:**
- Falls back to old format: `{name}.json`

### Edge Case 2: Domain with Special Characters
**Given:**
- Domain: `my-domain`
- App: `my-app`

**Expected:**
- File: `my-domain-my-app_openapi_spec.json`
- No issues with special characters

## Success Criteria
- ✅ All apps with OpenAPI specs generate unique filenames
- ✅ No filename conflicts in multi-domain builds
- ✅ OpenAPI client generation continues to work
- ✅ GitHub workflows handle new format correctly
- ✅ Backward compatibility maintained (fallback logic works)

## Manual Testing Steps

1. **Build demo app spec:**
   ```bash
   bazel build //demo/hello_fastapi:hello-fastapi_openapi_spec
   ```

2. **Build manman specs:**
   ```bash
   bazel build //manman:experience-api_openapi_spec
   bazel build //manman:status-api_openapi_spec
   bazel build //manman:worker-dal-api_openapi_spec
   ```

3. **Verify filenames:**
   ```bash
   find bazel-bin -name "*_openapi_spec.json"
   ```

4. **Build clients:**
   ```bash
   bazel build //manman:experience_api_client
   bazel build //tools/client_codegen:demo_hello_fastapi
   ```

5. **Check for no conflicts:**
   ```bash
   # Should show unique files with domain prefixes
   find bazel-bin -name "*_openapi_spec.json" | sort
   ```

## Automated Testing
Since there's no existing test infrastructure for OpenAPI generation rules, manual verification is recommended. Future work could include:
- Integration tests for OpenAPI spec generation
- Workflow simulation tests
- Filename uniqueness validation
