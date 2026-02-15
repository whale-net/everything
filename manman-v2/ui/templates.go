package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"time"

	"github.com/whale-net/everything/manman/protos"
)

//go:embed templates/*.html templates/partials/*.html
var templateFS embed.FS

var templates *template.Template

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

func renderWithLayout(contentTemplate string, contentData any, layout LayoutData) (LayoutData, error) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, contentTemplate, contentData); err != nil {
		return layout, err
	}

	layout.Content = template.HTML(buf.String())
	return layout, nil
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

// sgcName looks up a display name from the map, falling back to "SGC {id}".
func sgcName(names map[int64]string, id int64) string {
	if names != nil {
		if name, ok := names[id]; ok && name != "" {
			return name
		}
	}
	return fmt.Sprintf("SGC %d", id)
}
