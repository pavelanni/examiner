package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/time/rate"

	"github.com/pavelanni/examiner/internal/handler/views"
	appI18n "github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

var registrationUsername = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{2,63}$`)

// registrationAdmission ограничивает дорогое хеширование в пределах одного процесса.
// Общий bucket не доверяет подделываемым forwarded-заголовкам: 1 запрос/с,
// burst 4 и максимум 2 одновременных операции без очереди ожидания.
type registrationAdmission struct {
	once    sync.Once
	limiter *rate.Limiter
	slots   chan struct{}
}

// acquire отклоняет перегрузку до обращения к хранилищу или bcrypt.
func (a *registrationAdmission) acquire() bool {
	a.once.Do(func() {
		a.limiter = rate.NewLimiter(rate.Every(time.Second), 4)
		a.slots = make(chan struct{}, 2)
	})
	if !a.limiter.Allow() {
		return false
	}
	select {
	case a.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (a *registrationAdmission) release() { <-a.slots }

// registrationFormLimit ограничивает тело до разбора CSRF-токена и полей формы.
func registrationFormLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
			if err := r.ParseForm(); err != nil {
				status := http.StatusBadRequest
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					status = http.StatusRequestEntityTooLarge
				}
				http.Error(w, appI18n.T(r.Context(), "RegistrationInvalidForm"), status)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// handleRegisterPage показывает форму только при включённой саморегистрации.
func (h *Handler) handleRegisterPage(w http.ResponseWriter, r *http.Request) {
	h.renderRegistration(w, r, "", "", "", http.StatusOK)
}

// handleRegister создаёт исключительно активного студента и не принимает роль из формы.
func (h *Handler) handleRegister(w http.ResponseWriter, r *http.Request) {
	username := r.PostFormValue("username")
	displayName := strings.TrimSpace(r.PostFormValue("display_name"))
	password := r.PostFormValue("password")
	confirmation := r.PostFormValue("password_confirm")
	errorKey := ""
	switch {
	case !registrationUsername.MatchString(username):
		errorKey = "RegistrationUsernameInvalid"
	case displayName == "" || utf8.RuneCountInString(displayName) > 120:
		errorKey = "RegistrationNameInvalid"
	case utf8.RuneCountInString(password) < 8:
		errorKey = "RegistrationPasswordShort"
	case len(password) > 72:
		errorKey = "RegistrationPasswordLong"
	case password != confirmation:
		errorKey = "RegistrationPasswordMismatch"
	}
	if errorKey != "" {
		h.renderRegistration(w, r, username, displayName, errorKey, http.StatusBadRequest)
		return
	}
	if !h.registration.acquire() {
		w.Header().Set("Retry-After", "1")
		h.renderRegistration(w, r, username, displayName, "RegistrationRateLimited", http.StatusTooManyRequests)
		return
	}
	defer h.registration.release()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		slog.Error("failed to hash registration password", "error", err)
		h.renderRegistration(w, r, username, displayName, "RegistrationUnavailable", http.StatusInternalServerError)
		return
	}
	_, err = h.store.CreateUser(model.User{Username: username, DisplayName: displayName, PasswordHash: string(hash), Role: model.UserRoleStudent, Active: true})
	if errors.Is(err, store.ErrUsernameTaken) {
		h.renderRegistration(w, r, username, displayName, "RegistrationUsernameTaken", http.StatusConflict)
		return
	}
	if err != nil {
		slog.Error("student registration failed", "error", err)
		h.renderRegistration(w, r, username, displayName, "RegistrationUnavailable", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, h.path("/login?registered=1"), http.StatusSeeOther)
}

// renderRegistration сохраняет логин и имя при ошибке, но не возвращает пароли в HTML.
func (h *Handler) renderRegistration(w http.ResponseWriter, r *http.Request, username, displayName, errorKey string, status int) {
	errorMsg := ""
	if errorKey != "" {
		errorMsg = appI18n.T(r.Context(), errorKey)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := views.RegisterPage(username, displayName, errorMsg).Render(r.Context(), w); err != nil {
		slog.Error("render registration page", "error", err)
	}
}
