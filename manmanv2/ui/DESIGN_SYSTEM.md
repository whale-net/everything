# ManMan V2 Design System

**Professional + Gaming Aesthetic**  
A cohesive design language combining clean business-focused UI with vibrant, energetic accents.

---

## Color Palette

### Primary Colors

| Color | Usage | Tailwind Classes | Hex |
|-------|-------|------------------|-----|
| **Indigo** | Main actions, links, focus states | `indigo-600/700` | #4f46e5 / #4338ca |
| **Success (Green)** | Confirmations, success states | `green-600/700` | #16a34a / #15803d |
| **Danger (Red)** | Destructive actions, errors | `red-600/700` | #dc2626 / #b91c1c |
| **Warning (Yellow)** | Warnings, pending states | `yellow-500/600` | #eab308 / #ca8a04 |
| **Neutral (Slate)** | Secondary actions, text, borders | `slate-600/700/800` | #475569 / #334155 / #1e293b |

### Gradient (Hero Headers)

```css
background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
/* Tailwind: bg-gradient-to-br from-indigo-600 to-purple-600 */
```

---

## Component Patterns

### Hero Headers

**Usage**: Main section pages (Games, Servers, Sessions, Workshop)  
**Do NOT use on**: Detail pages (use breadcrumbs instead)

```html
<div class="bg-gradient-to-br from-indigo-600 to-purple-600 rounded-lg p-6 md:p-8 mb-6 shadow-lg">
    <div class="flex flex-col md:flex-row justify-between items-start md:items-center gap-4">
        <div class="text-white">
            <h1 class="text-2xl md:text-3xl font-bold mb-2">Page Title</h1>
            <p class="text-indigo-100 text-sm">Page description</p>
        </div>
        <div class="flex gap-2">
            <!-- Ghost button (on gradient) -->
            <button class="inline-flex items-center justify-center px-4 py-2 min-h-[44px] bg-white/20 hover:bg-white/30 text-white border border-white/30 font-medium rounded-md transition-colors">
                Secondary
            </button>
            <!-- Solid white button (primary CTA) -->
            <button class="inline-flex items-center justify-center px-4 py-2 min-h-[44px] bg-white hover:bg-gray-50 text-indigo-600 font-semibold rounded-md transition-colors shadow-md">
                + Create New
            </button>
        </div>
    </div>
</div>
```

---

### Buttons

#### Primary (Indigo)
**Usage**: Create, Edit, View, Manage, Configure

```html
<button class="inline-flex items-center justify-center px-4 py-2 min-h-[44px] bg-indigo-600 hover:bg-indigo-700 text-white font-medium rounded-md transition-colors">
    Primary Action
</button>
```

#### Success (Green)
**Usage**: Start, Deploy, Save, Confirm, Enable

```html
<button class="inline-flex items-center justify-center px-4 py-2 min-h-[44px] bg-green-600 hover:bg-green-700 text-white font-medium rounded-md transition-colors">
    Start / Save
</button>
```

#### Danger (Red)
**Usage**: Delete, Stop, Force, Remove, Disable

```html
<button class="inline-flex items-center justify-center px-4 py-2 min-h-[44px] bg-red-600 hover:bg-red-700 text-white font-medium rounded-md transition-colors">
    Delete / Stop
</button>
```

#### Secondary (Slate)
**Usage**: Cancel, Back, Close, Dismiss

```html
<button class="inline-flex items-center justify-center px-4 py-2 min-h-[44px] bg-slate-600 hover:bg-slate-700 text-white font-medium rounded-md transition-colors">
    Cancel / Back
</button>
```

#### Small Button
**Usage**: Inline actions, table actions (min 36px)

```html
<button class="inline-flex items-center justify-center px-3 py-1.5 min-h-[36px] bg-indigo-600 hover:bg-indigo-700 text-white text-sm font-medium rounded-md transition-colors">
    Small Action
</button>
```

---

### Badges

#### Status Badge Colors

| Status | Color | Classes |
|--------|-------|---------|
| Active, Running, Online | Green | `bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200` |
| Pending, Starting, Stopping | Yellow | `bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-200` |
| Error, Crashed, Failed | Red | `bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200` |
| Info, Deployed | Indigo | `bg-indigo-100 text-indigo-800 dark:bg-indigo-900 dark:text-indigo-200` |
| Inactive, Stopped, Offline | Slate | `bg-slate-100 text-slate-800 dark:bg-slate-700 dark:text-slate-300` |

```html
<span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200">
    Active
</span>
```

---

### Danger Zones

**Usage**: Delete game, Delete config, Delete SGC, Stop session  
**Location**: Always at bottom of page  
**Pattern**: Red-bordered section with inline confirmation

```html
<div x-data="{ confirmDelete: false }" class="bg-white dark:bg-slate-800 rounded-lg shadow-md border-2 border-red-200 dark:border-red-900 overflow-hidden mt-8">
    <div class="bg-red-50 dark:bg-red-900/20 px-6 py-4 border-b-2 border-red-200 dark:border-red-900">
        <h3 class="text-lg font-semibold text-red-900 dark:text-red-200">⚠️ Danger Zone</h3>
    </div>
    <div class="p-6">
        <div class="mb-4">
            <h4 class="text-base font-semibold text-slate-900 dark:text-white mb-2">Delete Resource</h4>
            <p class="text-sm text-slate-600 dark:text-slate-400 mb-4">
                This will permanently delete the resource and all associated data. This action cannot be undone.
            </p>
        </div>
        <div x-show="!confirmDelete">
            <button @click="confirmDelete = true" class="inline-flex items-center justify-center px-4 py-2 min-h-[44px] bg-red-600 hover:bg-red-700 text-white font-medium rounded-md transition-colors">
                Delete Resource
            </button>
        </div>
        <div x-show="confirmDelete" class="flex items-center gap-3">
            <span class="text-sm font-medium text-slate-900 dark:text-white">Are you sure?</span>
            <form method="POST" action="/delete" class="inline">
                <button type="submit" class="inline-flex items-center justify-center px-4 py-2 min-h-[44px] bg-red-600 hover:bg-red-700 text-white font-medium rounded-md transition-colors">
                    Yes, Delete
                </button>
            </form>
            <button @click="confirmDelete = false" class="inline-flex items-center justify-center px-4 py-2 min-h-[44px] bg-slate-600 hover:bg-slate-700 text-white font-medium rounded-md transition-colors">
                Cancel
            </button>
        </div>
    </div>
</div>
```

**Requirements**:
- Use Alpine.js for inline confirmation (`x-data`, `x-show`, `@click`)
- Include clear consequence messaging
- State when actions cannot be undone
- Always use red color scheme

---

### Cards

#### Standard Card

```html
<div class="bg-white dark:bg-slate-800 rounded-lg shadow-md p-6 mb-6 border border-gray-200 dark:border-slate-700">
    <p class="text-slate-700 dark:text-slate-300">Card content</p>
</div>
```

#### Card with Header

```html
<div class="bg-white dark:bg-slate-800 rounded-lg shadow-md border border-gray-200 dark:border-slate-700 mb-6 overflow-hidden">
    <div class="flex justify-between items-center p-4 border-b border-gray-200 dark:border-slate-700 bg-gray-50 dark:bg-slate-900">
        <h2 class="text-lg font-semibold text-slate-900 dark:text-white">Card Title</h2>
        <button class="inline-flex items-center justify-center px-3 py-1.5 min-h-[36px] bg-indigo-600 hover:bg-indigo-700 text-white text-sm font-medium rounded-md transition-colors">
            Action
        </button>
    </div>
    <div class="p-6">
        <p class="text-slate-700 dark:text-slate-300">Card content</p>
    </div>
</div>
```

---

### Forms

#### Text Input

```html
<div class="mb-4">
    <label class="block text-sm font-medium text-slate-700 dark:text-slate-300 mb-2">Label</label>
    <input type="text" class="w-full px-3 py-2 min-h-[44px] border border-gray-300 dark:border-slate-600 rounded-md bg-white dark:bg-slate-800 text-slate-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-indigo-500">
</div>
```

#### Select

```html
<div class="mb-4">
    <label class="block text-sm font-medium text-slate-700 dark:text-slate-300 mb-2">Select</label>
    <select class="w-full px-3 py-2 min-h-[44px] border border-gray-300 dark:border-slate-600 rounded-md bg-white dark:bg-slate-800 text-slate-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-indigo-500">
        <option>Option 1</option>
    </select>
</div>
```

#### Textarea

```html
<div class="mb-4">
    <label class="block text-sm font-medium text-slate-700 dark:text-slate-300 mb-2">Description</label>
    <textarea rows="3" class="w-full px-3 py-2 border border-gray-300 dark:border-slate-600 rounded-md bg-white dark:bg-slate-800 text-slate-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-indigo-500"></textarea>
</div>
```

---

### Tables

```html
<div class="bg-white dark:bg-slate-800 rounded-lg shadow-md border border-gray-200 dark:border-slate-700 overflow-hidden">
    <div class="overflow-x-auto">
        <table class="min-w-full divide-y divide-gray-200 dark:divide-slate-700">
            <thead class="bg-gray-50 dark:bg-slate-900">
                <tr>
                    <th scope="col" class="px-6 py-3 text-left text-xs font-medium text-slate-500 dark:text-slate-400 uppercase tracking-wider">Name</th>
                    <th scope="col" class="px-6 py-3 text-left text-xs font-medium text-slate-500 dark:text-slate-400 uppercase tracking-wider">Status</th>
                    <th scope="col" class="px-6 py-3 text-right text-xs font-medium text-slate-500 dark:text-slate-400 uppercase tracking-wider">Actions</th>
                </tr>
            </thead>
            <tbody class="bg-white dark:bg-slate-800 divide-y divide-gray-200 dark:divide-slate-700">
                <tr class="hover:bg-gray-50 dark:hover:bg-slate-700 transition-colors">
                    <td class="px-6 py-4 whitespace-nowrap text-sm font-medium text-slate-900 dark:text-white">Item Name</td>
                    <td class="px-6 py-4 whitespace-nowrap text-sm text-slate-700 dark:text-slate-300">Active</td>
                    <td class="px-6 py-4 whitespace-nowrap text-right text-sm">
                        <button class="inline-flex items-center justify-center px-3 py-1.5 min-h-[36px] bg-indigo-600 hover:bg-indigo-700 text-white text-sm font-medium rounded-md transition-colors">
                            View
                        </button>
                    </td>
                </tr>
            </tbody>
        </table>
    </div>
</div>
```

---

### Alerts / Banners

#### Info Banner

```html
<div class="mb-4 p-4 bg-indigo-50 dark:bg-indigo-900/20 border-l-4 border-indigo-500 dark:border-indigo-400 rounded">
    <p class="text-sm text-indigo-900 dark:text-indigo-200">Info message here</p>
</div>
```

#### Warning Banner

```html
<div class="mb-4 p-4 bg-yellow-50 dark:bg-yellow-900/20 border-l-4 border-yellow-400 rounded">
    <p class="text-sm text-yellow-900 dark:text-yellow-200">Warning message here</p>
</div>
```

#### Error Banner

```html
<div class="mb-4 p-4 bg-red-50 dark:bg-red-900/20 border-l-4 border-red-500 dark:border-red-400 rounded">
    <p class="text-sm text-red-900 dark:text-red-200">Error message here</p>
</div>
```

---

## Typography & Spacing

### Headings

- **H1**: `text-2xl md:text-3xl font-bold text-slate-900 dark:text-white`
- **H2**: `text-xl font-semibold text-slate-900 dark:text-white`
- **H3**: `text-lg font-semibold text-slate-900 dark:text-white`

### Body Text

- **Primary**: `text-slate-700 dark:text-slate-300`
- **Secondary**: `text-slate-600 dark:text-slate-400`
- **Muted**: `text-slate-500 dark:text-slate-500`

### Spacing Scale

Use Tailwind's spacing scale consistently:
- **4px**: `gap-1`, `p-1`, `m-1`
- **8px**: `gap-2`, `p-2`, `m-2`
- **12px**: `gap-3`, `p-3`, `m-3`
- **16px**: `gap-4`, `p-4`, `m-4`
- **24px**: `gap-6`, `p-6`, `m-6`
- **32px**: `gap-8`, `p-8`, `m-8`

---

## Touch Targets

### Minimum Sizes

- **Standard buttons/inputs**: `min-h-[44px]` (44px minimum)
- **Small buttons**: `min-h-[36px]` (36px minimum for inline actions)
- **Checkboxes/radios**: `w-4 h-4` (16px, acceptable for small controls)

### Mobile Considerations

- All interactive elements meet 44px minimum on mobile
- Forms stack vertically on mobile (`flex-col md:flex-row`)
- Tables scroll horizontally (`overflow-x-auto`)
- Navigation collapses to hamburger menu

---

## Theme Support

### Three Themes

1. **Light**: Clean white backgrounds (#ffffff)
2. **Night**: Dark slate (#1e293b)
3. **OLED Night**: Pure black (#000000) for OLED power savings

### Dark Mode Classes

All components use Tailwind's `dark:` variant:
- `bg-white dark:bg-slate-800`
- `text-slate-900 dark:text-white`
- `border-gray-200 dark:border-slate-700`

### Gradient Visibility

The indigo-purple gradient works in all three themes:
- **Light**: Full vibrancy
- **Night**: Slightly muted but visible
- **OLED**: High contrast against pure black

---

## Quick Reference

### When to Use Each Color

| Action Type | Color | Example |
|-------------|-------|---------|
| Create, Edit, View | Indigo | "Create Game", "Edit Config", "View Details" |
| Start, Deploy, Save | Green | "Start Session", "Deploy Config", "Save Changes" |
| Delete, Stop, Force | Red | "Delete Game", "Stop Session", "Force Restart" |
| Cancel, Back | Slate | "Cancel", "Back to List", "Close" |
| Warnings (badges only) | Yellow | "Pending", "Starting", "Stopping" |

### Component Selection

| Need | Use |
|------|-----|
| Main section page | Hero header with gradient |
| Detail page | Breadcrumbs (no hero) |
| Destructive action | Danger zone at bottom |
| List of items | Table with hover states |
| Form | Consistent input styling |
| Status indicator | Badge with appropriate color |
| Alert/notification | Banner with left border |

---

## Migration Checklist

When updating a page to the design system:

- [ ] Replace blue buttons with indigo (`bg-indigo-600 hover:bg-indigo-700`)
- [ ] Replace gray buttons with slate (`bg-slate-600 hover:bg-slate-700`)
- [ ] Add hero header to main section pages
- [ ] Move delete actions to danger zone at bottom
- [ ] Update status badges to use new colors
- [ ] Ensure all buttons meet 44px minimum height
- [ ] Test in all three themes (light/night/OLED)
- [ ] Verify mobile responsiveness

---

## Resources

- **Component Library**: `manmanv2/ui/templates/partials/components.html`
- **Example Pages**: `games.html`, `servers.html`, `sessions.html`
- **Workshop Examples**: `workshop_library.html`, `workshop_search.html`

---

**Last Updated**: March 2026  
**Version**: 1.0  
**Status**: Production Ready
