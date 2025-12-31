package httpserver

import (
	"net/http"
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
