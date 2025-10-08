#!/usr/bin/env python3
"""Benchmark different OCI layering strategies.

Compares:
1. Single layer (baseline)
2. Two layers (deps + app) - current implementation
3. Per-package layers (interpreter + each package + app) - experimental

Measures:
- Initial build time (clean build)
- Incremental build time (change app code)
- Tar creation overhead
"""

import subprocess
import time
from pathlib import Path
import json


def run_bazel_build(target: str, clean: bool = False) -> tuple[float, str]:
    """Run bazel build and measure time.
    
    Returns:
        (elapsed_seconds, output)
    """
    if clean:
        subprocess.run(["bazel", "clean"], check=True, capture_output=True)
    
    start = time.time()
    result = subprocess.run(
        ["bazel", "build", target, "--platforms=//tools:linux_arm64"],
        capture_output=True,
        text=True,
    )
    elapsed = time.time() - start
    
    return elapsed, result.stderr


def modify_app_code(file_path: Path, comment: str):
    """Add a comment to app code to trigger rebuild."""
    with open(file_path, "a") as f:
        f.write(f"\n# {comment}\n")


def restore_app_code(file_path: Path):
    """Restore app code to original state."""
    subprocess.run(["git", "checkout", str(file_path)], check=True)


def benchmark_scenario(name: str, target: str, app_file: Path) -> dict:
    """Benchmark a layering scenario.
    
    Returns:
        Dict with timing results
    """
    print(f"\n{'='*60}")
    print(f"Benchmarking: {name}")
    print(f"{'='*60}")
    
    # Clean build
    print("Running clean build...")
    clean_time, _ = run_bazel_build(target, clean=True)
    print(f"  Clean build: {clean_time:.2f}s")
    
    # Incremental builds (5 iterations for average)
    incremental_times = []
    for i in range(5):
        print(f"Running incremental build {i+1}/5...")
        modify_app_code(app_file, f"Benchmark iteration {i+1}")
        incr_time, _ = run_bazel_build(target, clean=False)
        incremental_times.append(incr_time)
        print(f"  Incremental build {i+1}: {incr_time:.2f}s")
    
    # Restore
    restore_app_code(app_file)
    
    avg_incremental = sum(incremental_times) / len(incremental_times)
    
    return {
        "scenario": name,
        "clean_build_time": clean_time,
        "incremental_build_times": incremental_times,
        "avg_incremental_time": avg_incremental,
    }


def main():
    """Run benchmarks and report results."""
    app_file = Path("demo/hello_fastapi/main.py")
    
    results = []
    
    # Benchmark current two-layer approach
    results.append(benchmark_scenario(
        "Two-layer (current)",
        "//demo/hello_fastapi:hello_fastapi_image_base",
        app_file,
    ))
    
    # TODO: Add single-layer benchmark (need to create target without app_layer)
    # TODO: Add per-package benchmark (once implemented)
    
    # Print summary
    print("\n" + "="*60)
    print("BENCHMARK RESULTS")
    print("="*60)
    print(json.dumps(results, indent=2))
    
    # Analysis
    print("\n" + "="*60)
    print("ANALYSIS")
    print("="*60)
    
    for result in results:
        print(f"\n{result['scenario']}:")
        print(f"  Clean build: {result['clean_build_time']:.2f}s")
        print(f"  Avg incremental: {result['avg_incremental_time']:.2f}s")
        print(f"  Incremental range: {min(result['incremental_build_times']):.2f}s - {max(result['incremental_build_times']):.2f}s")


if __name__ == "__main__":
    main()
