# krill UI wireframes

Clickable workspace-shell wireframe for the operator UI facelift. Start at
`screens/00-brief.html` (audience, object tree, archetype); the other screens
follow it by filename order.

```
bazel run //tools/wireframe -- --dir krill/ui/design/wireframes --title "krill"
open krill/ui/design/wireframes/preview.html   # gitignored, needs network for CDN styles
```

Use the floating "theme" button to check light, night and oled, and narrow the
window to ~390px to check the board and the drawer.
