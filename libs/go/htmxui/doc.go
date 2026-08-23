// Package htmxui is the shared UI primitives library for HTMX-based
// applications in this monorepo. It holds only chrome and primitives that
// are common across apps — layout scaffolding, themes, and reusable
// components (buttons, badges, form controls, and similar) — never
// app-specific screens or business logic. App-specific UI stays in each
// app's own package (e.g. tools/app_registry/ui, manmanv2/ui).
//
// # BUILD shape
//
// Unlike its directory siblings libs/go/htmxauth and libs/go/htmxbase
// (plain go_library rules), this package is built with templ_library from
// //tools:templ.bzl, because its primitives are authored as .templ files
// compiled to Go. See libs/go/htmxui/BUILD.bazel and
// tools/app_registry/ui/components/BUILD.bazel for the pattern.
//
// # Delivery model (NFR1)
//
// This package ships no Node/npm/bundler assets and introduces no CSS
// build toolchain. Tailwind and daisyUI are delivered to the browser via
// each consuming app's own pinned CDN <link>/<script> tags — htmxui
// supplies the Go/templ code that assumes those classes exist, not the
// CSS itself.
package htmxui
