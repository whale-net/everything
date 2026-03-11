package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/whale-net/everything/libs/go/htmxbase"
	"github.com/whale-net/everything/manmanv2/protos"
)

//go:embed templates/*.html templates/partials/*.html
var templateFS embed.FS

var templates *template.Template

// manmanStyles contains the CSS styles for the ManManV2 UI
const manmanStyles = template.CSS(`
/* Theme-specific CSS variables */
:root[data-theme="light"] {
	--bg-primary: #ffffff;
	--bg-secondary: #f9fafb;
	--bg-tertiary: #f3f4f6;
	--text-primary: #111827;
	--text-secondary: #6b7280;
	--border-color: #e5e7eb;
}

:root[data-theme="night"] {
	--bg-primary: #1e293b;
	--bg-secondary: #0f172a;
	--bg-tertiary: #334155;
	--text-primary: #f1f5f9;
	--text-secondary: #cbd5e1;
	--border-color: #475569;
}

:root[data-theme="oled"] {
	--bg-primary: #000000;
	--bg-secondary: #0a0a0a;
	--bg-tertiary: #1a1a1a;
	--text-primary: #ffffff;
	--text-secondary: #a3a3a3;
	--border-color: #262626;
}

/* Apply dark class for Tailwind dark mode */
:root[data-theme="night"],
:root[data-theme="oled"] {
	color-scheme: dark;
}

/* OLED theme overrides - pure black backgrounds */
:root[data-theme="oled"] main {
	background-color: #000000 !important;
}

:root[data-theme="oled"] nav {
	background-color: #000000 !important;
}

:root[data-theme="oled"] .dark\:bg-slate-900,
:root[data-theme="oled"] .dark\:bg-slate-800,
:root[data-theme="oled"] .dark\:bg-slate-700 {
	background-color: #000000 !important;
}

:root[data-theme="oled"] .dark\:bg-slate-950 {
	background-color: #000000 !important;
}

:root[data-theme="oled"] .dark\:border-slate-700,
:root[data-theme="oled"] .dark\:border-slate-600 {
	border-color: #1a1a1a !important;
}

* {
	margin: 0;
	padding: 0;
	box-sizing: border-box;
}

body {
	font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
	line-height: 1.6;
}

/* Legacy CSS for backward compatibility during migration */
/* These will be removed as templates are migrated to Tailwind */

/* Cards - theme-aware */
.card {
	background: var(--bg-primary);
	color: var(--text-primary);
	border-radius: 8px;
	padding: 1.5rem;
	box-shadow: 0 2px 4px rgba(0,0,0,0.1);
	margin-bottom: 1.5rem;
	border: 1px solid var(--border-color);
}

.card-header {
	display: flex;
	justify-content: space-between;
	align-items: center;
	margin-bottom: 1rem;
	padding-bottom: 0.5rem;
	border-bottom: 1px solid var(--border-color);
}

.card-title {
	font-size: 1.25rem;
	font-weight: 600;
	color: var(--text-primary);
}

/* Buttons - legacy */
.btn {
	display: inline-block;
	padding: 0.5rem 1rem;
	border: none;
	border-radius: 4px;
	cursor: pointer;
	text-decoration: none;
	font-size: 0.9rem;
	transition: all 0.2s;
	min-height: 44px;
	display: inline-flex;
	align-items: center;
	justify-content: center;
}

.btn-primary {
	background: #3498db;
	color: white;
}

.btn-primary:hover {
	background: #2980b9;
}

.btn-success {
	background: #27ae60;
	color: white;
}

.btn-success:hover {
	background: #229954;
}

.btn-danger {
	background: #e74c3c;
	color: white;
}

.btn-danger:hover {
	background: #c0392b;
}

.btn-secondary {
	background: #95a5a6;
	color: white;
}

.btn-secondary:hover {
	background: #7f8c8d;
}

.btn-sm {
	padding: 0.25rem 0.5rem;
	font-size: 0.85rem;
	min-height: 36px;
}

/* Badges */
.badge {
	display: inline-block;
	padding: 0.15rem 0.5rem;
	border-radius: 10px;
	font-size: 0.75rem;
	font-weight: 500;
}

.badge-success {
	background: #d4edda;
	color: #155724;
}

.badge-warning {
	background: #fff3cd;
	color: #856404;
}

.badge-danger {
	background: #f8d7da;
	color: #721c24;
}

.badge-secondary {
	background: #e2e3e5;
	color: #383d41;
}

/* Tables - theme-aware */
table {
	width: 100%;
	border-collapse: collapse;
	font-size: 0.875rem;
	background: var(--bg-primary);
	color: var(--text-primary);
}

th, td {
	padding: 0.4rem 0.6rem;
	text-align: left;
	border-bottom: 1px solid var(--border-color);
}

th {
	background: var(--bg-secondary);
	font-weight: 600;
	font-size: 0.8rem;
	text-transform: uppercase;
	color: var(--text-secondary);
	letter-spacing: 0.03em;
}

tr:hover {
	background: var(--bg-secondary);
}

/* Forms - theme-aware */
.form-group {
	margin-bottom: 1rem;
}

label {
	display: block;
	margin-bottom: 0.25rem;
	font-weight: 500;
	color: var(--text-primary);
}

input[type="text"],
input[type="number"],
input[type="datetime-local"],
textarea,
select {
	width: 100%;
	padding: 0.5rem;
	border: 1px solid var(--border-color);
	border-radius: 4px;
	font-size: 1rem;
	background: var(--bg-primary);
	color: var(--text-primary);
	min-height: 44px;
}

textarea {
	min-height: 100px;
	font-family: monospace;
}

/* Grid layouts */
.grid {
	display: grid;
	gap: 1.5rem;
}

.grid-2 {
	grid-template-columns: repeat(2, 1fr);
}

.grid-3 {
	grid-template-columns: repeat(3, 1fr);
}

.grid-4 {
	grid-template-columns: repeat(4, 1fr);
}

@media (max-width: 768px) {
	.grid-2, .grid-3, .grid-4 {
		grid-template-columns: 1fr;
	}
}

/* Stats cards - theme-aware */
.stat-card {
	background: var(--bg-primary);
	color: var(--text-primary);
	border-radius: 8px;
	padding: 1.5rem;
	box-shadow: 0 2px 4px rgba(0,0,0,0.1);
	text-align: center;
	border: 1px solid var(--border-color);
}

.stat-value {
	font-size: 2.5rem;
	font-weight: bold;
	color: #3498db;
}

.stat-label {
	color: var(--text-secondary);
	margin-top: 0.5rem;
}

/* Empty state */
.empty-state {
	text-align: center;
	padding: 3rem;
	color: var(--text-secondary);
}

/* Loading indicator */
.htmx-indicator {
	display: none;
}

.htmx-request .htmx-indicator {
	display: inline-block;
}

.spinner {
	border: 3px solid var(--border-color);
	border-top: 3px solid #3498db;
	border-radius: 50%;
	width: 20px;
	height: 20px;
	animation: spin 1s linear infinite;
	display: inline-block;
	margin-left: 0.5rem;
}

@keyframes spin {
	0% { transform: rotate(0deg); }
	100% { transform: rotate(360deg); }
}

/* Toast notifications - theme-aware */
.toast {
	background: var(--bg-primary);
	color: var(--text-primary);
	border-radius: 4px;
	padding: 1rem;
	box-shadow: 0 4px 6px rgba(0,0,0,0.3);
	margin-bottom: 0.5rem;
	animation: slideIn 0.3s ease-out;
	border: 1px solid var(--border-color);
}

@keyframes slideIn {
	from {
		transform: translateX(400px);
		opacity: 0;
	}
	to {
		transform: translateX(0);
		opacity: 1;
	}
}

.toast-success {
	border-left: 4px solid #27ae60;
}

.toast-error {
	border-left: 4px solid #e74c3c;
}

/* Action bar */
.action-bar {
	display: flex;
	justify-content: space-between;
	align-items: center;
	margin-bottom: 1.5rem;
	flex-wrap: wrap;
	gap: 1rem;
}

/* Session panels - theme-aware */
.session-panels {
	display: flex;
	flex-wrap: wrap;
	gap: 1rem;
	margin-top: 1rem;
}

.session-panel {
	background: var(--bg-primary);
	color: var(--text-primary);
	border-radius: 8px;
	padding: 1rem 1.25rem;
	box-shadow: 0 2px 4px rgba(0,0,0,0.1);
	width: calc(50% - 0.5rem);
	text-decoration: none;
	transition: box-shadow 0.2s, transform 0.15s;
	display: block;
	border: 1px solid var(--border-color);
}

.session-panel:hover {
	box-shadow: 0 4px 12px rgba(0,0,0,0.15);
	transform: translateY(-1px);
}

.session-panel-header {
	display: flex;
	justify-content: space-between;
	align-items: center;
	margin-bottom: 0.5rem;
}

.session-uptime {
	font-size: 0.8rem;
	color: var(--text-secondary);
}

.session-panel-game {
	font-weight: 600;
	font-size: 1.05rem;
	color: var(--text-primary);
	margin-bottom: 0.15rem;
}

.session-panel-config {
	font-size: 0.85rem;
	color: var(--text-secondary);
	margin-bottom: 0.5rem;
}

.session-panel-meta {
	font-size: 0.8rem;
	color: var(--text-secondary);
}

.session-panel-ports {
	display: flex;
	flex-wrap: wrap;
	gap: 0.35rem;
	margin-top: 0.5rem;
}

.session-port {
	display: inline-block;
	background: #eef2ff;
	color: #4338ca;
	padding: 0.1rem 0.5rem;
	border-radius: 4px;
	font-size: 0.8rem;
	font-family: monospace;
}

@media (max-width: 768px) {
	.session-panel {
		width: 100%;
	}
	
	.action-bar {
		flex-direction: column;
		align-items: flex-start;
	}
}

/* Alert styles - theme-aware */
.alert {
	padding: 1rem;
	border-radius: 4px;
	margin-bottom: 1rem;
	border: 1px solid;
}

.alert-warning {
	background: #fff3cd;
	color: #856404;
	border-color: #ffc107;
}

.alert-info {
	background: #d1ecf1;
	color: #0c5460;
	border-color: #bee5eb;
}

.alert-danger {
	background: #f8d7da;
	color: #721c24;
	border-color: #f5c6cb;
}
`)

// Breadcrumb represents a single breadcrumb item
type Breadcrumb struct {
	Label string
	URL   string
	Icon  string
}

// LayoutData is the shared layout context for all pages.
type LayoutData struct {
	Title           string
	Active          string
	User            any
	Content         template.HTML
	Servers         []*manmanpb.Server
	SelectedServer  *manmanpb.Server
	DefaultServerID int64
	Breadcrumbs     []Breadcrumb
}

func init() {
	var err error
	
	// Create function map for template helpers
	funcMap := template.FuncMap{
		"formatTime":  formatTime,
		"timeAgo":     timeAgo,
		"statusBadge": statusBadge,
		"toJSON":      toJSON,
		"toJSONEmpty": toJSONEmpty,
		"sgcName":     sgcName,
		"divf":        func(a int64, b float64) float64 { return float64(a) / b },
	}
	
	templates, err = template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/*.html", "templates/partials/*.html")
	if err != nil {
		log.Fatalf("Failed to parse templates: %v", err)
	}
}

// Template helper functions

// renderPage renders a complete page using htmxbase with the app layout
func renderPage(w http.ResponseWriter, contentTemplate string, contentData any, layout LayoutData) error {
	// Render the content template
	var contentBuf bytes.Buffer
	if err := templates.ExecuteTemplate(&contentBuf, contentTemplate, contentData); err != nil {
		return err
	}

	// Render the app wrapper (nav + content container)
	layout.Content = template.HTML(contentBuf.String())
	var wrapperBuf bytes.Buffer
	if err := templates.ExecuteTemplate(&wrapperBuf, "wrapper.html", layout); err != nil {
		return err
	}

	// Use htmxbase for the outer HTML structure
	return htmxbase.Render(w, htmxbase.LayoutData{
		Title:       layout.Title,
		TitleSuffix: "ManManV2",
		CustomHead: template.HTML(`<script src="https://cdn.tailwindcss.com"></script>
<script>
tailwind.config = {
	darkMode: 'class',
}
</script>`),
		CustomCSS:   manmanStyles,
		Content:     template.HTML(wrapperBuf.String()),
		CustomScripts: template.JS(`
			// Toast notification helper
			document.body.addEventListener('showToast', function(evt) {
				const toast = document.createElement('div');
				toast.className = 'toast toast-' + evt.detail.type;
				toast.textContent = evt.detail.message;
				document.getElementById('toast').appendChild(toast);

				setTimeout(() => toast.remove(), 5000);
			});
		`),
	})
}

// renderTemplate renders a named template directly to the response (for HTMX partials)
func renderTemplate(w http.ResponseWriter, name string, data any) error {
	return templates.ExecuteTemplate(w, name, data)
}

func toJSON(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return ""
	}
	return string(data)
}

func toJSONEmpty(value any) string {
	if value == nil {
		return ""
	}
	return toJSON(value)
}

func formatTime(timestamp int64) string {
	if timestamp == 0 {
		return "Never"
	}
	t := time.Unix(timestamp, 0)
	return t.Format("2006-01-02 15:04:05")
}

func timeAgo(timestamp int64) string {
	if timestamp == 0 {
		return "Never"
	}
	
	t := time.Unix(timestamp, 0)
	duration := time.Since(t)
	
	if duration < time.Minute {
		return "Just now"
	} else if duration < time.Hour {
		minutes := int(duration.Minutes())
		if minutes == 1 {
			return "1 minute ago"
		}
		return formatDuration(minutes, "minute")
	} else if duration < 24*time.Hour {
		hours := int(duration.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return formatDuration(hours, "hour")
	} else {
		days := int(duration.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return formatDuration(days, "day")
	}
}

func formatDuration(value int, unit string) string {
	return fmt.Sprintf("%d %ss ago", value, unit)
}

func statusBadge(status string) string {
	switch status {
	case "online", "active", "running", "completed":
		return "bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200"
	case "offline", "inactive", "stopped":
		return "bg-slate-100 text-slate-800 dark:bg-slate-700 dark:text-slate-300"
	case "starting", "stopping", "pending":
		return "bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-200"
	case "crashed", "error", "failed":
		return "bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200"
	case "deployed":
		return "bg-indigo-100 text-indigo-800 dark:bg-indigo-900 dark:text-indigo-200"
	default:
		return "bg-slate-100 text-slate-800 dark:bg-slate-700 dark:text-slate-300"
	}
}

// sgcName looks up a display name from the map, falling back to "SGC {id}".
func sgcName(names map[int64]string, id int64) string {
	if names != nil {
		if name, ok := names[id]; ok && name != "" {
			return name
		}
	}
	return fmt.Sprintf("SGC %d", id)
}
