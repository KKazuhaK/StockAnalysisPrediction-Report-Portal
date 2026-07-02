package batch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Manifest is a compiled declarative plugin: it describes how to call a backend
// (request template) and how to read its response (status mapping). A generic
// interpreter renders and executes it, so adding a backend is importing JSON —
// no recompile. See docs/adr/0001-batch-run-engine.md.
type Manifest struct {
	ID      string
	Name    string
	Version string
	inputs  []InputDecl
	config  []ConfigDecl
	request requestSpec
	resp    responseSpec
}

// InputDecl declares one input the plugin expects per row. The keys double as the
// CSV header and drive the executor's dynamic form.
type InputDecl struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
}

// ConfigDecl declares one per-target config field (e.g. base_url, api_key). Secret
// fields are rendered as password inputs and never sent back to the browser.
type ConfigDecl struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Secret bool   `json:"secret"`
}

type requestSpec struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

type responseSpec struct {
	RunID       string            `json:"run_id"`
	Status      string            `json:"status"`
	Map         map[string]string `json:"map"`
	PartialWhen *partialWhen      `json:"partial_when"`
	Detail      string            `json:"detail"`
}

type partialWhen struct {
	Path   string `json:"path"`
	Equals string `json:"equals"`
}

type spec struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Version  string       `json:"version"`
	Inputs   []InputDecl  `json:"inputs"`
	Config   []ConfigDecl `json:"config"`
	Request  requestSpec  `json:"request"`
	Response responseSpec `json:"response"`
}

// Compile parses and validates a plugin manifest.
func Compile(raw []byte) (*Manifest, error) {
	var s spec
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("manifest: invalid JSON: %w", err)
	}
	if strings.TrimSpace(s.Request.URL) == "" {
		return nil, fmt.Errorf("manifest: request.url is required")
	}
	if s.Request.Method == "" {
		s.Request.Method = http.MethodPost
	}
	if strings.TrimSpace(s.Response.Status) == "" {
		return nil, fmt.Errorf("manifest: response.status path is required")
	}
	return &Manifest{
		ID: s.ID, Name: s.Name, Version: s.Version,
		inputs: s.Inputs, config: s.Config, request: s.Request, resp: s.Response,
	}, nil
}

// Inputs returns the declared input fields (CSV columns / executor form fields).
func (m *Manifest) Inputs() []InputDecl { return m.inputs }

// Config returns the declared per-target config fields (base_url, api_key, …).
func (m *Manifest) Config() []ConfigDecl { return m.config }

// NewProvider binds a manifest to a target's config and an HTTP client.
func (m *Manifest) NewProvider(config map[string]string, client *http.Client) Provider {
	if client == nil {
		client = http.DefaultClient
	}
	return &manifestProvider{m: m, config: config, client: client}
}

type manifestProvider struct {
	m      *Manifest
	config map[string]string
	client *http.Client
}

func (p *manifestProvider) Run(ctx context.Context, inputs map[string]string) (RunResult, error) {
	for _, in := range p.m.inputs {
		if in.Required && strings.TrimSpace(inputs[in.Key]) == "" {
			return RunResult{}, permanentErr("manifest: missing required input %q", in.Key)
		}
	}
	url := p.renderText(p.m.request.URL, inputs)
	body, err := p.renderBody(p.m.request.Body, inputs)
	if err != nil {
		return RunResult{}, permanentErr("manifest: render body: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, p.m.request.Method, url, bytes.NewReader(body))
	if err != nil {
		return RunResult{}, permanentErr("manifest: build request: %v", err)
	}
	for k, v := range p.m.request.Headers {
		req.Header.Set(k, p.renderText(v, inputs))
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return RunResult{}, transientErr("manifest: request failed: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return RunResult{}, transientErr("manifest: backend status %d", resp.StatusCode)
	case resp.StatusCode >= 400:
		return RunResult{}, permanentErr("manifest: backend status %d", resp.StatusCode)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return RunResult{}, permanentErr("manifest: response not JSON: %v", err)
	}
	return RunResult{
		RunID:  extractString(doc, p.m.resp.RunID),
		Status: p.mapStatus(doc),
		Detail: extractString(doc, p.m.resp.Detail),
		Raw:    json.RawMessage(raw),
	}, nil
}

// renderText substitutes {{config.KEY}} and {{inputs.KEY}} in a template string.
func (p *manifestProvider) renderText(tmpl string, inputs map[string]string) string {
	out := tmpl
	for k, v := range p.config {
		out = strings.ReplaceAll(out, "{{config."+k+"}}", v)
	}
	for k, v := range inputs {
		out = strings.ReplaceAll(out, "{{inputs."+k+"}}", v)
	}
	return out
}

// renderBody walks the body template. A string value of exactly "{{inputs}}" is
// replaced by the whole inputs object; other strings get textual substitution.
func (p *manifestProvider) renderBody(raw json.RawMessage, inputs map[string]string) ([]byte, error) {
	if len(raw) == 0 {
		return []byte("{}"), nil
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, err
	}
	return json.Marshal(p.renderNode(tree, inputs))
}

func (p *manifestProvider) renderNode(node any, inputs map[string]string) any {
	switch v := node.(type) {
	case string:
		if v == "{{inputs}}" {
			return inputs
		}
		return p.renderText(v, inputs)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = p.renderNode(val, inputs)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, val := range v {
			out[i] = p.renderNode(val, inputs)
		}
		return out
	default:
		return v
	}
}

func (p *manifestProvider) mapStatus(doc map[string]any) Outcome {
	out := Failed
	if mapped, ok := p.m.resp.Map[extractString(doc, p.m.resp.Status)]; ok {
		out = parseOutcome(mapped)
	}
	if pw := p.m.resp.PartialWhen; pw != nil && pw.Path != "" {
		if extractString(doc, pw.Path) == pw.Equals {
			out = Partial
		}
	}
	return out
}

func parseOutcome(s string) Outcome {
	switch strings.ToLower(s) {
	case "ok":
		return Ok
	case "partial":
		return Partial
	default:
		return Failed
	}
}

// extractString reads a dotted path (e.g. "data.status") from a decoded JSON
// object and returns it as a string. Missing paths and non-scalar values yield "".
func extractString(doc map[string]any, path string) string {
	if path == "" {
		return ""
	}
	var cur any = doc
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[part]
	}
	switch v := cur.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}
