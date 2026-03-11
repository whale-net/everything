package utils

import "net/http"

func HXRedirect(w http.ResponseWriter, url string) {
	w.Header().Set("HX-Redirect", url)
}

func HXRefresh(w http.ResponseWriter) {
	w.Header().Set("HX-Refresh", "true")
}

func HXTrigger(w http.ResponseWriter, event string) {
	w.Header().Set("HX-Trigger", event)
}
