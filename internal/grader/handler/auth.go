package handler

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"log/slog"
	"net/http"

	"golang.org/x/crypto/bcrypt"

	"github.com/pavelanni/examiner/internal/grader/handler/views"
	"github.com/pavelanni/examiner/internal/model"
)

const (
	sessionCookieName = "session"
	csrfCookieName    = "csrf_token"
)

// generateCSRFToken generates a new CSRF token.
func generateCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// csrfMiddleware protects against cross-site request forgery.
// GET/HEAD requests reuse the existing csrf_token cookie or generate a new one.
// POST requests validate the form csrf_token against the cookie value.
// Tokens are NOT rotated on every request for htmx compatibility.
func (h *Handler) csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "HEAD" {
			// Reuse existing CSRF token if present; generate only on first visit.
			cookie, err := r.Cookie(csrfCookieName)
			if err != nil || cookie.Value == "" {
				token, err := generateCSRFToken()
				if err != nil {
					slog.Error("failed to generate CSRF token", "error", err)
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
				http.SetCookie(w, &http.Cookie{
					Name:     csrfCookieName,
					Value:    token,
					Path:     "/",
					HttpOnly: false, // htmx needs to read it
					Secure:   h.secureCookies,
					SameSite: http.SameSiteLaxMode,
				})
				ctx := model.ContextWithCSRFToken(r.Context(), token)
				next.ServeHTTP(w, r.WithContext(ctx))
			} else {
				ctx := model.ContextWithCSRFToken(r.Context(), cookie.Value)
				next.ServeHTTP(w, r.WithContext(ctx))
			}
			return
		}

		cookie, err := r.Cookie(csrfCookieName)
		if err != nil || cookie.Value == "" {
			slog.Warn("CSRF cookie missing")
			http.Error(w, "csrf token missing", http.StatusForbidden)
			return
		}

		formToken := r.FormValue("csrf_token")
		if formToken == "" {
			slog.Warn("CSRF form token missing")
			http.Error(w, "csrf token missing", http.StatusForbidden)
			return
		}

		if len(formToken) != len(cookie.Value) || subtle.ConstantTimeCompare([]byte(formToken), []byte(cookie.Value)) != 1 {
			slog.Warn("CSRF token mismatch")
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}

		// Keep the same token for POST responses so htmx fragments stay valid.
		ctx := model.ContextWithCSRFToken(r.Context(), cookie.Value)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAuth is middleware that checks for a valid session cookie.
func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			redirectToLogin(w, r)
			return
		}

		authSess, err := h.store.GetAuthSession(cookie.Value)
		if err != nil {
			slog.Error("failed to get auth session", "error", err)
			redirectToLogin(w, r)
			return
		}
		if authSess == nil {
			redirectToLogin(w, r)
			return
		}

		user, err := h.store.GetUserByID(authSess.UserID)
		if err != nil || user == nil || !user.Active {
			redirectToLogin(w, r)
			return
		}

		ctx := model.ContextWithUser(r.Context(), user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireRole returns middleware that checks the user has one of the allowed roles.
func requireRole(allowed ...model.UserRole) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := model.UserFromContext(r.Context())
			if user == nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			for _, role := range allowed {
				if user.Role == role {
					next.ServeHTTP(w, r)
					return
				}
			}
			http.Error(w, "forbidden", http.StatusForbidden)
		})
	}
}

// redirectToLogin redirects the user to the login page.
func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// loginPage serves the login page.
func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.LoginPage("").Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

// handleLogin processes login form submission.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	user, err := h.store.GetUserByUsername(username)
	if err != nil {
		slog.Error("failed to get user", "error", err)
		renderLoginError(w, r)
		return
	}
	if user == nil || !user.Active {
		renderLoginError(w, r)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		renderLoginError(w, r)
		return
	}

	token, err := h.store.CreateAuthSession(user.ID)
	if err != nil {
		slog.Error("failed to create auth session", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.secureCookies,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleLogout processes logout request.
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil && cookie.Value != "" {
		_ = h.store.DeleteAuthSession(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.secureCookies,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: false,
		Secure:   h.secureCookies,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// renderLoginError renders the login page with an error message.
func renderLoginError(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	if err := views.LoginPage("Invalid username or password.").Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}
