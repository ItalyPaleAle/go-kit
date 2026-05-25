package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeOrderMW returns a middleware that appends the given label to order on entry and again on exit
// It is the standard helper used across the order tests to capture the precise wrapping sequence
func makeOrderMW(order *[]string, label string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*order = append(*order, label+"-before")
			next.ServeHTTP(w, r)
			*order = append(*order, label+"-after")
		})
	}
}

// makeOrderHandler returns a terminal handler that records "handler" in order and writes the given message
func makeOrderHandler(order *[]string, message string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*order = append(*order, "handler")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(message)) //nolint:errcheck
	}
}

func TestMux_NewMux(t *testing.T) {
	m := NewMux()
	require.NotNil(t, m)
	require.NotNil(t, m.ServeMux())
	assert.Empty(t, m.prefix)
	assert.Empty(t, m.middlewares)
}

func TestMux_NewMuxFromServeMux(t *testing.T) {
	sm := http.NewServeMux()
	m := NewMuxFromServeMux(sm)
	require.NotNil(t, m)
	assert.Same(t, sm, m.ServeMux())
}

func TestMux_HandleNoPrefix(t *testing.T) {
	m := NewMux()
	m.HandleFunc("GET /hi", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok")) //nolint:errcheck
	})

	req := httptest.NewRequest(http.MethodGet, "/hi", nil)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestMux_GroupPrefix(t *testing.T) {
	t.Run("Simple prefix", func(t *testing.T) {
		m := NewMux()
		v1 := m.Group("/v1")
		v1.HandleFunc("GET /hi", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("v1-hi")) //nolint:errcheck
		})

		req := httptest.NewRequest(http.MethodGet, "/v1/hi", nil)
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "v1-hi", rec.Body.String())
	})

	t.Run("Different methods route independently", func(t *testing.T) {
		m := NewMux()
		v1 := m.Group("/v1")
		v1.HandleFunc("GET /resource", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("get")) //nolint:errcheck
		})
		v1.HandleFunc("POST /resource", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("post")) //nolint:errcheck
		})

		getReq := httptest.NewRequest(http.MethodGet, "/v1/resource", nil)
		getRec := httptest.NewRecorder()
		m.ServeHTTP(getRec, getReq)
		assert.Equal(t, http.StatusOK, getRec.Code)
		assert.Equal(t, "get", getRec.Body.String())

		postReq := httptest.NewRequest(http.MethodPost, "/v1/resource", nil)
		postRec := httptest.NewRecorder()
		m.ServeHTTP(postRec, postReq)
		assert.Equal(t, http.StatusCreated, postRec.Code)
		assert.Equal(t, "post", postRec.Body.String())
	})

	t.Run("Multiple groups are independent", func(t *testing.T) {
		m := NewMux()
		v1 := m.Group("/v1")
		v2 := m.Group("/v2")
		v1.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("v1")) //nolint:errcheck
		})
		v2.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("v2")) //nolint:errcheck
		})

		req1 := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
		rec1 := httptest.NewRecorder()
		m.ServeHTTP(rec1, req1)
		assert.Equal(t, "v1", rec1.Body.String())

		req2 := httptest.NewRequest(http.MethodGet, "/v2/ping", nil)
		rec2 := httptest.NewRecorder()
		m.ServeHTTP(rec2, req2)
		assert.Equal(t, "v2", rec2.Body.String())
	})

	t.Run("Nested groups concatenate prefixes", func(t *testing.T) {
		m := NewMux()
		api := m.Group("/api")
		v1 := api.Group("/v1")
		users := v1.Group("/users")
		users.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("me")) //nolint:errcheck
		})

		req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "me", rec.Body.String())
	})
}

func TestMux_PrefixNormalization(t *testing.T) {
	cases := []struct {
		name       string
		groupPath  string
		routePath  string
		requestURL string
	}{
		{"Plain", "/v1", "GET /hi", "/v1/hi"},
		{"Trailing slash on prefix", "/v1/", "GET /hi", "/v1/hi"},
		{"Missing leading slash on prefix", "v1", "GET /hi", "/v1/hi"},
		{"Missing both slashes on prefix", "v1/", "GET /hi", "/v1/hi"},
		{"Path without leading slash", "/v1", "GET hi", "/v1/hi"},
		{"Empty prefix acts as no-op", "", "GET /hi", "/hi"},
		{"Slash-only prefix is empty", "/", "GET /hi", "/hi"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMux()
			g := m.Group(tc.groupPath)
			g.HandleFunc(tc.routePath, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("ok")) //nolint:errcheck
			})

			req := httptest.NewRequest(http.MethodGet, tc.requestURL, nil)
			rec := httptest.NewRecorder()
			m.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code, "expected route to be registered at %s", tc.requestURL)
			assert.Equal(t, "ok", rec.Body.String())
		})
	}
}

func TestMux_GroupMiddlewareOrder(t *testing.T) {
	t.Run("Single middleware wraps handler", func(t *testing.T) {
		order := []string{}
		mw := makeOrderMW(&order, "g1")
		m := NewMux()
		g := m.Group("/v1", mw)
		g.HandleFunc("GET /x", makeOrderHandler(&order, "ok"))

		req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, req)

		assert.Equal(t, []string{"g1-before", "handler", "g1-after"}, order)
	})

	t.Run("Multiple middlewares in one Group: last is outer", func(t *testing.T) {
		order := []string{}
		g1 := makeOrderMW(&order, "g1")
		g2 := makeOrderMW(&order, "g2")
		m := NewMux()
		g := m.Group("/v1", g1, g2)
		g.HandleFunc("GET /x", makeOrderHandler(&order, "ok"))

		req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, req)

		// g2 is the last in the variadic, so it wraps g1 from the outside, matching the existing Use semantics
		expected := []string{"g2-before", "g1-before", "handler", "g1-after", "g2-after"}
		assert.Equal(t, expected, order)
	})

	t.Run("Nested groups: parent wraps child", func(t *testing.T) {
		order := []string{}
		p1 := makeOrderMW(&order, "p1")
		p2 := makeOrderMW(&order, "p2")
		c1 := makeOrderMW(&order, "c1")
		c2 := makeOrderMW(&order, "c2")

		m := NewMux()
		parent := m.Group("/v1", p1, p2)
		child := parent.Group("/sub", c1, c2)
		child.HandleFunc("GET /x", makeOrderHandler(&order, "ok"))

		req := httptest.NewRequest(http.MethodGet, "/v1/sub/x", nil)
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, req)

		// Parent middlewares wrap the child's; within each level the last-listed is outermost
		expected := []string{
			"p2-before", "p1-before",
			"c2-before", "c1-before",
			"handler",
			"c1-after", "c2-after",
			"p1-after", "p2-after",
		}
		assert.Equal(t, expected, order)
	})

	t.Run("Route middlewares are innermost", func(t *testing.T) {
		order := []string{}
		g := makeOrderMW(&order, "g")
		r1 := makeOrderMW(&order, "r1")
		r2 := makeOrderMW(&order, "r2")

		m := NewMux()
		grp := m.Group("/v1", g)
		grp.HandleFunc("GET /x", makeOrderHandler(&order, "ok"), r1, r2)

		req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, req)

		// Group middleware wraps the route middlewares; within the route list r2 wraps r1
		expected := []string{
			"g-before",
			"r2-before", "r1-before",
			"handler",
			"r1-after", "r2-after",
			"g-after",
		}
		assert.Equal(t, expected, order)
	})

	t.Run("Subgroups created later do not affect siblings", func(t *testing.T) {
		// The child group inherits a snapshot of the parent's middlewares
		// Adding a sibling subgroup must not retroactively change earlier subgroups
		order := []string{}
		p := makeOrderMW(&order, "p")
		c1 := makeOrderMW(&order, "c1")
		_ = makeOrderMW(&order, "c2") // not used directly, just to exercise the path

		m := NewMux()
		parent := m.Group("/v1", p)
		first := parent.Group("/first", c1)
		first.HandleFunc("GET /x", makeOrderHandler(&order, "ok"))

		// Register a sibling group with different middlewares; should not affect first
		second := parent.Group("/second", makeOrderMW(&order, "c2"))
		second.HandleFunc("GET /x", makeOrderHandler(&order, "ok"))

		req := httptest.NewRequest(http.MethodGet, "/v1/first/x", nil)
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, req)

		expected := []string{"p-before", "c1-before", "handler", "c1-after", "p-after"}
		assert.Equal(t, expected, order)
	})
}

func TestMux_HandleWithHandlerVsHandleFunc(t *testing.T) {
	// Handle accepts an http.Handler; HandleFunc accepts an http.HandlerFunc.
	// Both should produce identical behavior
	m := NewMux()

	m.Handle("GET /a", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("a")) //nolint:errcheck
	}))
	m.HandleFunc("GET /b", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("b")) //nolint:errcheck
	})

	reqA := httptest.NewRequest(http.MethodGet, "/a", nil)
	recA := httptest.NewRecorder()
	m.ServeHTTP(recA, reqA)
	assert.Equal(t, "a", recA.Body.String())

	reqB := httptest.NewRequest(http.MethodGet, "/b", nil)
	recB := httptest.NewRecorder()
	m.ServeHTTP(recB, reqB)
	assert.Equal(t, "b", recB.Body.String())
}

func TestMux_PatternFormats(t *testing.T) {
	t.Run("Method only", func(t *testing.T) {
		m := NewMux()
		v1 := m.Group("/v1")
		v1.HandleFunc("POST /submit", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/submit", nil)
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusAccepted, rec.Code)
	})

	t.Run("Method with host", func(t *testing.T) {
		// http.ServeMux supports patterns with an explicit host: "GET host.com/path"
		// The prefix must be inserted between the host and the path, not before the host
		m := NewMux()
		v1 := m.Group("/v1")
		v1.HandleFunc("GET example.com/hi", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("host-matched")) //nolint:errcheck
		})

		req := httptest.NewRequest(http.MethodGet, "/v1/hi", nil)
		req.Host = "example.com"
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "host-matched", rec.Body.String())
	})

	t.Run("No method", func(t *testing.T) {
		// Patterns without a method match any method, matching standard ServeMux behavior
		m := NewMux()
		v1 := m.Group("/v1")
		v1.HandleFunc("/anything", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(r.Method)) //nolint:errcheck,gosec
		})

		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
			req := httptest.NewRequest(method, "/v1/anything", nil)
			rec := httptest.NewRecorder()
			m.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, method, rec.Body.String())
		}
	})

	t.Run("Path with wildcard", func(t *testing.T) {
		// Go 1.22+ supports {name} path wildcards; the prefix must preserve them
		m := NewMux()
		v1 := m.Group("/v1")
		v1.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(r.PathValue("id"))) //nolint:errcheck,gosec
		})

		req := httptest.NewRequest(http.MethodGet, "/v1/users/42", nil)
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "42", rec.Body.String())
	})
}

func TestMux_ServeMuxAccessor(t *testing.T) {
	// Users that need direct access to the underlying ServeMux (e.g. for routes that should bypass group middleware)
	// can register handlers on it directly. Those routes will not see any group middlewares
	m := NewMux()
	v1 := m.Group("/v1", makeOrderMW(new([]string), "x"))

	// Register a handler directly on the underlying ServeMux through v1
	v1.ServeMux().HandleFunc("GET /raw", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("raw")) //nolint:errcheck
	})

	req := httptest.NewRequest(http.MethodGet, "/raw", nil)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "raw", rec.Body.String())
}

func TestMux_NoMiddlewareNoOverhead(t *testing.T) {
	// When no middlewares are registered, the handler should still work and receive requests normally
	called := false
	m := NewMux()
	v1 := m.Group("/v1")
	v1.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
}
