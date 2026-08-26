#!/usr/bin/env bash
# Tests for push-config.sh script

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PUSH_CONFIG_SCRIPT="$SCRIPT_DIR/push-config.sh"

# Test 1: Script exists and is executable
test_script_exists() {
    if [[ ! -f "$PUSH_CONFIG_SCRIPT" ]]; then
        echo "FAIL: push-config.sh not found at $PUSH_CONFIG_SCRIPT"
        return 1
    fi
    if [[ ! -x "$PUSH_CONFIG_SCRIPT" ]]; then
        echo "FAIL: push-config.sh is not executable"
        return 1
    fi
    echo "PASS: Script exists and is executable"
    return 0
}

# Test 2: Script syntax is valid (bash -n check)
test_script_syntax() {
    if bash -n "$PUSH_CONFIG_SCRIPT" 2>/dev/null; then
        echo "PASS: Script syntax is valid"
        return 0
    else
        echo "FAIL: Script has syntax errors"
        return 1
    fi
}

# Test 3: Script does not use server reflection
test_no_reflection_usage() {
    # Check that script uses -protoset flag (descriptor-based), not reflection
    if grep -q "\-protoset" "$PUSH_CONFIG_SCRIPT"; then
        echo "PASS: Script uses descriptor set, not reflection"
        return 0
    else
        echo "FAIL: Script doesn't use -protoset flag for descriptor"
        return 1
    fi
}

# Test 4: Script uses grpcurl with -plaintext for unauthenticated descriptor transfer
test_uses_grpcurl_plaintext() {
    if grep -q "grpcurl.*-plaintext" "$PUSH_CONFIG_SCRIPT"; then
        echo "PASS: Script uses grpcurl with plaintext for unauthenticated access"
        return 0
    else
        echo "FAIL: Script doesn't use grpcurl with -plaintext"
        return 1
    fi
}

# Test 5: Script exits with non-zero when descriptor not found
test_descriptor_not_found_exit_code() {
    export LEAFLAB_API_DESCRIPTOR_PATH="/nonexistent/descriptor.pb"
    
    "$PUSH_CONFIG_SCRIPT" device-1 single-light > /dev/null 2>&1
    exit_code=$?
    
    if [[ $exit_code -ne 0 ]]; then
        echo "PASS: Script exits with error when descriptor not found (exit code: $exit_code)"
        return 0
    else
        echo "FAIL: Script should exit non-zero when descriptor not found"
        return 1
    fi
}

# Test 6: Script --list command is supported
test_list_flag_supported() {
    # Check that the script has --list flag support in code
    if grep -q '"\$SCENARIO" == "--list"' "$PUSH_CONFIG_SCRIPT"; then
        echo "PASS: Script supports --list flag"
        return 0
    else
        echo "FAIL: Script doesn't support --list flag"
        return 1
    fi
}

# Test 7: Script requires device ID
test_requires_device_id() {
    output=$("$PUSH_CONFIG_SCRIPT" 2>&1) || true
    
    if echo "$output" | grep -qi "usage"; then
        echo "PASS: Script requires device ID and shows usage"
        return 0
    else
        echo "FAIL: Script doesn't properly handle missing device ID"
        return 1
    fi
}

# Test 8: Token cache permissions are restrictive (0600)
test_token_cache_permissions() {
    # Verify the script creates cache with 0600 perms (from code inspection)
    if grep -q "chmod 600" "$PUSH_CONFIG_SCRIPT"; then
        echo "PASS: Script sets restrictive token cache permissions (0600)"
        return 0
    else
        echo "FAIL: Script doesn't enforce restrictive token cache permissions"
        return 1
    fi
}

# Test 9: Device auth failure messages are clear
test_device_auth_failure_messages() {
    # Verify the script has clear error messages for various device auth failures
    
    # Check for key error messages
    if ! grep -qi "failed to get device code" "$PUSH_CONFIG_SCRIPT"; then
        echo "FAIL: Missing error message for device code failure"
        return 1
    fi
    
    if ! grep -qi "device code expired" "$PUSH_CONFIG_SCRIPT"; then
        echo "FAIL: Missing error message for device code expiration"
        return 1
    fi
    
    if ! grep -qi "refresh token invalid or expired" "$PUSH_CONFIG_SCRIPT"; then
        echo "FAIL: Missing error message for token expiration"
        return 1
    fi
    
    echo "PASS: Script has clear device auth failure messages"
    return 0
}

# Test 10: Script uses device authorization grant flow
test_device_auth_grant_flow() {
    if grep -q "device_authorization_grant" "$PUSH_CONFIG_SCRIPT"; then
        echo "PASS: Script uses device authorization grant flow"
        return 0
    else
        echo "FAIL: Script doesn't use device authorization grant flow"
        return 1
    fi
}

# Test 11: Script supports token refresh
test_token_refresh_support() {
    if grep -q "refresh_token_internal" "$PUSH_CONFIG_SCRIPT"; then
        echo "PASS: Script supports token refresh"
        return 0
    else
        echo "FAIL: Script doesn't support token refresh"
        return 1
    fi
}

# Test 12: Script uses Bearer token for authentication
test_bearer_token_auth() {
    if grep -q "Bearer" "$PUSH_CONFIG_SCRIPT"; then
        echo "PASS: Script uses Bearer token for authentication"
        return 0
    else
        echo "FAIL: Script doesn't use Bearer token"
        return 1
    fi
}

# Run all tests
main() {
    local passed=0
    local failed=0
    
    for test in test_script_exists \
                test_script_syntax \
                test_no_reflection_usage \
                test_uses_grpcurl_plaintext \
                test_descriptor_not_found_exit_code \
                test_list_flag_supported \
                test_requires_device_id \
                test_token_cache_permissions \
                test_device_auth_failure_messages \
                test_device_auth_grant_flow \
                test_token_refresh_support \
                test_bearer_token_auth; do
        
        echo "Running: $test"
        if $test; then
            ((passed++))
        else
            ((failed++))
        fi
        echo ""
    done
    
    echo "========================================"
    echo "Results: $passed passed, $failed failed"
    echo "========================================"
    
    if [[ $failed -gt 0 ]]; then
        exit 1
    fi
    
    exit 0
}

main
