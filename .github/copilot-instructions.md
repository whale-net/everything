# GitHub Copilot Instructions for Everything Monorepo

## Primary Reference Document
**ALWAYS consult `AGENT.md` first** for comprehensive guidelines when working on this monorepo. The AGENT.md file contains detailed instructions for AI agents and provides the authoritative framework for understanding and working with this codebase.

## Copilot-Specific Guidelines

### Code Suggestions
When providing code suggestions:
- Follow existing patterns in the repository
- Use the `release_app` macro for any new applications
- Maintain consistency with existing `BUILD.bazel` file structures
- Suggest appropriate dependencies from `//libs/python` or `//libs/go`

### File Generation
When creating new files:
- Use existing files as templates when possible
- Follow naming conventions established in the repository
- Include appropriate Bazel targets in `BUILD.bazel` files
- Reference shared libraries correctly

### Documentation
When suggesting documentation changes:
- Keep documentation updates minimal and focused
- Refer users to `AGENT.md` for comprehensive information
- Maintain consistency with existing documentation style

## Quick Reference
For all detailed information about:
- Repository architecture and patterns → See `AGENT.md` sections 🚀 and 🛠️
- Development workflows and procedures → See `AGENT.md` sections 🔄 and 🔍
- Release management and troubleshooting → See `AGENT.md` sections 🧪 and 🚨

The `AGENT.md` file contains comprehensive guidelines that should inform all code suggestions and development assistance.