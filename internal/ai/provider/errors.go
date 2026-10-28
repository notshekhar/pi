package provider

import (
	"errors"
	"fmt"
)

// Sentinel errors for classifying failures with errors.Is. The concrete error
// types below wrap the matching sentinel, so a caller can ask
// errors.Is(err, provider.ErrAPICall) without knowing which provider produced it.
var (
	// ErrAPICall is any non-success response from a provider API.
	ErrAPICall = errors.New("api call failed")
	// ErrLoadAPIKey means no credential was found in settings or environment.
	ErrLoadAPIKey = errors.New("api key not found")
	// ErrNoSuchModel means the requested model id is unknown to the provider.
	ErrNoSuchModel = errors.New("no such model")
	// ErrUnsupportedFunctionality means the model cannot do what was asked.
	ErrUnsupportedFunctionality = errors.New("unsupported functionality")
	// ErrInvalidPrompt means the prompt violates the spec, e.g. a tool result
	// with no matching tool call.
	ErrInvalidPrompt = errors.New("invalid prompt")
	// ErrInvalidResponse means the provider returned something unparseable.
	ErrInvalidResponse = errors.New("invalid response from provider")
	// ErrJSONParse means a payload was not valid JSON.
	ErrJSONParse = errors.New("json parse error")
	// ErrTypeValidation means a payload parsed but did not match its schema.
	ErrTypeValidation = errors.New("type validation failed")
	// ErrTooManyEmbeddingValues means a batch exceeded the provider's limit.
	ErrTooManyEmbeddingValues = errors.New("too many embedding values")
)

// APICallError is a failed HTTP call to a provider.
type APICallError struct {
	Message string
	URL     string
	// RequestBody is the payload that was sent, for reproducing the failure.
	RequestBody string
	StatusCode  int
	// ResponseHeaders and ResponseBody are the raw provider response.
	ResponseHeaders Headers
	ResponseBody    string
	// IsRetryable reports whether retrying unchanged could succeed. Derived
	// from the status code: 408, 409, 429 and 5xx are retryable.
	IsRetryable bool
	// Data is any structured error payload the provider returned.
	Data JSONValue
	// Cause is the underlying transport error, if the call never completed.
	Cause error
}

// Error implements error.
func (e *APICallError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("%s (%s: HTTP %d)", e.Message, e.URL, e.StatusCode)
	}
	return fmt.Sprintf("%s (%s)", e.Message, e.URL)
}

// Unwrap reports the sentinel and any transport cause.
func (e *APICallError) Unwrap() []error {
	if e.Cause != nil {
		return []error{ErrAPICall, e.Cause}
	}
	return []error{ErrAPICall}
}

// IsRetryableStatus reports whether a status code is worth retrying: request
// timeout, conflict, rate limit, or any server-side error.
func IsRetryableStatus(status int) bool {
	switch status {
	case 408, 409, 429:
		return true
	}
	return status >= 500
}

// LoadAPIKeyError means no credential could be resolved.
type LoadAPIKeyError struct{ Message string }

// Error implements error.
func (e *LoadAPIKeyError) Error() string { return e.Message }

// Unwrap reports the sentinel.
func (e *LoadAPIKeyError) Unwrap() error { return ErrLoadAPIKey }

// NoSuchModelError means the model id is unknown.
type NoSuchModelError struct {
	ModelID string
	// ModelType is "languageModel", "embeddingModel", etc.
	ModelType string
	Message   string
}

// Error implements error.
func (e *NoSuchModelError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("no such %s: %s", e.ModelType, e.ModelID)
}

// Unwrap reports the sentinel.
func (e *NoSuchModelError) Unwrap() error { return ErrNoSuchModel }

// UnsupportedFunctionalityError means the model cannot perform the request.
type UnsupportedFunctionalityError struct {
	Functionality string
	Message       string
}

// Error implements error.
func (e *UnsupportedFunctionalityError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("unsupported functionality: %s", e.Functionality)
}

// Unwrap reports the sentinel.
func (e *UnsupportedFunctionalityError) Unwrap() error { return ErrUnsupportedFunctionality }

// InvalidPromptError means the prompt is malformed.
type InvalidPromptError struct {
	Prompt  JSONValue
	Message string
	Cause   error
}

// Error implements error.
func (e *InvalidPromptError) Error() string { return "invalid prompt: " + e.Message }

// Unwrap reports the sentinel and any cause.
func (e *InvalidPromptError) Unwrap() []error {
	if e.Cause != nil {
		return []error{ErrInvalidPrompt, e.Cause}
	}
	return []error{ErrInvalidPrompt}
}

// InvalidResponseError means the provider's response could not be interpreted.
type InvalidResponseError struct {
	Message string
	// Response is the payload that could not be interpreted.
	Response JSONValue
	Cause    error
}

// Error implements error.
func (e *InvalidResponseError) Error() string { return e.Message }

// Unwrap reports the sentinel and any cause.
func (e *InvalidResponseError) Unwrap() []error {
	if e.Cause != nil {
		return []error{ErrInvalidResponse, e.Cause}
	}
	return []error{ErrInvalidResponse}
}

// JSONParseError means text was not valid JSON.
type JSONParseError struct {
	Text  string
	Cause error
}

// Error implements error.
func (e *JSONParseError) Error() string {
	return fmt.Sprintf("failed to parse JSON: %v", e.Cause)
}

// Unwrap reports the sentinel and the decoder error.
func (e *JSONParseError) Unwrap() []error { return []error{ErrJSONParse, e.Cause} }

// TypeValidationError means a value parsed as JSON but did not match its type.
type TypeValidationError struct {
	Value JSONValue
	Cause error
}

// Error implements error.
func (e *TypeValidationError) Error() string {
	return fmt.Sprintf("type validation failed: %v", e.Cause)
}

// Unwrap reports the sentinel and the validation error.
func (e *TypeValidationError) Unwrap() []error { return []error{ErrTypeValidation, e.Cause} }
