# Component Usage Guide

## Base UI Components

### Button
```templ
@ui.Button(types.ButtonProps{Variant: types.ButtonPrimary}) {
    Click Me
}
```

Variants: `ButtonPrimary`, `ButtonSecondary`, `ButtonDanger`, `ButtonSuccess`

### Badge
```templ
@ui.Badge(types.BadgeProps{Variant: types.BadgeSuccess}) {
    Active
}
```

Variants: `BadgeDefault`, `BadgeSuccess`, `BadgeDanger`, `BadgeWarning`, `BadgeInfo`

### Card
```templ
@ui.Card() {
    @ui.CardBody() {
        <p>Content</p>
    }
}
```

### Alert
```templ
@ui.Alert(types.AlertProps{Variant: types.AlertInfo}) {
    Information message
}
```

Variants: `AlertInfo`, `AlertSuccess`, `AlertWarning`, `AlertDanger`

## Layout Components

### Base Layout
```templ
@layout.Base(data.Layout) {
    <!-- page content -->
}
```

### Hero
```templ
@layout.Hero("Page Title", "Subtitle text")
```

### Breadcrumbs
```templ
@layout.Breadcrumbs([]layout.BreadcrumbItem{
    {Label: "Home", Href: "/"},
    {Label: "Current", Href: ""},
})
```

## Form Components

### Input
```templ
@forms.Input("field_name", "value", "Placeholder")
```

### FormField
```templ
@forms.FormField("field_name", "Label", "error message") {
    @forms.Input("field_name", "", "Enter value")
}
```

### FormActions
```templ
@forms.FormActions("Save", "/cancel-url")
```

## Domain Components

### GameCard
```templ
@domain.GameCard(game)
```

### DeploymentTable
```templ
@domain.DeploymentTable(deployments)
```

## Alpine.js Patterns

### Collapsible Section
```html
<div x-data="{ open: false }">
    <button @click="open = !open">
        <svg :class="{ 'rotate-180': open }">...</svg>
    </button>
    <div x-show="open" x-collapse>
        Content
    </div>
</div>
```
