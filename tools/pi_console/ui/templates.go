package main

import (
	"embed"
	"html/template"
)

//go:embed templates/*.html
var templateFS embed.FS

// App holds everything the UI's HTTP handlers need.
type App struct {
	hosts []Host
	token string
	tmpl  *template.Template
}

func (a *App) loadTemplates() error {
	tmpl, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return err
	}
	a.tmpl = tmpl
	return nil
}
