package handler

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

func TestAdminCannotDeactivateSelfAndCanLogInAgain(t *testing.T) {
	for _, basePath := range []string{"", "/ru"} {
		t.Run("base="+basePath, func(t *testing.T) {
			lang := "en"
			if basePath == "/ru" {
				lang = "ru"
			}
			if err := i18n.Init(lang); err != nil {
				t.Fatal(err)
			}
			s, err := store.New(filepath.Join(t.TempDir(), "examiner.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			const password = "test-password"
			hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
			if err != nil {
				t.Fatal(err)
			}
			adminID, err := s.CreateUser(model.User{
				Username: "admin", PasswordHash: string(hash), Role: model.UserRoleAdmin, Active: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			studentID, err := s.CreateUser(model.User{Username: "student", Role: model.UserRoleStudent, Active: true})
			if err != nil {
				t.Fatal(err)
			}
			h := &Handler{store: s, config: model.ExamConfig{BasePath: basePath}}
			r := chi.NewRouter()
			r.Use(h.BasePathMiddleware, i18n.Middleware(lang))
			if basePath == "" {
				h.Routes(r)
			} else {
				r.Route(basePath, h.Routes)
			}
			server := httptest.NewServer(r)
			t.Cleanup(server.Close)
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			client := server.Client()
			client.Jar = jar
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			request := func(path string, form url.Values, want int) string {
				t.Helper()
				var response *http.Response
				var err error
				if form == nil {
					response, err = client.Get(server.URL + basePath + path)
				} else {
					response, err = client.PostForm(server.URL+basePath+path, form)
				}
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = response.Body.Close() }()
				body, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != want {
					t.Fatalf("%s: status %d, want %d", path, response.StatusCode, want)
				}
				return string(body)
			}
			token := func() string {
				t.Helper()
				body := request("/login", nil, http.StatusOK)
				match := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(body)
				if len(match) != 2 {
					t.Fatal("login page has no CSRF token")
				}
				return match[1]
			}
			login := func() {
				t.Helper()
				request("/login", url.Values{
					"username": {"admin"}, "password": {password}, "csrf_token": {token()},
				}, http.StatusSeeOther)
			}
			login()
			page := request("/admin/users", nil, http.StatusOK)
			selfAction := fmt.Sprintf(`action="%s/admin/users/%d/toggle"`, basePath, adminID)
			studentAction := fmt.Sprintf(`action="%s/admin/users/%d/toggle"`, basePath, studentID)
			if strings.Contains(page, selfAction) {
				t.Error("user management must not offer a self-deactivation form")
			}
			if !strings.Contains(page, studentAction) {
				t.Error("user management must offer a toggle form for other users")
			}
			if !strings.Contains(page, "<td>admin</td>") {
				t.Error("user management must list the admin row")
			}
			cannotDeactivate := "You cannot deactivate your own account."
			if lang == "ru" {
				cannotDeactivate = "Свою учётную запись отключить нельзя."
			}
			if !strings.Contains(page, cannotDeactivate) {
				t.Error("user management must explain the self-deactivation restriction")
			}
			body := request(fmt.Sprintf("/admin/users/%d/toggle", adminID),
				url.Values{"csrf_token": {token()}}, http.StatusForbidden)
			if !strings.Contains(body, cannotDeactivate) {
				t.Error("self-deactivation response must explain the restriction in the selected language")
			}
			admin, err := s.GetUserByID(adminID)
			if err != nil || admin == nil || !admin.Active || admin.PasswordHash != string(hash) {
				t.Fatal("self-deactivation attempt changed admin credentials or activity", err)
			}
			request("/admin/users", nil, http.StatusOK)
			// Управление активностью другого пользователя остаётся доступным.
			for _, wantActive := range []bool{false, true} {
				request(fmt.Sprintf("/admin/users/%d/toggle", studentID),
					url.Values{"csrf_token": {token()}}, http.StatusSeeOther)
				student, err := s.GetUserByID(studentID)
				if err != nil || student == nil || student.Active != wantActive {
					t.Fatalf("student activity after toggle: want %v, error %v", wantActive, err)
				}
			}
			for range 2 {
				request("/logout", url.Values{"csrf_token": {token()}}, http.StatusSeeOther)
				request("/admin/users", nil, http.StatusSeeOther)
				login()
				request("/admin/users", nil, http.StatusOK)
			}
		})
	}
}

func TestAdminCreateUserReturnsConflictForDuplicateUsername(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h := &Handler{store: s}
	form := url.Values{"username": {"duplicate"}, "password": {"test-password"}, "role": {"student"}}
	for _, want := range []int{http.StatusSeeOther, http.StatusConflict} {
		req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.handleCreateUser(rec, req)
		if rec.Code != want {
			t.Fatalf("status = %d, want %d: %s", rec.Code, want, rec.Body.String())
		}
	}
}
