package main

// LEGACY: This file contains the old html/template system.
// Remaining legacy templates:
// - actions_manage.html (809 lines - complex action management UI), still
//   rendered as an HTML fragment (not a full page) by the reachable
//   handleGameActions/handleConfigActions handlers in handlers_actions.go.
//
// All other pages and partials have been migrated to templ (see pages/*.templ and components/*.templ).
// New pages should use templ components.
//
// The full-page legacy render path that used to live here (renderPage,
// renderTemplate, this package's own LayoutData/Breadcrumb types, and
// manmanStyles — the CustomCSS literal those fed into htmxbase, including
// the pre-daisyUI OLED "force pure black" overrides) was removed in the
// daisyUI/htmxui chrome migration (#1007): renderPage's only caller,
// handleSGCActions, dispatched on a "/sgcs/" URL prefix main.go never
// actually mounts (routes register "/sgc/", singular — see
// handleSGCRoutes), so the whole chain (handleSGCActions -> renderPage ->
// manmanStyles/this package's LayoutData) was dead code, and this
// package's buildLayoutData (main.go), the only producer of that
// LayoutData, was equally unreachable. Deleted rather than migrated, per
// #1007's scope. manmanStyles' classes that ARE still reachable today
// (via actions_manage.html, below) are covered post-migration by daisyUI
// itself (.btn, .btn-primary, .btn-sm, .badge, .badge-success,
// .badge-secondary all share daisyUI's own class names) or by the small
// documented shim in templ_render.go (.btn-danger, which daisyUI spells
// .btn-error) — see buildHead's legacyClassShimCSS there.
import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

var templates *template.Template

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
	
	templates, err = template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		log.Fatalf("Failed to parse templates: %v", err)
	}
}

// Template helper functions

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
