package i18n

import "net/http"

// Middleware injects the localizer and the language code into every request context.
func Middleware(lang string) func(http.Handler) http.Handler {
	loc := NewLocalizer(lang)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := WithLang(WithLocalizer(r.Context(), loc), lang)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
