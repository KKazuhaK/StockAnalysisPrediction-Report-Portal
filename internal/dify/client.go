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
	"mime/multipart"
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
	Type     string   `json:"type"` // text-input | paragraph | number | select | file | file-list
	Required bool     `json:"required"`
	Options  []string `json:"options,omitempty"`
}

// The two Input types that carry files rather than a plain value: their run input is a
// file object (or an array of them) referencing an id from UploadFile, not a string.
const (
	InputFile     = "file"
	InputFileList = "file-list"
)

// MaxUploadBytes is Dify's own per-file limit. Callers reject a bigger file up front so the
// operator gets a clear answer instead of Dify's error after a 15MB round trip.
const MaxUploadBytes = 15 << 20

// MaxRunFiles is Dify's own per-run file cap. The count is enforced server-side as well as in
// the browser, so a run assembled outside the form (batch, API) cannot exceed it silently.
const MaxRunFiles = 10

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
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode/100 != 2 {
		return nil, &APIError{Status: resp.StatusCode, Message: apiErrMsg(raw)}
	}
	return raw, nil
}

// apiErrMsg pulls Dify's {message,code} error text out of a response body, falling
// back to the raw body when it isn't the expected JSON shape.
func apiErrMsg(raw []byte) string {
	var e struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Message != "" {
		return e.Message
	}
	return strings.TrimSpace(string(raw))
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

// UploadedFile is a file Dify accepted. ID is the handle a run's file input carries as
// upload_file_id; Name/Size are echoed back for the operator to confirm what was sent.
type UploadedFile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// UploadFile pushes one file to Dify (POST /files/upload) and returns its id, which a
// file / file-list input references as upload_file_id. user must be the SAME identity the
// run is recorded under: Dify scopes an uploaded file to its uploader, so a file uploaded
// as somebody else is not resolvable when the workflow runs.
//
// The body is buffered whole because multipart needs a Content-Length and callers cap the
// size at MaxUploadBytes anyway.
func (c *Client) UploadFile(ctx context.Context, filename string, src io.Reader, user string) (UploadedFile, error) {
	if user == "" {
		user = "report-portal"
	}
	if filename == "" {
		filename = "file"
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("user", user); err != nil {
		return UploadedFile{}, err
	}
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return UploadedFile{}, err
	}
	if _, err := io.Copy(part, src); err != nil {
		return UploadedFile{}, err
	}
	if err := mw.Close(); err != nil {
		return UploadedFile{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/files/upload", &body)
	if err != nil {
		return UploadedFile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return UploadedFile{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return UploadedFile{}, &APIError{Status: resp.StatusCode, Message: apiErrMsg(raw)}
	}
	var out UploadedFile
	if err := json.Unmarshal(raw, &out); err != nil {
		return UploadedFile{}, fmt.Errorf("dify /files/upload: bad JSON: %w", err)
	}
	// No id means nothing to reference at run time — a 2xx with an unusable body is still a
	// failed upload, and saying so here beats a workflow failing on a blank upload_file_id.
	if out.ID == "" {
		return UploadedFile{}, fmt.Errorf("dify /files/upload: response carried no file id")
	}
	return out, nil
}

// RunResult is the outcome of a workflow run (blocking or streaming).
type RunResult struct {
	WorkflowRunID  string
	ConversationID string // chat/agent apps only; the handle used to reconcile a dropped chat run
	TaskID         string // streaming only; needed to stop the run server-side
	Status         string // running | succeeded | partial-succeeded | failed | stopped
	Error          string
	Outputs        map[string]any
	Raw            json.RawMessage
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
