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

// commitTrackingResponseWriter records whether response headers or body bytes have already been sent
type commitTrackingResponseWriter struct {
	http.ResponseWriter

	committed bool
}

func (cw *commitTrackingResponseWriter) WriteHeader(statusCode int) {
	cw.committed = true
	cw.ResponseWriter.WriteHeader(statusCode)
}

func (cw *commitTrackingResponseWriter) Write(p []byte) (int, error) {
	cw.committed = true
	return cw.ResponseWriter.Write(p)
}

func (cw *commitTrackingResponseWriter) Flush() {
	cw.committed = true
	flusher, ok := cw.ResponseWriter.(http.Flusher)
	if ok {
		flusher.Flush()
	}
}

func (cw *commitTrackingResponseWriter) Unwrap() http.ResponseWriter {
	return cw.ResponseWriter
}

// MiddlewareRecoverPanics is a middleware that recovers from panics in downstream handlers, logs them with a stack trace, and writes errResponse
// Panics with http.ErrAbortHandler, or panics after the response has already been committed, abort the connection via http.ErrAbortHandler
func MiddlewareRecoverPanics(errResponse ApiError) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cw := &commitTrackingResponseWriter{ResponseWriter: w}
			defer func() { //nolint:contextcheck // Uses r.Context()
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
				if cw.committed {
					panic(http.ErrAbortHandler)
				}
				errResponse.WriteResponse(cw, r)
			}()

			next.ServeHTTP(cw, r)
		})
	}
}
