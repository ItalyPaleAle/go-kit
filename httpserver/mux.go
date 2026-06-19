package httpserver

import (
	"net/http"
	"strings"
)

// Mux extends http.ServeMux with support for route groups, path prefixes, and per-group middlewares
// It implements http.Handler so it can be passed wherever an http.Handler is expected
// The zero value is not usable
// Construct one with NewMux or NewMuxFromServeMux
type Mux struct {
	mux    *http.ServeMux
	prefix string

	// middlewares stored in the order to pass to Use: last entry is outermost
	// For nested groups the parent's middlewares come after the child's own entries,
	// so the parent wraps the child once Use composes them
	middlewares []Middleware
}

// NewMux returns a Mux backed by a fresh http.ServeMux
func NewMux() *Mux {
	return &Mux{
		mux: http.NewServeMux(),
	}
}

// NewMuxFromServeMux wraps an existing *http.ServeMux
// Useful when other code already holds a ServeMux and registers routes on it directly
func NewMuxFromServeMux(mux *http.ServeMux) *Mux {
	return &Mux{
		mux: mux,
	}
}

// ServeMux returns the underlying *http.ServeMux
func (m *Mux) ServeMux() *http.ServeMux {
	return m.mux
}

// ServeHTTP implements http.Handler by delegating to the underlying ServeMux
func (m *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mux.ServeHTTP(w, r)
}

// Group returns a sub-Mux that shares the same underlying ServeMux but prepends prefix to all routes registered through it
// The new group inherits a snapshot of the parent's middlewares: later changes to the parent (e.g. additional sub-groups) do not affect this group
// The middlewares passed here are inner of (i.e. wrapped by) the parent's middlewares
// Within the supplied list the last entry is outermost, matching the semantics of the package-level Use function
func (m *Mux) Group(prefix string, middlewares ...Middleware) *Mux {
	combined := make([]Middleware, 0, len(middlewares)+len(m.middlewares))
	combined = append(combined, middlewares...)
	combined = append(combined, m.middlewares...)

	return &Mux{
		mux:         m.mux,
		prefix:      m.prefix + normalizePrefix(prefix),
		middlewares: combined,
	}
}

// Handle registers handler for the given pattern
// The pattern follows the standard library format: "[METHOD ][HOST]/PATH"
// The group's prefix is inserted before PATH (PATH is given a leading slash if missing)
// Per-route middlewares apply inside the group's middlewares (they are wrapped by them)
// Within the per-route list the last entry is outermost
func (m *Mux) Handle(pattern string, handler http.Handler, middlewares ...Middleware) {
	full := m.applyPrefix(pattern)

	// Compose: route middlewares first (inner), then group middlewares (outer)
	// Use applies them in slice order so last entry ends up outermost
	all := make([]Middleware, 0, len(middlewares)+len(m.middlewares))
	all = append(all, middlewares...)
	all = append(all, m.middlewares...)

	m.mux.Handle(full, Use(handler, all...))
}

// HandleFunc is the http.HandlerFunc variant of Handle
func (m *Mux) HandleFunc(pattern string, handler http.HandlerFunc, middlewares ...Middleware) {
	m.Handle(pattern, handler, middlewares...)
}

// normalizePrefix ensures the prefix has a leading slash and no trailing slash
// An empty input returns an empty string so a Group with no prefix acts as a pure middleware scope
func normalizePrefix(p string) string {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// applyPrefix splits a "[METHOD ][HOST]/PATH" pattern and inserts m.prefix before PATH
// Each segment is preserved: the method (with its trailing space), the optional host, then the prefixed path
func (m *Mux) applyPrefix(pattern string) string {
	// Split off the optional method, which is everything up to the first space
	methodPart := ""
	rest := pattern
	spaceIdx := strings.Index(pattern, " ")
	if spaceIdx >= 0 {
		methodPart = pattern[:spaceIdx+1]
		rest = pattern[spaceIdx+1:]
	}

	// Split off the optional host, which appears before the first slash when present
	hostPart := ""
	path := rest
	if !strings.HasPrefix(rest, "/") {
		slashIdx := strings.Index(rest, "/")
		if slashIdx >= 0 {
			hostPart = rest[:slashIdx]
			path = rest[slashIdx:]
		} else {
			// No slash anywhere in rest: treat the entire remainder as a path missing its leading slash
			path = "/" + rest
		}
	}

	if m.prefix == "" {
		return methodPart + hostPart + path
	}
	return methodPart + hostPart + m.prefix + path
}
