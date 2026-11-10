package handler

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/time/rate"

	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

type registrationEnv struct {
	handler *Handler
	store   *store.Store
	server  *httptest.Server
	client  *http.Client
	prefix  string
}

// newRegistrationEnv использует настоящий роутер, SQLite и cookies браузера.
func newRegistrationEnv(t *testing.T, enabled bool, prefix, lang string, secure bool) *registrationEnv {
	t.Helper()
	if err := i18n.Init(lang); err != nil {
		t.Fatal(err)
	}
	s, err := store.New(filepath.Join(t.TempDir(), "registration.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h := &Handler{store: s, config: model.ExamConfig{AllowRegistration: enabled, BasePath: prefix, SecureCookies: secure}}
	r := chi.NewRouter()
	r.Use(i18n.Middleware(lang))
	if prefix == "" {
		r.Use(h.BasePathMiddleware)
		h.Routes(r)
	} else {
		r.Route(prefix, func(sub chi.Router) { sub.Use(h.BasePathMiddleware); h.Routes(sub) })
	}
	var server *httptest.Server
	if secure {
		server = httptest.NewTLSServer(r)
	} else {
		server = httptest.NewServer(r)
	}
	t.Cleanup(server.Close)
	client := server.Client()
	client.Jar, err = cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &registrationEnv{handler: h, store: s, server: server, client: client, prefix: prefix}
}

// request сохраняет статус, cookies и редиректы для проверок полного сценария.
func (e *registrationEnv) request(t *testing.T, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	var resp *http.Response
	var err error
	if form == nil {
		resp, err = e.client.Get(e.server.URL + e.prefix + path)
	} else {
		resp, err = e.client.PostForm(e.server.URL+e.prefix+path, form)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

// token получает CSRF из настоящей формы и соответствующий cookie через jar.
func (e *registrationEnv) token(t *testing.T) string {
	t.Helper()
	resp, body := e.request(t, "/register", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register GET: %d", resp.StatusCode)
	}
	match := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("registration form has no CSRF token")
	}
	return match[1]
}

func registrationForm(token string) url.Values {
	return url.Values{"csrf_token": {token}, "username": {"Student.One"}, "display_name": {"Иван Петров"}, "password": {"long-password"}, "password_confirm": {"long-password"}}
}

// TestStudentRegistrationAndLogin проверяет создание студента, cookies и запрет повышения роли.
func TestStudentRegistrationAndLogin(t *testing.T) {
	for _, tc := range []struct {
		prefix, lang string
		secure       bool
	}{{"", "en", false}, {"/ru", "ru", true}} {
		t.Run(tc.lang, func(t *testing.T) {
			env := newRegistrationEnv(t, true, tc.prefix, tc.lang, tc.secure)
			resp, body := env.request(t, "/login", nil)
			if resp.StatusCode != 200 || !strings.Contains(body, `href="`+tc.prefix+`/register"`) {
				t.Fatal("login page does not offer registration")
			}
			token := env.token(t)
			form := registrationForm(token)
			form.Set("role", "admin")
			form.Set("active", "false")
			form.Set("external_id", "impersonated-roster-id")
			resp, body = env.request(t, "/register", form)
			if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != tc.prefix+"/login?registered=1" {
				t.Fatalf("registration: %d %q %s", resp.StatusCode, resp.Header.Get("Location"), body)
			}
			for _, cookie := range resp.Cookies() {
				if cookie.Name == sessionCookieName {
					t.Fatal("registration must not reuse or create a login session")
				}
			}
			user, err := env.store.GetUserByUsername("Student.One")
			if err != nil {
				t.Fatal(err)
			}
			if user == nil || user.Role != model.UserRoleStudent || !user.Active || user.ExternalID != "" || user.DisplayName != "Иван Петров" {
				t.Fatalf("unexpected student: %+v", user)
			}
			if user.PasswordHash == form.Get("password") || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(form.Get("password"))) != nil {
				t.Fatal("password was not hashed correctly")
			}
			resp, body = env.request(t, "/login?registered=1", nil)
			success := "Your account has been created."
			if tc.lang == "ru" {
				success = "Учётная запись создана."
			}
			if resp.StatusCode != 200 || !strings.Contains(body, success) {
				t.Fatal("missing localized confirmation")
			}
			resp, body = env.request(t, "/login", url.Values{"csrf_token": {token}, "username": {"Student.One"}, "password": {"long-password"}})
			if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != tc.prefix+"/" {
				t.Fatalf("login: %d %s", resp.StatusCode, body)
			}
			found := false
			for _, cookie := range resp.Cookies() {
				if cookie.Name == sessionCookieName {
					found = true
					if cookie.Path != tc.prefix+"/" || cookie.Secure != tc.secure || !cookie.HttpOnly {
						t.Fatalf("bad session cookie: %+v", cookie)
					}
				}
			}
			if !found {
				t.Fatal("login did not create a session")
			}
			resp, _ = env.request(t, "/", nil)
			if resp.StatusCode != 200 {
				t.Fatalf("student cannot access home: %d", resp.StatusCode)
			}
			for _, path := range []string{"/admin/users", "/admin/questions", "/review"} {
				resp, _ = env.request(t, path, nil)
				if resp.StatusCode != http.StatusForbidden {
					t.Fatalf("student accessed %s: %d", path, resp.StatusCode)
				}
			}
		})
	}
}

// TestRegistrationDisabledByDefault закрывает маршруты и убирает ссылку при выключенной функции.
func TestRegistrationDisabledByDefault(t *testing.T) {
	env := newRegistrationEnv(t, false, "", "en", false)
	_, body := env.request(t, "/login", nil)
	if strings.Contains(body, `href="/register"`) {
		t.Fatal("disabled registration is advertised")
	}
	for _, form := range []url.Values{nil, registrationForm("token")} {
		resp, _ := env.request(t, "/register", form)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("disabled registration: %d", resp.StatusCode)
		}
	}
	count, err := env.store.UserCount()
	if err != nil || count != 0 {
		t.Fatalf("disabled registration created users: %d %v", count, err)
	}
}

// TestRegistrationValidation проверяет серверные ограничения и отсутствие пароля в ошибке.
func TestRegistrationValidation(t *testing.T) {
	cases := []struct{ name, field, value, confirm string }{
		{"short username", "username", "ab", ""}, {"invalid username", "username", "bad name", ""}, {"long username", "username", strings.Repeat("a", 65), ""},
		{"blank name", "display_name", "  ", ""}, {"long name", "display_name", strings.Repeat("Ж", 121), ""},
		{"short password", "password", "short", "short"},
		{"short multibyte password", "password", strings.Repeat("Ж", 7), strings.Repeat("Ж", 7)}, {"long password", "password", strings.Repeat("a", 73), strings.Repeat("a", 73)},
		{"UTF8 byte limit", "password", strings.Repeat("Ж", 37), strings.Repeat("Ж", 37)},
		{"mismatch", "password_confirm", "different-password", ""},
	}
	env := newRegistrationEnv(t, true, "/ru", "ru", false)
	token := env.token(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := registrationForm(token)
			form.Set(tc.field, tc.value)
			if tc.confirm != "" {
				form.Set("password_confirm", tc.confirm)
			}
			resp, body := env.request(t, "/register", form)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status=%d, body=%s", resp.StatusCode, body)
			}
			if strings.Contains(body, `value="long-password"`) || strings.Contains(body, `value="different-password"`) {
				t.Fatal("password leaked into the form")
			}
			if !strings.Contains(body, `action="/ru/register"`) {
				t.Fatal("form lost the base path on error")
			}
		})
	}
	count, err := env.store.UserCount()
	if err != nil || count != 0 {
		t.Fatalf("invalid form created a user: %d %v", count, err)
	}
}

// TestRegistrationCSRFAndBodyLimit отсекает поддельные и чрезмерно большие запросы до создания пользователя.
func TestRegistrationCSRFAndBodyLimit(t *testing.T) {
	env := newRegistrationEnv(t, true, "", "en", false)
	token := env.token(t)
	for _, value := range []string{"", "wrong"} {
		form := registrationForm(value)
		resp, _ := env.request(t, "/register", form)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("CSRF: %d", resp.StatusCode)
		}
	}
	form := registrationForm(token)
	form.Set("display_name", strings.Repeat("x", 9<<10))
	resp, _ := env.request(t, "/register", form)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized form: %d", resp.StatusCode)
	}
	count, err := env.store.UserCount()
	if err != nil || count != 0 {
		t.Fatalf("rejected form created users: %d %v", count, err)
	}
}

// TestConcurrentRegistrationKeepsOneAccount проверяет атомарный конфликт логина при нескольких POST.
func TestConcurrentRegistrationKeepsOneAccount(t *testing.T) {
	env := newRegistrationEnv(t, true, "", "en", false)
	token := env.token(t)
	const clients = 2
	statuses := make(chan int, clients)
	var wg sync.WaitGroup
	for range clients {
		wg.Go(func() {
			resp, err := env.client.PostForm(env.server.URL+"/register", registrationForm(token))
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = resp.Body.Close() }()
			_, _ = io.Copy(io.Discard, resp.Body)
			statuses <- resp.StatusCode
		})
	}
	wg.Wait()
	close(statuses)
	created, duplicates := 0, 0
	for status := range statuses {
		switch status {
		case http.StatusSeeOther:
			created++
		case http.StatusConflict:
			duplicates++
		default:
			t.Errorf("unexpected status: %d", status)
		}
	}
	if created != 1 || duplicates != clients-1 {
		t.Fatalf("created=%d, duplicates=%d", created, duplicates)
	}
	count, err := env.store.UserCount()
	if err != nil || count != 1 {
		t.Fatalf("user count=%d, error=%v", count, err)
	}
	user, err := env.store.GetUserByUsername("Student.One")
	if err != nil || user == nil {
		t.Fatalf("missing account: %v", err)
	}
	if user.Role != model.UserRoleStudent || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("long-password")) != nil {
		t.Fatal("duplicate registration damaged the account")
	}
}

// TestRegistrationEscapesRetainedFields не исполняет разметку из ошибочной формы.
func TestRegistrationEscapesRetainedFields(t *testing.T) {
	env := newRegistrationEnv(t, true, "", "en", false)
	form := registrationForm(env.token(t))
	form.Set("display_name", `<script>alert("x")</script>`)
	form.Set("password_confirm", "mismatch")
	resp, body := env.request(t, "/register", form)
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if strings.Contains(body, `<script>alert("x")</script>`) {
		t.Fatal("unescaped name in response")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("name was not retained as escaped text")
	}
}

// TestRegistrationCannotOverwriteExistingAdmin защищает роль, пароль и активность существующей записи.
func TestRegistrationCannotOverwriteExistingAdmin(t *testing.T) {
	env := newRegistrationEnv(t, true, "", "en", false)
	hash, err := bcrypt.GenerateFromPassword([]byte("administrator-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := env.store.CreateUser(model.User{Username: "admin", DisplayName: "Existing Administrator", ExternalID: "admin-id", PasswordHash: string(hash), Role: model.UserRoleAdmin, Active: false})
	if err != nil {
		t.Fatal(err)
	}
	form := registrationForm(env.token(t))
	form.Set("username", "admin")
	form.Set("role", "admin")
	resp, body := env.request(t, "/register", form)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "already taken") {
		t.Fatalf("duplicate: %d %s", resp.StatusCode, body)
	}
	admin, err := env.store.GetUserByID(adminID)
	if err != nil {
		t.Fatal(err)
	}
	if admin == nil || admin.Role != model.UserRoleAdmin || admin.Active || admin.PasswordHash != string(hash) || admin.ExternalID != "admin-id" || admin.DisplayName != "Existing Administrator" {
		t.Fatalf("existing admin was changed: %+v", admin)
	}
}

// TestRegistrationBackpressure отклоняет перегрузку до создания пользователя и позволяет повторить запрос.
func TestRegistrationBackpressure(t *testing.T) {
	for _, kind := range []string{"rate", "concurrency"} {
		t.Run(kind, func(t *testing.T) {
			env := newRegistrationEnv(t, true, "", "en", false)
			token := env.token(t)
			admission := &env.handler.registration
			if !admission.acquire() {
				t.Fatal("initial admission rejected")
			}
			admission.release()
			held := 0
			t.Cleanup(func() {
				for held > 0 {
					admission.release()
					held--
				}
			})
			if kind == "rate" {
				admission.limiter = rate.NewLimiter(0, 0)
			} else {
				admission.limiter.SetLimit(rate.Inf)
				for range 2 {
					if !admission.acquire() {
						t.Fatal("could not fill hash slots")
					}
					held++
				}
			}
			resp, body := env.request(t, "/register", registrationForm(token))
			if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "1" {
				t.Fatalf("backpressure: %d %s", resp.StatusCode, body)
			}
			if !strings.Contains(body, "Too many registration attempts") {
				t.Fatal("missing overload explanation")
			}
			count, err := env.store.UserCount()
			if err != nil || count != 0 {
				t.Fatalf("rejected request created users: %d %v", count, err)
			}
			if kind == "concurrency" {
				admission.release()
				held--
			} else {
				admission.limiter = rate.NewLimiter(rate.Inf, 4)
			}
			resp, body = env.request(t, "/register", registrationForm(token))
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("retry: %d %s", resp.StatusCode, body)
			}
		})
	}
}

// TestRegistrationHashConcurrency удерживает слоты до завершения всех параллельных попыток допуска.
func TestRegistrationHashConcurrency(t *testing.T) {
	var admission registrationAdmission
	if !admission.acquire() {
		t.Fatal("initial admission rejected")
	}
	admission.release()
	admission.limiter.SetLimit(rate.Inf)
	const clients = 32
	release := make(chan struct{})
	results := make(chan bool, clients)
	var wg sync.WaitGroup
	for range clients {
		wg.Go(func() {
			admitted := admission.acquire()
			results <- admitted
			if admitted {
				<-release
				admission.release()
			}
		})
	}
	admitted := 0
	for range clients {
		if <-results {
			admitted++
		}
	}
	close(release)
	wg.Wait()
	if admitted != 2 {
		t.Fatalf("concurrent password hashes=%d, want 2", admitted)
	}
	if !admission.acquire() {
		t.Fatal("hash slot leaked")
	}
	admission.release()
}

// TestRegistrationSupportsSupplementaryUnicode принимает допустимое имя и пароль без ограничения UTF-16.
func TestRegistrationSupportsSupplementaryUnicode(t *testing.T) {
	env := newRegistrationEnv(t, true, "", "en", false)
	_, page := env.request(t, "/register", nil)
	field := regexp.MustCompile(`<input[^>]*id="display_name"[^>]*>`).FindString(page)
	if field == "" || strings.Contains(field, "maxlength") {
		t.Fatal("browser rejects valid supplementary Unicode names")
	}
	form := registrationForm(env.token(t))
	name := strings.Repeat("𐍈", 120)
	form.Set("display_name", name)
	form.Set("password", strings.Repeat("Ж", 8))
	form.Set("password_confirm", strings.Repeat("Ж", 8))
	resp, body := env.request(t, "/register", form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("Unicode registration: %d %s", resp.StatusCode, body)
	}
	user, err := env.store.GetUserByUsername(form.Get("username"))
	if err != nil || user == nil || user.DisplayName != name {
		t.Fatal("Unicode name was not preserved", err)
	}
}
