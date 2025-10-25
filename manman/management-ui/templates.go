package main

import (
	"embed"
	"html/template"
)

//go:embed templates/*.html
var templateFS embed.FS

var templates *template.Template

func init() {
	templates = template.Must(template.ParseFS(templateFS, "templates/*.html"))
}
