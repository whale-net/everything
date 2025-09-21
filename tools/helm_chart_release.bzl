"""Forwarding file for helm_chart_release.bzl - moved to //tools/helm."""

load("//tools/helm:helm_chart_release.bzl", "helm_chart_release", "helm_chart_release_macro")

# Re-export everything from the new location
helm_chart_release = helm_chart_release
helm_chart_release_macro = helm_chart_release_macro
