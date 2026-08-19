// Package provider defines the language model specification that all pi
// providers implement. It is a Go port of @ai-sdk/provider's
// LanguageModelV4 interface (ai@7).
//
// Providers depend on this package and core (package pi) depends on it; it
// depends only on the standard library and pigo/jsonschema.
package provider

import "net/url"

// SpecificationVersion is the spec revision implemented by this package.
const SpecificationVersion = "v4"

// JSONValue is any JSON-serializable value: nil, bool, float64, string,
// []any, or map[string]any.
type JSONValue = any

// JSONObject is a JSON object.
type JSONObject = map[string]any

// ProviderMetadata carries provider-specific data out of a provider, keyed by
// provider id (e.g. "anthropic") then by field.
type ProviderMetadata map[string]JSONObject

// ProviderOptions carries provider-specific settings into a provider, keyed by
// provider id then by field. A provider must ignore keys it does not own.
type ProviderOptions map[string]JSONObject

// Get returns the options block for a provider id.
func (o ProviderOptions) Get(providerID string) JSONObject {
	if o == nil {
		return nil
	}
	return o[providerID]
}

// Headers are HTTP headers as a flat map.
type Headers map[string]string

// ProviderReference refers to a file already uploaded to a provider,
// keyed by provider id.
type ProviderReference map[string]string

// WarningType classifies a Warning.
type WarningType string

const (
	// WarningUnsupported marks a requested feature the model cannot support.
	WarningUnsupported WarningType = "unsupported"
	// WarningCompatibility marks a feature emulated with reduced fidelity.
	WarningCompatibility WarningType = "compatibility"
	// WarningDeprecated marks a setting that will be removed.
	WarningDeprecated WarningType = "deprecated"
	// WarningOther is a free-form warning.
	WarningOther WarningType = "other"
)

// Warning reports a non-fatal problem with a call, such as a setting the
// model ignored. Which fields are populated depends on Type.
type Warning struct {
	Type WarningType `json:"type"`

	// Feature is set for unsupported and compatibility warnings.
	Feature string `json:"feature,omitempty"`
	// Details is optional extra context for unsupported and compatibility.
	Details string `json:"details,omitempty"`
	// Setting is set for deprecated warnings.
	Setting string `json:"setting,omitempty"`
	// Message is set for deprecated and other warnings.
	Message string `json:"message,omitempty"`
}

// Unsupported builds a warning for a feature the model does not support.
func Unsupported(feature, details string) Warning {
	return Warning{Type: WarningUnsupported, Feature: feature, Details: details}
}

// FileData is file content in one of several forms: FileDataBytes,
// FileDataURL, FileDataRef, or FileDataText.
type FileData interface{ isFileData() }

// FileDataBytes is inline file content. Exactly one of Data or Base64 is set;
// providers should prefer whichever their API accepts to avoid a re-encode.
type FileDataBytes struct {
	Data   []byte
	Base64 string
}

// FileDataURL points at a file by URL. Providers that declare support for the
// URL via SupportedURLs receive it unfetched; otherwise core downloads it
// first and substitutes FileDataBytes.
type FileDataURL struct{ URL *url.URL }

// FileDataRef refers to a file already stored with the provider.
type FileDataRef struct{ Reference ProviderReference }

// FileDataText is inline text content, e.g. a plain-text document.
type FileDataText struct{ Text string }

func (FileDataBytes) isFileData() {}
func (FileDataURL) isFileData()   {}
func (FileDataRef) isFileData()   {}
func (FileDataText) isFileData()  {}
