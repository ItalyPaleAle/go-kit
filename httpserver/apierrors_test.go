package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewApiError(t *testing.T) {
	tests := []struct {
		name       string
		code       string
		httpStatus int
		message    string
		wantCode   string
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "creates error with all fields",
			code:       "NOT_FOUND",
			httpStatus: http.StatusNotFound,
			message:    "Resource not found",
			wantCode:   "NOT_FOUND",
			wantStatus: http.StatusNotFound,
			wantMsg:    "Resource not found",
		},
		{
			name:       "creates error with empty message",
			code:       "INTERNAL_ERROR",
			httpStatus: http.StatusInternalServerError,
			message:    "",
			wantCode:   "INTERNAL_ERROR",
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "",
		},
		{
			name:       "creates error with custom status",
			code:       "RATE_LIMITED",
			httpStatus: http.StatusTooManyRequests,
			message:    "Too many requests",
			wantCode:   "RATE_LIMITED",
			wantStatus: http.StatusTooManyRequests,
			wantMsg:    "Too many requests",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewApiError(tt.code, tt.httpStatus, tt.message)

			assert.Equal(t, tt.wantCode, err.Code)
			assert.Equal(t, tt.wantStatus, err.httpStatus)
			assert.Equal(t, tt.wantMsg, err.Message)
			assert.Empty(t, err.InnerError)
			assert.Nil(t, err.Metadata)
		})
	}
}

func TestApiError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  *ApiError
		want string
	}{
		{
			name: "formats error message correctly",
			err:  NewApiError("NOT_FOUND", http.StatusNotFound, "Resource not found"),
			want: "API error (NOT_FOUND): Resource not found",
		},
		{
			name: "handles empty message",
			err:  NewApiError("ERROR", http.StatusInternalServerError, ""),
			want: "API error (ERROR): ",
		},
		{
			name: "handles special characters in message",
			err:  NewApiError("VALIDATION_ERROR", http.StatusBadRequest, "Invalid input: \"field\" is required"),
			want: `API error (VALIDATION_ERROR): Invalid input: "field" is required`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.err.Error())
		})
	}
}

func TestApiError_Is(t *testing.T) {
	err1 := NewApiError("NOT_FOUND", http.StatusNotFound, "Resource not found")
	err2 := NewApiError("NOT_FOUND", http.StatusNotFound, "Different message")
	err3 := NewApiError("UNAUTHORIZED", http.StatusUnauthorized, "Unauthorized")
	standardErr := errors.New("standard error")

	tests := []struct {
		name   string
		err    error
		target error
		want   bool
	}{
		{
			name:   "matches same error code",
			err:    err1,
			target: *err2,
			want:   true,
		},
		{
			name:   "matches itself",
			err:    err1,
			target: *err1,
			want:   true,
		},
		{
			name:   "does not match different error code",
			err:    err1,
			target: *err3,
			want:   false,
		},
		{
			name:   "does not match standard error",
			err:    err1,
			target: standardErr,
			want:   false,
		},
		{
			name:   "does not match nil",
			err:    err1,
			target: nil,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiErr, ok := tt.err.(ApiError)
			if !ok {
				apiErr = *tt.err.(*ApiError)
			}
			assert.Equal(t, tt.want, apiErr.Is(tt.target))
		})
	}
}

func TestApiError_WriteResponse(t *testing.T) {
	tests := []struct {
		name           string
		err            *ApiError
		wantStatus     int
		wantCode       string
		wantMessage    string
		wantInnerError bool
		wantMetadata   bool
	}{
		{
			name:        "writes simple error response",
			err:         NewApiError("NOT_FOUND", http.StatusNotFound, "Resource not found"),
			wantStatus:  http.StatusNotFound,
			wantCode:    "NOT_FOUND",
			wantMessage: "Resource not found",
		},
		{
			name: "writes error with inner error",
			err: NewApiError("INTERNAL_ERROR", http.StatusInternalServerError, "Database error").Clone(
				WithInnerError(errors.New("connection failed")),
			),
			wantStatus:     http.StatusInternalServerError,
			wantCode:       "INTERNAL_ERROR",
			wantMessage:    "Database error",
			wantInnerError: true,
		},
		{
			name: "writes error with metadata",
			err: NewApiError("VALIDATION_ERROR", http.StatusBadRequest, "Invalid input").Clone(
				WithMetadata(map[string]string{"field": "email", "reason": "invalid format"}),
			),
			wantStatus:   http.StatusBadRequest,
			wantCode:     "VALIDATION_ERROR",
			wantMessage:  "Invalid input",
			wantMetadata: true,
		},
		{
			name: "writes error with both inner error and metadata",
			err: NewApiError("CONFLICT", http.StatusConflict, "Resource conflict").Clone(
				WithInnerError(errors.New("duplicate key")),
				WithMetadata(map[string]string{"resource": "user", "id": "123"}),
			),
			wantStatus:     http.StatusConflict,
			wantCode:       "CONFLICT",
			wantMessage:    "Resource conflict",
			wantInnerError: true,
			wantMetadata:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			w := httptest.NewRecorder()

			tt.err.WriteResponse(w, req)

			resp := w.Result()
			t.Cleanup(func() { _ = resp.Body.Close() })

			assert.Equal(t, tt.wantStatus, resp.StatusCode)
			assert.Equal(t, ContentTypeJson, resp.Header.Get("Content-Type"))

			// Decode into a map since error fields cannot be unmarshaled directly
			var result map[string]any
			err := json.NewDecoder(resp.Body).Decode(&result)
			require.NoError(t, err)

			assert.Equal(t, tt.wantCode, result["code"])
			assert.Equal(t, tt.wantMessage, result["message"])

			if tt.wantInnerError {
				assert.Contains(t, result, "innerError")
				assert.NotNil(t, result["innerError"])
			} else {
				assert.NotContains(t, result, "innerError")
			}

			if tt.wantMetadata {
				assert.Contains(t, result, "metadata")
				assert.NotNil(t, result["metadata"])
			} else {
				assert.NotContains(t, result, "metadata")
			}
		})
	}
}

func TestApiError_Clone(t *testing.T) {
	t.Run("clones error without modifications", func(t *testing.T) {
		original := NewApiError("NOT_FOUND", http.StatusNotFound, "Resource not found")
		cloned := original.Clone()

		assert.Equal(t, original.Code, cloned.Code)
		assert.Equal(t, original.Message, cloned.Message)
		assert.Equal(t, original.httpStatus, cloned.httpStatus)
		assert.NotSame(t, original, cloned, "Clone should return a new instance")
	})

	t.Run("clones error with inner error", func(t *testing.T) {
		original := NewApiError("ERROR", http.StatusInternalServerError, "Error occurred")
		innerErr := errors.New("inner error")
		cloned := original.Clone(WithInnerError(innerErr))

		assert.NotNil(t, cloned.InnerError)
		assert.Empty(t, original.InnerError)
	})

	t.Run("clones error with metadata", func(t *testing.T) {
		original := NewApiError("ERROR", http.StatusBadRequest, "Validation error")
		metadata := map[string]string{"field": "email"}
		cloned := original.Clone(WithMetadata(metadata))

		require.NotNil(t, cloned.Metadata)
		assert.Equal(t, "email", cloned.Metadata["field"])
		assert.Nil(t, original.Metadata)
	})

	t.Run("clones error with multiple modifications", func(t *testing.T) {
		original := NewApiError("ERROR", http.StatusInternalServerError, "Error occurred")
		innerErr := errors.New("inner error")
		metadata := map[string]string{"key": "value"}

		cloned := original.Clone(
			WithInnerError(innerErr),
			WithMetadata(metadata),
		)

		assert.NotNil(t, cloned.InnerError)
		assert.NotNil(t, cloned.Metadata)
		assert.Empty(t, original.InnerError)
		assert.Nil(t, original.Metadata)
	})
}

func TestWithInnerError(t *testing.T) {
	innerErr := errors.New("test error")
	apiErr := &ApiError{}

	modifier := WithInnerError(innerErr)
	modifier(apiErr)

	require.NotNil(t, apiErr.InnerError)
	assert.Equal(t, "test error", apiErr.InnerError)
}

func TestWithMetadata(t *testing.T) {
	metadata := map[string]string{
		"key1": "value1",
		"key2": "value2",
	}
	apiErr := &ApiError{}

	modifier := WithMetadata(metadata)
	modifier(apiErr)

	require.NotNil(t, apiErr.Metadata)
	assert.Equal(t, "value1", apiErr.Metadata["key1"])
	assert.Equal(t, "value2", apiErr.Metadata["key2"])
}

func TestApiError_JSONSerialization(t *testing.T) {
	t.Run("serializes error without optional fields", func(t *testing.T) {
		err := NewApiError("TEST_ERROR", http.StatusBadRequest, "Test message")
		data, jsonErr := json.Marshal(err)
		require.NoError(t, jsonErr)

		var result map[string]any
		jsonErr = json.Unmarshal(data, &result)
		require.NoError(t, jsonErr)

		assert.Equal(t, "TEST_ERROR", result["code"])
		assert.Equal(t, "Test message", result["message"])
		assert.NotContains(t, result, "innerError")
		assert.NotContains(t, result, "metadata")

		// Verify the JSON structure matches expected format
		expectedJSON := `{"code":"TEST_ERROR","message":"Test message"}`
		assert.JSONEq(t, expectedJSON, string(data))
	})

	t.Run("serializes error with inner error", func(t *testing.T) {
		err := NewApiError(
			"INTERNAL_ERROR", http.StatusInternalServerError, "Something went wrong",
		).Clone(WithInnerError(errors.New("database connection timeout")))

		data, jsonErr := json.Marshal(err)
		require.NoError(t, jsonErr)

		var result map[string]any
		jsonErr = json.Unmarshal(data, &result)
		require.NoError(t, jsonErr)

		assert.Equal(t, "INTERNAL_ERROR", result["code"])
		assert.Equal(t, "Something went wrong", result["message"])
		assert.Contains(t, result, "innerError")
		assert.Equal(t, "database connection timeout", result["innerError"])
		assert.NotContains(t, result, "metadata")

		// Verify the JSON structure
		expectedJSON := `{
			"code":"INTERNAL_ERROR",
			"message":"Something went wrong",
			"innerError":"database connection timeout"
		}`
		assert.JSONEq(t, expectedJSON, string(data))
	})

	t.Run("serializes error with metadata", func(t *testing.T) {
		metadata := map[string]string{
			"field":  "email",
			"reason": "invalid format",
			"value":  "not-an-email",
		}
		err := NewApiError(
			"VALIDATION_ERROR", http.StatusBadRequest, "Validation failed",
		).Clone(WithMetadata(metadata))

		data, jsonErr := json.Marshal(err)
		require.NoError(t, jsonErr)

		var result map[string]any
		jsonErr = json.Unmarshal(data, &result)
		require.NoError(t, jsonErr)

		assert.Equal(t, "VALIDATION_ERROR", result["code"])
		assert.Equal(t, "Validation failed", result["message"])
		assert.NotContains(t, result, "innerError")
		assert.Contains(t, result, "metadata")

		metadataMap, ok := result["metadata"].(map[string]any)
		require.True(t, ok, "metadata should be a map")
		assert.Equal(t, "email", metadataMap["field"])
		assert.Equal(t, "invalid format", metadataMap["reason"])
		assert.Equal(t, "not-an-email", metadataMap["value"])
	})

	t.Run("serializes error with both inner error and metadata", func(t *testing.T) {
		metadata := map[string]string{
			"userId":     "12345",
			"resource":   "profile",
			"attemptNum": "3",
		}
		err := NewApiError("UPDATE_FAILED", http.StatusConflict, "Failed to update resource").
			Clone(
				WithInnerError(errors.New("unique constraint violation")),
				WithMetadata(metadata),
			)

		data, jsonErr := json.Marshal(err)
		require.NoError(t, jsonErr)

		var result map[string]any
		jsonErr = json.Unmarshal(data, &result)
		require.NoError(t, jsonErr)

		assert.Equal(t, "UPDATE_FAILED", result["code"])
		assert.Equal(t, "Failed to update resource", result["message"])
		assert.Contains(t, result, "innerError")
		assert.Equal(t, "unique constraint violation", result["innerError"])
		assert.Contains(t, result, "metadata")

		metadataMap, ok := result["metadata"].(map[string]any)
		require.True(t, ok, "metadata should be a map")
		assert.Equal(t, "12345", metadataMap["userId"])
		assert.Equal(t, "profile", metadataMap["resource"])
		assert.Equal(t, "3", metadataMap["attemptNum"])

		// Verify complete JSON structure
		expectedJSON := `{
			"code":"UPDATE_FAILED",
			"message":"Failed to update resource",
			"innerError":"unique constraint violation",
			"metadata":{
				"userId":"12345",
				"resource":"profile",
				"attemptNum":"3"
			}
		}`
		assert.JSONEq(t, expectedJSON, string(data))
	})

	t.Run("serializes error with empty metadata map", func(t *testing.T) {
		err := NewApiError(
			"ERROR", http.StatusBadRequest, "Error",
		).Clone(WithMetadata(map[string]string{}))

		data, jsonErr := json.Marshal(err)
		require.NoError(t, jsonErr)

		var result map[string]any
		jsonErr = json.Unmarshal(data, &result)
		require.NoError(t, jsonErr)

		// Empty maps are omitted due to omitempty tag
		assert.NotContains(t, result, "metadata")
	})
}
