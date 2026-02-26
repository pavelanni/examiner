package handler

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"log/slog"
	"net/http"

	"golang.org/x/crypto/bcrypt"

	"github.com/pavelanni/examiner/internal/handler/views"
	appI18n "github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
)

const (
	sessionCookieName = "session"
	csrfCookieName    = "csrf_token"
)

func generateCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

func (h *Handler) csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "HEAD" {
			token, err := generateCSRFToken()
			if err != nil {
				slog.Error("failed to generate CSRF token", "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			cookiePath := "/"
			if h.config.BasePath != "" {
				cookiePath = h.config.BasePath + "/"
			}
			http.SetCookie(w, &http.Cookie{
				Name:     csrfCookieName,
				Value:    token,
				Path:     cookiePath,
				HttpOnly: false,
				Secure:   h.config.SecureCookies,
				SameSite: http.SameSiteLaxMode,
			})
			ctx := model.ContextWithCSRFToken(r.Context(), token)
			next.ServeHTTP(w, r.WithContext(ctx))
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

		token, err := generateCSRFToken()
		if err != nil {
			slog.Error("failed to generate CSRF token", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		cookiePath := "/"
		if h.config.BasePath != "" {
			cookiePath = h.config.BasePath + "/"
		}
		http.SetCookie(w, &http.Cookie{
			Name:     csrfCookieName,
			Value:    token,
			Path:     cookiePath,
			HttpOnly: false,
			Secure:   h.config.SecureCookies,
			SameSite: http.SameSiteLaxMode,
		})

		ctx := model.ContextWithCSRFToken(r.Context(), token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAuth is middleware that checks for a valid session cookie.
func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			h.redirectToLogin(w, r)
			return
		}

		authSess, err := h.store.GetAuthSession(cookie.Value)
		if err != nil {
			slog.Error("failed to get auth session", "error", err)
			h.redirectToLogin(w, r)
			return
		}
		if authSess == nil {
			h.redirectToLogin(w, r)
			return
		}

		user, err := h.store.GetUserByID(authSess.UserID)
		if err != nil || user == nil || !user.Active {
			h.redirectToLogin(w, r)
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

func (h *Handler) redirectToLogin(w http.ResponseWriter, r *http.Request) {
	loginPath := h.path("/login")
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", loginPath)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, loginPath, http.StatusSeeOther)
}

func (h *Handler) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.LoginPage("").Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	user, err := h.store.GetUserByUsername(username)
	if err != nil {
		slog.Error("failed to get user", "error", err)
		h.renderLoginError(w, r)
		return
	}
	if user == nil || !user.Active {
		h.renderLoginError(w, r)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		h.renderLoginError(w, r)
		return
	}

	token, err := h.store.CreateAuthSession(user.ID)
	if err != nil {
		slog.Error("failed to create auth session", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	cookiePath := "/"
	if h.config.BasePath != "" {
		cookiePath = h.config.BasePath + "/"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     cookiePath,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.config.SecureCookies,
	})
	http.Redirect(w, r, h.path("/"), http.StatusSeeOther)
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil && cookie.Value != "" {
		_ = h.store.DeleteAuthSession(cookie.Value)
	}

	logoutCookiePath := "/"
	if h.config.BasePath != "" {
		logoutCookiePath = h.config.BasePath + "/"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     logoutCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.config.SecureCookies,
	})
	http.Redirect(w, r, h.path("/login"), http.StatusSeeOther)
}

func (h *Handler) renderLoginError(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	if err := views.LoginPage(appI18n.T(r.Context(), "LoginError")).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}
