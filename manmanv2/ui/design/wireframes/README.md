# ManManV2 UI Wireframes

Design-iteration wireframes for the HTMX UI redesign. Not production code —
fake data, static markup, daisyUI classes.

```
bazel run //tools/wireframe -- --dir manmanv2/ui/design/wireframes --title "ManMan Wireframes"
open manmanv2/ui/design/wireframes/preview.html
```

`preview.html` is generated and gitignored — never edit or commit it; edit the
fragments in `screens/` (and `_shell.html` for shared chrome) and re-run the
assembler. Workflow and guardrails: `.claude/skills/wireframe/SKILL.md`.
Kit docs: `tools/wireframe/README.md`.
