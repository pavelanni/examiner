package handler

import "net/http"

// Stubs for handlers not yet implemented. Will be replaced in task 11.

func (h *Handler) handleReport(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
