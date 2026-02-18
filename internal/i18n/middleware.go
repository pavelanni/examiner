package i18n

import "net/http"

// Middleware injects the localizer for the given language into every request context.
func Middleware(lang string) func(http.Handler) http.Handler {
	loc := NewLocalizer(lang)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := WithLocalizer(r.Context(), loc)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
