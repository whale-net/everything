#!/usr/bin/env python3
"""
Extract complete list of BCR URLs from MODULE.bazel.lock
"""

import json
from pathlib import Path

def extract_all_bcr_urls():
    """Extract all BCR URLs from lock file and organize them."""
    
    lock_file = Path("MODULE.bazel.lock")
    if not lock_file.exists():
        print("Error: MODULE.bazel.lock not found")
        return
    
    with open(lock_file, 'r') as f:
        lock_data = json.load(f)
    
    # Extract all BCR URLs
    bcr_urls = []
    other_urls = []
    
    if "registryFileHashes" in lock_data:
        for url in lock_data["registryFileHashes"].keys():
            if "bcr.bazel.build" in url:
                bcr_urls.append(url)
            else:
                other_urls.append(url)
    
    # Sort URLs
    bcr_urls.sort()
    other_urls.sort()
    
    # Organize by type
    registry_urls = [url for url in bcr_urls if "bazel_registry.json" in url]
    module_urls = [url for url in bcr_urls if "/modules/" in url and url.endswith("MODULE.bazel")]
    source_urls = [url for url in bcr_urls if "/modules/" in url and url.endswith("source.json")]
    
    # Generate the comprehensive list
    with open("BCR_URLS_COMPLETE_LIST.md", "w") as f:
        f.write("# Complete List of BCR URLs Used by whale-net/everything\n\n")
        f.write("This document contains the complete list of Bazel Central Registry URLs that are referenced by this repository's `MODULE.bazel.lock` file.\n\n")
        f.write("**Total BCR URLs**: {}\n\n".format(len(bcr_urls)))
        
        f.write("## Summary\n\n")
        f.write("- Registry metadata URLs: {}\n".format(len(registry_urls)))
        f.write("- Module definition URLs: {}\n".format(len(module_urls)))
        f.write("- Source information URLs: {}\n".format(len(source_urls)))
        f.write("- Other BCR URLs: {}\n\n".format(len(bcr_urls) - len(registry_urls) - len(module_urls) - len(source_urls)))
        
        if registry_urls:
            f.write("## Registry Metadata URLs\n\n")
            f.write("These URLs provide registry configuration:\n\n")
            for url in registry_urls:
                f.write(f"- {url}\n")
            f.write("\n")
        
        f.write("## Module Definition URLs\n\n")
        f.write("These URLs provide module definitions (MODULE.bazel files):\n\n")
        for url in module_urls:
            f.write(f"- {url}\n")
        f.write("\n")
        
        if source_urls:
            f.write("## Source Information URLs\n\n")
            f.write("These URLs provide source archive information:\n\n")
            for url in source_urls:
                f.write(f"- {url}\n")
            f.write("\n")
        
        f.write("## Domain Summary\n\n")
        f.write("All URLs use the domain: `bcr.bazel.build`\n\n")
        f.write("**Firewall requirement**: Allow HTTPS access to `bcr.bazel.build` (port 443)\n\n")
        
        f.write("## Module Dependencies\n\n")
        f.write("The following Bazel modules are referenced:\n\n")
        
        # Extract unique modules
        modules = set()
        for url in module_urls:
            parts = url.split("/modules/")[1].split("/")
            if len(parts) >= 2:
                module_name = parts[0]
                version = parts[1]
                modules.add((module_name, version))
        
        for module_name, version in sorted(modules):
            f.write(f"- {module_name} (v{version})\n")
        
        f.write("\n## Complete URL List\n\n")
        f.write("### All BCR URLs ({})\n\n".format(len(bcr_urls)))
        f.write("```\n")
        for url in bcr_urls:
            f.write(f"{url}\n")
        f.write("```\n\n")
        
        if other_urls:
            f.write("### Non-BCR URLs ({})\n\n".format(len(other_urls)))
            f.write("These URLs are also referenced but are not BCR addresses:\n\n")
            f.write("```\n")
            for url in other_urls:
                f.write(f"{url}\n")
            f.write("```\n")
    
    print(f"✅ Generated BCR_URLS_COMPLETE_LIST.md with {len(bcr_urls)} BCR URLs")
    print(f"📊 Breakdown:")
    print(f"  - Registry URLs: {len(registry_urls)}")
    print(f"  - Module URLs: {len(module_urls)}")
    print(f"  - Source URLs: {len(source_urls)}")
    print(f"  - Unique modules: {len(set(url.split('/modules/')[1].split('/')[0] for url in module_urls if '/modules/' in url))}")

if __name__ == "__main__":
    extract_all_bcr_urls()