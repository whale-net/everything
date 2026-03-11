package utils

import (
	"net/http"
	"github.com/a-h/templ"
)

func RenderComponent(w http.ResponseWriter, r *http.Request, component templ.Component) error {
	return component.Render(r.Context(), w)
}

func RenderError(w http.ResponseWriter, r *http.Request, statusCode int, message string) {
	w.WriteHeader(statusCode)
	w.Write([]byte(message))
}

func RenderNotFound(w http.ResponseWriter, r *http.Request) {
	RenderError(w, r, http.StatusNotFound, "Not Found")
}
