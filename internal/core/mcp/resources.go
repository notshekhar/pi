package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Resources are the half of MCP that is read rather than called: files,
// records and documents a server publishes. Shapes follow loop's
// mcp/features.ts so both agents describe them the same way.

// Resource is one listed resource, qualified by the server that publishes it.
type Resource struct {
	Server      string `json:"server"`
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
	Size        *int64 `json:"size,omitempty"`
}

// ResourceTemplate describes resources a client builds a uri for rather than
// ones listed one by one.
type ResourceTemplate struct {
	Server      string `json:"server"`
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourcePart is one piece of a read resource, flattened to text a model
// can be shown.
type ResourcePart struct {
	URI      string
	MimeType string
	// Text is the contents, or a stand-in description of a binary blob.
	Text string
	// Binary is true when Text describes a blob instead of holding it.
	Binary bool
}

// maxResources bounds a runaway listing, like maxTools.
const maxResources = 1000

// ListResources fetches every resource the server publishes, following
// pagination.
func (c *Client) ListResources(ctx context.Context, server string) ([]Resource, error) {
	var all []Resource
	cursor := ""
	for {
		var params map[string]any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		raw, err := c.request(ctx, "resources/list", params, 0)
		if err != nil {
			return nil, err
		}
		var r struct {
			Resources  []Resource `json:"resources"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("malformed resources/list result: %w", err)
		}
		for _, res := range r.Resources {
			res.Server = server
			all = append(all, res)
		}
		if r.NextCursor == "" || r.NextCursor == cursor || len(all) >= maxResources {
			return all, nil
		}
		cursor = r.NextCursor
	}
}

// ListResourceTemplates fetches the server's uri templates. Templates are
// optional even for a server with resources, so method-not-found is none.
func (c *Client) ListResourceTemplates(ctx context.Context, server string) ([]ResourceTemplate, error) {
	raw, err := c.request(ctx, "resources/templates/list", nil, 0)
	if err != nil {
		var re *RPCError
		if errors.As(err, &re) && re.Code == codeMethodNotFound {
			return nil, nil
		}
		return nil, err
	}
	var r struct {
		ResourceTemplates []ResourceTemplate `json:"resourceTemplates"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("malformed resources/templates/list result: %w", err)
	}
	for i := range r.ResourceTemplates {
		r.ResourceTemplates[i].Server = server
	}
	return r.ResourceTemplates, nil
}

// ReadResource fetches one resource's contents. Never cached: contents
// change, and a stale read is worse than a round trip.
func (c *Client) ReadResource(ctx context.Context, uri string) ([]ResourcePart, error) {
	raw, err := c.request(ctx, "resources/read", map[string]any{"uri": uri}, 0)
	if err != nil {
		return nil, err
	}
	var r struct {
		Contents []resourceContents `json:"contents"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("malformed resources/read result: %w", err)
	}
	return flattenContents(r.Contents), nil
}

type resourceContents struct {
	URI      string  `json:"uri"`
	MimeType string  `json:"mimeType"`
	Text     *string `json:"text"`
	Blob     *string `json:"blob"`
}

// flattenContents describes a binary blob instead of inlining it: base64 of
// a PDF or an image is unreadable and enormous, and the uri, type and size
// are enough for the model to decide what to do next.
func flattenContents(contents []resourceContents) []ResourcePart {
	var parts []ResourcePart
	for _, c := range contents {
		switch {
		case c.Text != nil:
			parts = append(parts, ResourcePart{URI: c.URI, MimeType: c.MimeType, Text: *c.Text})
		case c.Blob != nil:
			kind := c.MimeType
			if kind == "" {
				kind = "content"
			}
			bytes := len(*c.Blob) * 3 / 4
			parts = append(parts, ResourcePart{URI: c.URI, MimeType: c.MimeType, Binary: true,
				Text: fmt.Sprintf("[binary %s, %d bytes — not inlined]", kind, bytes)})
		}
	}
	return parts
}
