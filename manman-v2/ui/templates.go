package main

import (
	"bytes"
	"embed"
	"html/template"
	"log"
	"time"
)

//go:embed templates/*.html templates/partials/*.html
var templateFS embed.FS

var templates *template.Template

// LayoutData is the shared layout context for all pages.
type LayoutData struct {
	Title   string
	Active  string
	User    any
	Content template.HTML
}

func init() {
	var err error
	
	// Create function map for template helpers
	funcMap := template.FuncMap{
		"formatTime": formatTime,
		"timeAgo":    timeAgo,
		"statusBadge": statusBadge,
	}
	
	templates, err = template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/*.html", "templates/partials/*.html")
	if err != nil {
		log.Fatalf("Failed to parse templates: %v", err)
	}
}

// Template helper functions

func renderWithLayout(contentTemplate string, contentData any, layout LayoutData) (LayoutData, error) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, contentTemplate, contentData); err != nil {
		return layout, err
	}

	layout.Content = template.HTML(buf.String())
	return layout, nil
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
	return time.Duration(value).String() + " " + unit + "s ago"
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
