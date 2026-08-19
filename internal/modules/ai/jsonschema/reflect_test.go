package jsonschema

import (
	"encoding/json"
	"testing"
)

type readFileArgs struct {
	Path   string `json:"path" jsonschema:"description=Absolute path to the file"`
	Offset *int   `json:"offset,omitempty" jsonschema:"description=Line to start at,minimum=0"`
	Limit  int    `json:"limit,omitempty"`
	Mode   string `json:"mode" jsonschema:"enum=read,enum=write,default=read"`
	secret string
}

func TestReflectStruct(t *testing.T) {
	s := Reflect[readFileArgs]()

	got, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}

	want := `{"type":"object","properties":{` +
		`"path":{"type":"string","description":"Absolute path to the file"},` +
		`"offset":{"type":"integer","description":"Line to start at","minimum":0},` +
		`"limit":{"type":"integer"},` +
		`"mode":{"type":"string","enum":["read","write"],"default":"read"}` +
		`},"required":["path","mode"],"additionalProperties":false}`

	if string(got) != want {
		t.Errorf("schema mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestPropertyOrderIsDeclarationOrder(t *testing.T) {
	s := Reflect[readFileArgs]()
	names := s.Properties.Names()
	want := []string{"path", "offset", "limit", "mode"}

	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("got %v, want %v", names, want)
		}
	}
}

type treeNode struct {
	Name     string      `json:"name"`
	Children []*treeNode `json:"children,omitempty"`
}

func TestReflectRecursiveTypeTerminates(t *testing.T) {
	s := Reflect[treeNode]()

	children, ok := s.Properties.Get("children")
	if !ok {
		t.Fatal("missing children property")
	}
	if children.Items.Ref != "#/$defs/treeNode" {
		t.Errorf("recursive element should be a $ref, got %q", children.Items.Ref)
	}
	if _, ok := s.Defs["treeNode"]; !ok {
		t.Errorf("expected treeNode in $defs, got %v", s.Defs)
	}
}

type embeddedBase struct {
	ID string `json:"id"`
}

type withEmbedded struct {
	embeddedBase
	Extra string `json:"extra,omitempty"`
}

func TestEmbeddedStructIsFlattened(t *testing.T) {
	s := Reflect[withEmbedded]()

	if _, ok := s.Properties.Get("id"); !ok {
		t.Errorf("embedded field should be inlined, got %v", s.Properties.Names())
	}
	if len(s.Required) != 1 || s.Required[0] != "id" {
		t.Errorf("required = %v, want [id]", s.Required)
	}
}

type quotedDesc struct {
	Query string `json:"query" jsonschema:"description='find files, then grep them'"`
}

func TestQuotedTagValueKeepsCommas(t *testing.T) {
	s := Reflect[quotedDesc]()
	q, _ := s.Properties.Get("query")
	if q.Description != "find files, then grep them" {
		t.Errorf("description = %q", q.Description)
	}
}

func TestScalarAndContainerKinds(t *testing.T) {
	type payload struct {
		Tags   []string          `json:"tags"`
		Meta   map[string]string `json:"meta"`
		Blob   []byte            `json:"blob"`
		Raw    json.RawMessage   `json:"raw"`
		Ratio  float64           `json:"ratio"`
		Active bool              `json:"active"`
	}
	s := Reflect[payload]()

	cases := map[string]Type{
		"tags": Array, "meta": Object, "blob": String,
		"raw": "", "ratio": Number, "active": Boolean,
	}
	for name, want := range cases {
		got, ok := s.Properties.Get(name)
		if !ok {
			t.Fatalf("missing %q", name)
		}
		if got.Type != want {
			t.Errorf("%s: type = %q, want %q", name, got.Type, want)
		}
	}
}
