// Package dify is a small typed client for the Dify workflow-app service API
// (docs/adr/0006-dify-native.md). The portal is Dify-specific, so instead of a
// generic manifest we talk to Dify directly: discover a workflow's name and input
// fields from an API key, and run it. Three endpoints, all authorized by the app's
// service key (Bearer app-…):
//
//	GET  /info        → app name / mode
//	GET  /parameters  → user_input_form (the input fields we map to CSV columns)
//	POST /workflows/run (blocking) → run one row
package dify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client targets one Dify workflow app: a base URL (…/v1) and that app's service key.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// New builds a client, defaulting the HTTP client and trimming a trailing slash.
func New(baseURL, apiKey string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, HTTP: hc}
}

// Info is a workflow app's basic metadata.
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Mode        string `json:"mode"` // "workflow" for workflow apps
}

// Input is one declared input variable of a workflow (from user_input_form). Variable
// is the key sent in `inputs` and doubles as the batch CSV column.
type Input struct {
	Variable string   `json:"variable"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // text-input | paragraph | number | select
	Required bool     `json:"required"`
	Options  []string `json:"options,omitempty"`
}

// APIError is a non-2xx response from Dify (carries the status so callers can tell
// a retryable 5xx/429 from a permanent 4xx). A transport failure (no response)
// surfaces as the raw error instead, which callers also treat as retryable.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("dify: %d %s", e.Status, e.Message) }

// do issues an authorized request and returns the decoded body, mapping non-2xx to
// an *APIError (Dify returns {message, code}).
func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(raw))
		var e struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Message != "" {
			msg = e.Message
		}
		return nil, &APIError{Status: resp.StatusCode, Message: msg}
	}
	return raw, nil
}

// Info fetches the workflow app's metadata (used to confirm the key + show a name).
func (c *Client) Info(ctx context.Context) (Info, error) {
	raw, err := c.do(ctx, http.MethodGet, "/info", nil)
	if err != nil {
		return Info{}, err
	}
	var out Info
	if err := json.Unmarshal(raw, &out); err != nil {
		return Info{}, fmt.Errorf("dify /info: bad JSON: %w", err)
	}
	return out, nil
}

// Parameters fetches and flattens the workflow's user_input_form into Inputs. Each
// form element is a single-key object keyed by field type, e.g.
// {"text-input": {"variable":"symbol","label":"…","required":true, ...}}.
func (c *Client) Parameters(ctx context.Context) ([]Input, error) {
	raw, err := c.do(ctx, http.MethodGet, "/parameters", nil)
	if err != nil {
		return nil, err
	}
	var doc struct {
		UserInputForm []map[string]struct {
			Variable string   `json:"variable"`
			Label    string   `json:"label"`
			Type     string   `json:"type"`
			Required bool     `json:"required"`
			Options  []string `json:"options"`
		} `json:"user_input_form"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("dify /parameters: bad JSON: %w", err)
	}
	out := make([]Input, 0, len(doc.UserInputForm))
	for _, field := range doc.UserInputForm {
		for kind, f := range field { // exactly one entry; the key is the field type
			t := f.Type
			if t == "" {
				t = kind
			}
			if f.Variable == "" {
				continue // skip malformed entries
			}
			out = append(out, Input{Variable: f.Variable, Label: f.Label, Type: t, Required: f.Required, Options: f.Options})
		}
	}
	return out, nil
}

// RunResult is the outcome of a blocking workflow run.
type RunResult struct {
	WorkflowRunID string
	Status        string // running | succeeded | failed | stopped
	Error         string
	Outputs       map[string]any
	Raw           json.RawMessage
}

// RunWorkflow runs the workflow once (blocking) with the given inputs. user is the
// caller identity Dify records for the run.
func (c *Client) RunWorkflow(ctx context.Context, inputs map[string]any, user string) (RunResult, error) {
	if inputs == nil {
		inputs = map[string]any{}
	}
	if user == "" {
		user = "report-portal"
	}
	raw, err := c.do(ctx, http.MethodPost, "/workflows/run", map[string]any{
		"inputs": inputs, "response_mode": "blocking", "user": user,
	})
	if err != nil {
		return RunResult{}, err
	}
	var doc struct {
		WorkflowRunID string `json:"workflow_run_id"`
		Data          struct {
			Status  string         `json:"status"`
			Error   string         `json:"error"`
			Outputs map[string]any `json:"outputs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return RunResult{}, fmt.Errorf("dify /workflows/run: bad JSON: %w", err)
	}
	return RunResult{
		WorkflowRunID: doc.WorkflowRunID, Status: doc.Data.Status,
		Error: doc.Data.Error, Outputs: doc.Data.Outputs, Raw: raw,
	}, nil
}
