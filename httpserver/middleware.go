package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Middleware type is a function that takes an http.Handler and returns another http.Handler
type Middleware func(next http.Handler) http.Handler

// MiddlewareFunc type is a function that takes an http.HandlerFunc and returns another http.HandlerFunc
type MiddlewareFunc func(next http.HandlerFunc) http.HandlerFunc

// Use applies middlewares to the handler
func Use(h http.Handler, middlewares ...Middleware) http.Handler {
	for _, middleware := range middlewares {
		h = middleware(h)
	}
	return h
}

// UseFunc applies middlewares (of type http.HandlerFunc) to the handler
func UseFunc(h http.HandlerFunc, middlewares ...MiddlewareFunc) http.HandlerFunc {
	for _, middleware := range middlewares {
		h = middleware(h)
	}
	return h
}

// MiddlewareMaxBodySize is a middleware that limits the size of the request body
func MiddlewareMaxBodySize(maxSize int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxSize)
			next.ServeHTTP(w, r)
		})
	}
}

// MiddlewareHostIDHeader is a middleware that adds the X-Host-ID header
func MiddlewareHostIDHeader(hostID string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Add(HeaderXHostID, hostID)
			next.ServeHTTP(w, req)
		})
	}
}

// MiddlewareRecoverPanics is a middleware that recovers from panics in the next handlers, logs them with their stack trace, and responds with errResponse
// The panic is logged with the default slog logger and the request's context, so the log is exported like every other log and keeps the request's trace
// Panics with http.ErrAbortHandler are re-panicked, because handlers use them to abort a response on purpose and net/http handles them without logging
func MiddlewareRecoverPanics(errResponse ApiError) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() { //nolint:contextcheck // The request's context is used through r
				p := recover()
				if p == nil {
					return
				}

				err, ok := p.(error)
				if ok && errors.Is(err, http.ErrAbortHandler) {
					panic(p)
				}

				slog.ErrorContext(r.Context(), "Panic while handling the request",
					slog.Any("panic", p),
					slog.String("stack", string(debug.Stack())),
				)
				errResponse.WriteResponse(w, r)
			}()

			next.ServeHTTP(w, r)
		})
	}
}
