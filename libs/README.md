# Shared Libraries

Shared Python and Go libraries used across the monorepo.

See [TOC.md](TOC.md) for a full index of available libraries.

## Structure

```
libs/
├── python/   # Python libraries
└── go/       # Go libraries
```

## Usage

Reference libraries in your `BUILD.bazel` by their Bazel target, e.g. `//libs/python/logging` or `//libs/go/htmxauth`.
