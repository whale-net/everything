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
* {
	margin: 0;
	padding: 0;
	box-sizing: border-box;
}

body {
	font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
	background: #f5f5f5;
	color: #333;
	line-height: 1.6;
}

.container {
	max-width: 1200px;
	margin: 0 auto;
	padding: 0 20px;
}

/* Navigation */
nav {
	background: #2c3e50;
	color: white;
	padding: 1rem 0;
	box-shadow: 0 2px 4px rgba(0,0,0,0.1);
}

nav .container {
	display: flex;
	justify-content: space-between;
	align-items: center;
}

nav .logo {
	font-size: 1.5rem;
	font-weight: bold;
}

nav ul {
	list-style: none;
	display: flex;
	gap: 2rem;
}

nav a {
	color: white;
	text-decoration: none;
	transition: opacity 0.2s;
}

nav a:hover {
	opacity: 0.8;
}

nav a.active {
	border-bottom: 2px solid #3498db;
	padding-bottom: 4px;
}

/* Main content */
main {
	padding: 2rem 0;
	min-height: calc(100vh - 200px);
}

/* Cards */
.card {
	background: white;
	border-radius: 8px;
	padding: 1.5rem;
	box-shadow: 0 2px 4px rgba(0,0,0,0.1);
	margin-bottom: 1.5rem;
}

.card-header {
	display: flex;
	justify-content: space-between;
	align-items: center;
	margin-bottom: 1rem;
	padding-bottom: 0.5rem;
	border-bottom: 1px solid #eee;
}

.card-title {
	font-size: 1.25rem;
	font-weight: 600;
}

/* Buttons */
.btn {
	display: inline-block;
	padding: 0.5rem 1rem;
	border: none;
	border-radius: 4px;
	cursor: pointer;
	text-decoration: none;
	font-size: 0.9rem;
	transition: all 0.2s;
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

/* Tables */
table {
	width: 100%;
	border-collapse: collapse;
	font-size: 0.875rem;
}

th, td {
	padding: 0.4rem 0.6rem;
	text-align: left;
	border-bottom: 1px solid #eee;
}

th {
	background: #f8f9fa;
	font-weight: 600;
	font-size: 0.8rem;
	text-transform: uppercase;
	color: #6b7280;
	letter-spacing: 0.03em;
}

tr:hover {
	background: #f8f9fa;
}

/* Forms */
.form-group {
	margin-bottom: 1rem;
}

label {
	display: block;
	margin-bottom: 0.25rem;
	font-weight: 500;
}

input[type="text"],
input[type="number"],
textarea,
select {
	width: 100%;
	padding: 0.5rem;
	border: 1px solid #ddd;
	border-radius: 4px;
	font-size: 1rem;
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

/* Stats cards */
.stat-card {
	background: white;
	border-radius: 8px;
	padding: 1.5rem;
	box-shadow: 0 2px 4px rgba(0,0,0,0.1);
	text-align: center;
}

.stat-value {
	font-size: 2.5rem;
	font-weight: bold;
	color: #3498db;
}

.stat-label {
	color: #7f8c8d;
	margin-top: 0.5rem;
}

/* Empty state */
.empty-state {
	text-align: center;
	padding: 3rem;
	color: #7f8c8d;
}

/* Loading indicator */
.htmx-indicator {
	display: none;
}

.htmx-request .htmx-indicator {
	display: inline-block;
}

.spinner {
	border: 3px solid #f3f3f3;
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

/* Toast notifications */
#toast {
	position: fixed;
	bottom: 2rem;
	right: 2rem;
	max-width: 400px;
	z-index: 1000;
}

.toast {
	background: white;
	border-radius: 4px;
	padding: 1rem;
	box-shadow: 0 4px 6px rgba(0,0,0,0.1);
	margin-bottom: 0.5rem;
	animation: slideIn 0.3s ease-out;
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
}

/* Session panels */
.session-panels {
	display: flex;
	flex-wrap: wrap;
	gap: 1rem;
	margin-top: 1rem;
}

.session-panel {
	background: white;
	border-radius: 8px;
	padding: 1rem 1.25rem;
	box-shadow: 0 2px 4px rgba(0,0,0,0.1);
	width: calc(50% - 0.5rem);
	text-decoration: none;
	color: inherit;
	transition: box-shadow 0.2s, transform 0.15s;
	display: block;
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
	color: #7f8c8d;
}

.session-panel-game {
	font-weight: 600;
	font-size: 1.05rem;
	color: #1f2937;
	margin-bottom: 0.15rem;
}

.session-panel-config {
	font-size: 0.85rem;
	color: #6b7280;
	margin-bottom: 0.5rem;
}

.session-panel-meta {
	font-size: 0.8rem;
	color: #7f8c8d;
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
}
`)

// LayoutData is the shared layout context for all pages.
type LayoutData struct {
	Title           string
	Active          string
	User            any
	Content         template.HTML
	Servers         []*manmanpb.Server
	SelectedServer  *manmanpb.Server
	DefaultServerID int64
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
	case "online", "active", "running":
		return "badge-success"
	case "offline", "inactive", "stopped":
		return "badge-secondary"
	case "starting", "stopping", "pending":
		return "badge-warning"
	case "crashed", "error":
		return "badge-danger"
	default:
		return "badge-secondary"
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
