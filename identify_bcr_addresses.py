#!/usr/bin/env python3
"""
Script to identify and test BCR (Bazel Central Registry) addresses that may be blocked by firewall.
"""

import json
import re
import subprocess
import urllib.request
import urllib.error
from typing import Set, List, Dict
import sys
from pathlib import Path

def extract_bcr_addresses_from_lock() -> Set[str]:
    """Extract all BCR addresses from MODULE.bazel.lock file."""
    bcr_addresses = set()
    
    lock_file = Path("MODULE.bazel.lock")
    if not lock_file.exists():
        print("Error: MODULE.bazel.lock not found")
        return bcr_addresses
    
    with open(lock_file, 'r') as f:
        lock_data = json.load(f)
    
    # Extract from registryFileHashes
    if "registryFileHashes" in lock_data:
        for url in lock_data["registryFileHashes"].keys():
            if "bcr.bazel.build" in url:
                # Extract base URL
                base_url = re.match(r"https://bcr\.bazel\.build", url)
                if base_url:
                    bcr_addresses.add(base_url.group(0))
                # Also add the full URL pattern
                bcr_addresses.add(url)
    
    return bcr_addresses

def extract_primary_bcr_addresses() -> Set[str]:
    """Extract primary BCR addresses that Bazel typically needs."""
    primary_addresses = {
        "https://bcr.bazel.build",
        "https://bcr.bazel.build/bazel_registry.json",
        "https://registry-1.docker.io",  # Docker Hub for container images
        "https://index.docker.io",      # Docker index
        "https://github.com",           # GitHub for source downloads
    }
    
    return primary_addresses

def test_connectivity(urls: Set[str]) -> Dict[str, str]:
    """Test connectivity to URLs and return status."""
    results = {}
    
    for url in sorted(urls):
        try:
            # Set a timeout to avoid hanging
            req = urllib.request.Request(url, headers={'User-Agent': 'Mozilla/5.0'})
            with urllib.request.urlopen(req, timeout=10) as response:
                if response.status == 200:
                    results[url] = "✅ ACCESSIBLE"
                else:
                    results[url] = f"⚠️  HTTP {response.status}"
        except urllib.error.HTTPError as e:
            results[url] = f"❌ HTTP ERROR {e.code}"
        except urllib.error.URLError as e:
            results[url] = f"❌ NETWORK ERROR: {e.reason}"
        except Exception as e:
            results[url] = f"❌ ERROR: {str(e)}"
    
    return results

def test_with_curl(urls: Set[str]) -> Dict[str, str]:
    """Test connectivity using curl command for more detailed network info."""
    results = {}
    
    for url in sorted(urls):
        try:
            # Use curl with timeout and fail options
            result = subprocess.run([
                'curl', '-s', '--fail', '--max-time', '10', 
                '--connect-timeout', '5', url
            ], capture_output=True, text=True)
            
            if result.returncode == 0:
                results[url] = "✅ ACCESSIBLE (curl)"
            else:
                # Get more specific error
                error_result = subprocess.run([
                    'curl', '-s', '--max-time', '10', 
                    '--connect-timeout', '5', '-I', url
                ], capture_output=True, text=True)
                
                if "Could not resolve host" in error_result.stderr:
                    results[url] = "❌ DNS RESOLUTION FAILED"
                elif "Connection timed out" in error_result.stderr:
                    results[url] = "❌ CONNECTION TIMEOUT"
                elif "Connection refused" in error_result.stderr:
                    results[url] = "❌ CONNECTION REFUSED"
                else:
                    results[url] = f"❌ CURL ERROR (exit {result.returncode})"
        except FileNotFoundError:
            results[url] = "⚠️  curl not available"
        except Exception as e:
            results[url] = f"❌ ERROR: {str(e)}"
    
    return results

def analyze_bazel_dependencies() -> Set[str]:
    """Analyze MODULE.bazel to identify potential external dependencies."""
    external_urls = set()
    
    module_file = Path("MODULE.bazel")
    if module_file.exists():
        with open(module_file, 'r') as f:
            content = f.read()
            
        # Look for potential external registry URLs or patterns
        # Bazel modules often reference external sources
        
        # Add known patterns that Bazel uses
        external_urls.update([
            "https://bcr.bazel.build",
            "https://github.com/bazelbuild",
            "https://registry-1.docker.io",
            "https://index.docker.io"
        ])
    
    return external_urls

def main():
    print("🔍 BCR Address Firewall Analysis")
    print("=" * 50)
    
    # Extract addresses from various sources
    print("\n📋 Extracting BCR addresses from MODULE.bazel.lock...")
    lock_addresses = extract_bcr_addresses_from_lock()
    print(f"Found {len(lock_addresses)} addresses in lock file")
    
    print("\n📋 Identifying primary BCR addresses...")
    primary_addresses = extract_primary_bcr_addresses()
    
    print("\n📋 Analyzing Bazel dependencies...")
    dep_addresses = analyze_bazel_dependencies()
    
    # Combine all addresses
    all_addresses = lock_addresses | primary_addresses | dep_addresses
    
    # Filter to unique base domains for initial testing
    base_domains = set()
    for addr in all_addresses:
        if addr.startswith("https://bcr.bazel.build"):
            base_domains.add("https://bcr.bazel.build")
        elif addr.startswith("https://registry-1.docker.io"):
            base_domains.add("https://registry-1.docker.io")
        elif addr.startswith("https://index.docker.io"):
            base_domains.add("https://index.docker.io")
        elif addr.startswith("https://github.com"):
            base_domains.add("https://github.com")
        else:
            base_domains.add(addr)
    
    print(f"\n🌐 Testing connectivity to {len(base_domains)} unique base domains...")
    print("\nTesting with Python urllib:")
    urllib_results = test_connectivity(base_domains)
    
    print("\nTesting with curl:")
    curl_results = test_with_curl(base_domains)
    
    # Display results
    print("\n" + "=" * 50)
    print("📊 CONNECTIVITY RESULTS")
    print("=" * 50)
    
    blocked_addresses = []
    accessible_addresses = []
    
    for url in sorted(base_domains):
        urllib_status = urllib_results.get(url, "Unknown")
        curl_status = curl_results.get(url, "Unknown")
        
        print(f"\n🔗 {url}")
        print(f"   urllib: {urllib_status}")
        print(f"   curl:   {curl_status}")
        
        # Determine if blocked
        if "❌" in urllib_status or "❌" in curl_status:
            blocked_addresses.append(url)
        elif "✅" in urllib_status or "✅" in curl_status:
            accessible_addresses.append(url)
    
    # Summary
    print("\n" + "=" * 50)
    print("📋 SUMMARY")
    print("=" * 50)
    
    print(f"\n✅ ACCESSIBLE ({len(accessible_addresses)}):")
    for addr in accessible_addresses:
        print(f"  - {addr}")
    
    print(f"\n❌ BLOCKED/PROBLEMATIC ({len(blocked_addresses)}):")
    for addr in blocked_addresses:
        print(f"  - {addr}")
    
    # Specific BCR analysis
    bcr_blocked = [addr for addr in blocked_addresses if "bcr.bazel.build" in addr]
    if bcr_blocked:
        print(f"\n🚨 CRITICAL: BCR addresses are blocked!")
        print("   This will prevent Bazel builds from working.")
        print("   Firewall needs to allow access to:")
        for addr in bcr_blocked:
            print(f"     - {addr}")
    
    # Generate firewall rules recommendation
    print("\n" + "=" * 50)
    print("🔧 FIREWALL CONFIGURATION RECOMMENDATIONS")
    print("=" * 50)
    
    print("\nDomains that need firewall access:")
    domains_needed = set()
    for addr in all_addresses:
        if "bcr.bazel.build" in addr:
            domains_needed.add("bcr.bazel.build")
        elif "docker.io" in addr:
            domains_needed.add("*.docker.io")
        elif "github.com" in addr:
            domains_needed.add("github.com")
    
    for domain in sorted(domains_needed):
        print(f"  - {domain}")
    
    print("\nSpecific URLs that need access:")
    critical_urls = [
        "https://bcr.bazel.build",
        "https://bcr.bazel.build/bazel_registry.json",
        "https://registry-1.docker.io",
        "https://index.docker.io",
        "https://github.com"
    ]
    
    for url in critical_urls:
        status = "❌ BLOCKED" if url in blocked_addresses else "✅ OK"
        print(f"  - {url} {status}")

if __name__ == "__main__":
    main()