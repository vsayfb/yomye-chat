package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

// Recovery converts panics from request handlers into a generic HTTP 500
// response when the response has not already started. Panic values are not
// logged because they may contain request-derived or otherwise sensitive data.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)

		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(
					r.Context(),
					"request panic recovered",
					"request_id", chimiddleware.GetReqID(r.Context()),
					"method", r.Method,
					"path", r.URL.Path,
					"panic_type", fmt.Sprintf("%T", recovered),
					"stack", string(debug.Stack()),
				)

				if rw.Status() == 0 {
					rw.Header().Set("Content-Type", "application/json")
					rw.WriteHeader(http.StatusInternalServerError)
					_, _ = rw.Write([]byte("{\"error\":\"internal server error\"}\n"))
				}
			}
		}()

		next.ServeHTTP(rw, r)
	})
}
