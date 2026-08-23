# htmxui

Shared UI primitives (chrome, themes, reusable components) for HTMX-based
applications in this monorepo. This package holds only cross-app primitives
— never app-specific screens. App-specific UI stays in each consuming app's
own package (e.g. `tools/app_registry/ui`, `manmanv2/ui`).

This is scaffold-only groundwork (part of #998, FR1): the actual primitives
and the shared `themes.css` land in sibling Phase 1 issues. Right now the
package exists only so those issues have somewhere to add files.

## BUILD shape: `templ_library`, not `go_library`

`libs/go/htmxui` is a **`templ_library`**, loaded from `//tools:templ.bzl`
— the first `templ_library` under `libs/go/`. This is deliberate and
diverges from its directory siblings `libs/go/htmxauth` and
`libs/go/htmxbase`, which are plain `go_library` rules because they contain
no `.templ` sources.

```python
load("//tools:templ.bzl", "templ_library")

templ_library(
    name = "htmxui",
    srcs = glob(["*.templ"]),
    go_srcs = ["doc.go"],       # non-generated .go files
    visibility = ["//visibility:public"],
    deps = [],
)
```

`templ_library` (defined in `tools/templ.bzl`) genrules each `.templ` file
into a `<name>_templ.go` file via the `templ` CLI, then wraps the generated
files plus `go_srcs` in a `go_library`. The `importpath` is derived
automatically from `native.package_name()`, so this package's Go import
path is `github.com/whale-net/everything/libs/go/htmxui`.

Because this shape is hand-written (not gazelle-generated) and the rule is
named `templ_library` rather than `go_library`, gazelle would otherwise try
to rewrite or duplicate it. The `# keep` marker in `BUILD.bazel` on the
`templ_library(...)` rule tells `bazel run //:gazelle` to leave it alone.

## Depending on this package

Add `//libs/go/htmxui` to your target's `deps`. Visibility is
`//visibility:public` (FR1, user story 10) so any UI — `//tools/wireframe`,
`//tools/app_registry/ui/...`, `//manmanv2/ui/...`, and any future third UI
— can depend on it directly:

```python
templ_library(
    name = "my_ui",
    srcs = glob(["*.templ"]),
    deps = [
        "//libs/go/htmxui",
        # ...
    ],
)
```

## No bundler assets (NFR1)

This package ships no Node/npm/bundler files and introduces no CSS build
toolchain — there is no `package.json` here, and there won't be one.
Tailwind and daisyUI arrive at the browser via each consuming app's own
pinned CDN `<link>`/`<script>` tags; `htmxui` supplies Go/templ code that
assumes those utility classes exist, not the CSS itself.
