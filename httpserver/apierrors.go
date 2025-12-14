package httpserver

import (
	"fmt"
	"net/http"
)

// ApiError represents a structured API error response that can be serialized to JSON.
type ApiError struct {
	Code       string            `json:"code"`
	Message    string            `json:"message"`
	InnerError error             `json:"innerError,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`

	httpStatus int
}

// NewApiError creates a new ApiError with the specified code, HTTP status, and message.
// The HTTP status code determines what status will be written to the response when WriteResponse is called.
func NewApiError(code string, httpStatus int, message string) *ApiError {
	return &ApiError{
		Code:    code,
		Message: message,

		httpStatus: httpStatus,
	}
}

// WriteResponse writes the ApiError as a JSON response to the HTTP response writer.
// It sets the Content-Type header to application/json, writes the appropriate HTTP
// status code, and serializes the error as JSON in the response body.
func (e ApiError) WriteResponse(w http.ResponseWriter, r *http.Request) {
	w.Header().Add(HeaderContentType, ContentTypeJson)
	w.WriteHeader(e.httpStatus)

	RespondWithJSON(w, r, e)
}

// Clone creates a deep copy of the ApiError and optionally applies modifications through the provided functions. This is useful for creating variations of an error without modifying the original.
// The with parameter accepts functions like WithInnerError and WithMetadata to customize the cloned error.
func (e ApiError) Clone(with ...func(*ApiError)) *ApiError {
	cloned := &ApiError{
		Code:    e.Code,
		Message: e.Message,

		httpStatus: e.httpStatus,
	}

	for _, w := range with {
		w(cloned)
	}

	return cloned
}

// WithInnerError returns a function that sets the InnerError field on an ApiError.
// This is typically used with the Clone method to add an underlying error cause to an API error response.
func WithInnerError(innerError error) func(*ApiError) {
	return func(e *ApiError) {
		e.InnerError = innerError
	}
}

// WithMetadata returns a function that sets the Metadata field on an ApiError.
// This is typically used with the Clone method to add additional context or debugging information to an API error response.
func WithMetadata(metadata map[string]string) func(*ApiError) {
	return func(e *ApiError) {
		e.Metadata = metadata
	}
}

// Error implements the error interface, returning a formatted string representation of the API error that includes both the error code and message.
func (e ApiError) Error() string {
	return fmt.Sprintf("API error (%s): %s", e.Code, e.Message)
}

// Is implements error comparison by checking if the target error is an ApiError with the same error code.
// This allows using errors.Is() to compare API errors based on their code rather than pointer equality.
func (e ApiError) Is(target error) bool {
	targetApiError, ok := target.(ApiError)
	if !ok {
		return false
	}

	return targetApiError.Code == e.Code
}
