package catalog

import (
	"regexp"
	"strings"
)

// metadata.ts: a gateway model's facts merged from layers, first opinion wins
// per field (cost per sub-field), over a floor.

// Metadata is what one source knows; nil/zero pointers mean "no opinion".
type Metadata struct {
	Name          *string
	ContextWindow *int
	MaxOutput     *int
	Cost          PartialCost
	Reasoning     *bool
	Modalities    []string
}

// PartialCost is a cost with per-field opinions.
type PartialCost struct {
	Input, Output, CacheRead, CacheWrite, Reasoning *float64
}

// Floor is the values used where no layer had an opinion.
type Floor struct {
	Name          string
	ContextWindow int
	MaxOutput     int
	Cost          Cost
	Reasoning     bool
	Modalities    []string
}

var gatewayFloor = Floor{
	ContextWindow: 200_000, MaxOutput: 16_000, Modalities: []string{"text"},
}

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func intp(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func merge(layers []*Metadata) Metadata {
	var m Metadata
	for _, l := range layers {
		if l == nil {
			continue
		}
		if m.Name == nil {
			m.Name = l.Name
		}
		if m.ContextWindow == nil {
			m.ContextWindow = l.ContextWindow
		}
		if m.MaxOutput == nil {
			m.MaxOutput = l.MaxOutput
		}
		if m.Reasoning == nil {
			m.Reasoning = l.Reasoning
		}
		if m.Modalities == nil {
			m.Modalities = l.Modalities
		}
		if m.Cost.Input == nil {
			m.Cost.Input = l.Cost.Input
		}
		if m.Cost.Output == nil {
			m.Cost.Output = l.Cost.Output
		}
		if m.Cost.CacheRead == nil {
			m.Cost.CacheRead = l.Cost.CacheRead
		}
		if m.Cost.CacheWrite == nil {
			m.Cost.CacheWrite = l.Cost.CacheWrite
		}
		if m.Cost.Reasoning == nil {
			m.Cost.Reasoning = l.Cost.Reasoning
		}
	}
	return m
}

func floorMetadata(f Floor, id string) *Metadata {
	name := f.Name
	if name == "" {
		name = id
	}
	in, out, cr, cw := f.Cost.Input, f.Cost.Output, f.Cost.CacheRead, f.Cost.CacheWrite
	ctx, mo, r := f.ContextWindow, f.MaxOutput, f.Reasoning
	return &Metadata{Name: &name, ContextWindow: &ctx, MaxOutput: &mo, Reasoning: &r, Modalities: f.Modalities,
		Cost: PartialCost{Input: &in, Output: &out, CacheRead: &cr, CacheWrite: &cw}}
}

// buildModelInfo merges layers over the floor into a complete model.
func buildModelInfo(id, provider string, layers []*Metadata, floor Floor) Model {
	m := merge(append(layers, floorMetadata(floor, id)))
	return Model{
		ID: id, Provider: provider, Name: *m.Name, Context: *m.ContextWindow, MaxOut: *m.MaxOutput,
		Cost: Cost{Input: *m.Cost.Input, Output: *m.Cost.Output, CacheRead: *m.Cost.CacheRead,
			CacheWrite: *m.Cost.CacheWrite, Reasoning: m.Cost.Reasoning},
		Reasoning: *m.Reasoning, Modalities: m.Modalities, Available: true,
	}.withShort()
}

// fromModel borrows a catalog model's facts.
func fromModel(info *Model) *Metadata {
	if info == nil {
		return nil
	}
	in, out, cr, cw := info.Cost.Input, info.Cost.Output, info.Cost.CacheRead, info.Cost.CacheWrite
	ctx, mo, r := info.Context, info.MaxOut, info.Reasoning
	name := info.Name
	return &Metadata{Name: &name, ContextWindow: &ctx, MaxOutput: &mo, Reasoning: &r, Modalities: info.Modalities,
		Cost: PartialCost{Input: &in, Output: &out, CacheRead: &cr, CacheWrite: &cw, Reasoning: info.Cost.Reasoning}}
}

// fromDiscovery is what a gateway reported about its own deployment.
func fromDiscovery(meta *CustomMeta) *Metadata {
	if meta == nil {
		return nil
	}
	return &Metadata{Name: strp(meta.Name), ContextWindow: intp(meta.ContextWindow), MaxOutput: intp(meta.MaxOutput)}
}

// fromDeclaration is a model entry as the user wrote it in auth.json.
func fromDeclaration(d CustomModelDecl) *Metadata {
	md := &Metadata{Name: strp(d.Name), ContextWindow: intp(d.ContextWindow), MaxOutput: intp(d.MaxOutput)}
	if d.Cost != nil {
		md.Cost = *d.Cost
	}
	return md
}

var vendorAliases = map[string]string{
	"bedrock": "amazon-bedrock", "aws-bedrock": "amazon-bedrock",
	"vertex": "google-vertex", "vertex_ai": "google-vertex", "vertex-ai": "google-vertex",
	"gemini": "google", "azure_ai": "azure", "azure-ai": "azure", "azure-openai": "azure",
}

type gatewayID struct{ raw, label, tail, vendor string }

func parseGatewayModelID(id string) gatewayID {
	slash := strings.IndexByte(id, '/')
	if slash <= 0 {
		return gatewayID{raw: id, tail: id}
	}
	label := strings.ToLower(id[:slash])
	vendor := label
	if v, ok := vendorAliases[label]; ok {
		vendor = v
	}
	return gatewayID{raw: id, label: label, tail: id[slash+1:], vendor: vendor}
}

var dateSuffix = regexp.MustCompile(`-20\d{6}$`)

type index func(key string) *Model

func chain(indexes ...index) index {
	return func(key string) *Model {
		for _, ix := range indexes {
			if m := ix(key); m != nil {
				return m
			}
		}
		return nil
	}
}

func mapIndex(m map[string]Model) index {
	return func(key string) *Model {
		if v, ok := m[key]; ok {
			return &v
		}
		return nil
	}
}

// vendorResolver is loop's VendorCatalogResolver.
type vendorResolver struct{ qualified, short index }

func (r vendorResolver) resolve(modelID string) *Metadata { return fromModel(r.find(modelID)) }

func (r vendorResolver) find(modelID string) *Model {
	p := parseGatewayModelID(modelID)
	type cand struct {
		qualified bool
		key       string
	}
	var strategies [][]cand
	if p.vendor != "" {
		strategies = append(strategies, []cand{{true, p.vendor + "/" + p.tail}})
	}
	strategies = append(strategies, []cand{{false, p.raw}})
	if p.label != "" {
		strategies = append(strategies, []cand{{false, p.tail}})
	}
	for _, s := range strategies {
		for _, c := range s {
			ix := r.short
			if c.qualified {
				ix = r.qualified
			}
			for _, key := range []string{c.key, dateSuffix.ReplaceAllString(c.key, "")} {
				if m := ix(key); m != nil {
					return m
				}
			}
		}
	}
	return nil
}
