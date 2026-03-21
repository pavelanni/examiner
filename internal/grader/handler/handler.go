package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/pavelanni/examiner/internal/grader/store"
	"github.com/pavelanni/examiner/internal/model"
)

// Handler holds dependencies for HTTP request handling.
type Handler struct {
	store         *store.Store
	secureCookies bool
}

// New creates a Handler.
func New(s *store.Store, secureCookies bool) *Handler {
	return &Handler{store: s, secureCookies: secureCookies}
}

// Routes returns the configured chi router.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Public
	r.Group(func(r chi.Router) {
		r.Use(h.csrfMiddleware)
		r.Get("/login", h.loginPage)
		r.Post("/login", h.handleLogin)
	})

	// Protected
	r.Group(func(r chi.Router) {
		r.Use(h.requireAuth)
		r.Use(h.csrfMiddleware)
		r.Post("/logout", h.handleLogout)

		// Teacher + Admin
		r.Get("/", h.dashboard)
		r.Get("/exam/{examID}", h.examStudentList)
		r.Get("/exam/{examID}/student/{sessionID}", h.reviewPage)
		r.Post("/exam/{examID}/student/{sessionID}/score/{questionID}",
			h.handleUpdateScore)
		r.Post("/exam/{examID}/student/{sessionID}/finalize",
			h.handleFinalize)
		r.Get("/exam/{examID}/student/{sessionID}/report",
			h.handleReport)

		// Admin only
		r.Group(func(r chi.Router) {
			r.Use(requireRole(model.UserRoleAdmin))
			r.Get("/admin/upload", h.uploadPage)
			r.Post("/admin/upload", h.handleUpload)
			r.Get("/admin/users", h.usersPage)
			r.Post("/admin/users", h.handleCreateUser)
			r.Post("/admin/users/import", h.handleImportUsers)
			r.Post("/admin/users/{userID}/toggle", h.handleToggleUser)
			r.Delete("/admin/exam/{examID}", h.handleDeleteExam)
		})
	})

	return r
}
