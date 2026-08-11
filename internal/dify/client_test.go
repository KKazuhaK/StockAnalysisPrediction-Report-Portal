package dify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stub mimics the real Dify workflow-app service API shapes captured from a live
// instance (workflow "1-6-4投资决策模块", one input "symbol").
func stub(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer app-key" {
			w.WriteHeader(401)
			w.Write([]byte(`{"code":"unauthorized","message":"Access token is invalid"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/info":
			w.Write([]byte(`{"name":"[运行入口/工具] 1-6-4投资决策模块-CoD-V2","description":"…","mode":"workflow","author_name":"wfeixu"}`))
		case "/parameters":
			w.Write([]byte(`{"user_input_form":[{"text-input":{"label":"上市公司代码","max_length":48,"options":[],"required":true,"type":"text-input","variable":"symbol"}},{"select":{"label":"类型","required":false,"type":"select","variable":"kind","options":["a","b"]}}]}`))
		case "/workflows/run":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["response_mode"] != "blocking" {
				t.Errorf("response_mode = %v, want blocking", body["response_mode"])
			}
			w.Write([]byte(`{"workflow_run_id":"run-1","task_id":"t1","data":{"status":"succeeded","outputs":{"uid":"600160|2026-07-02"},"error":null}}`))
		default:
			w.WriteHeader(404)
		}
	}))
}

func TestInfoAndParameters(t *testing.T) {
	s := stub(t)
	defer s.Close()
	c := New(s.URL+"/", "app-key", s.Client()) // trailing slash trimmed

	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if !strings.Contains(info.Name, "1-6-4投资决策模块") || info.Mode != "workflow" {
		t.Fatalf("info = %+v", info)
	}

	inputs, err := c.Parameters(context.Background())
	if err != nil {
		t.Fatalf("Parameters: %v", err)
	}
	if len(inputs) != 2 {
		t.Fatalf("inputs = %+v, want 2", inputs)
	}
	if inputs[0].Variable != "symbol" || inputs[0].Label != "上市公司代码" || inputs[0].Type != "text-input" || !inputs[0].Required {
		t.Fatalf("inputs[0] = %+v", inputs[0])
	}
	if inputs[1].Variable != "kind" || inputs[1].Type != "select" || len(inputs[1].Options) != 2 {
		t.Fatalf("inputs[1] (select) = %+v", inputs[1])
	}
}

func TestRunWorkflow(t *testing.T) {
	s := stub(t)
	defer s.Close()
	c := New(s.URL, "app-key", s.Client())

	res, err := c.RunWorkflow(context.Background(), map[string]any{"symbol": "600160"}, "op")
	if err != nil {
		t.Fatalf("RunWorkflow: %v", err)
	}
	if res.WorkflowRunID != "run-1" || res.Status != "succeeded" {
		t.Fatalf("run result = %+v", res)
	}
	if res.Outputs["uid"] != "600160|2026-07-02" {
		t.Fatalf("outputs = %v", res.Outputs)
	}
}

func TestAuthErrorIsReadable(t *testing.T) {
	s := stub(t)
	defer s.Close()
	c := New(s.URL, "wrong-key", s.Client())
	_, err := c.Info(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Access token is invalid") {
		t.Fatalf("expected a readable auth error, got %v", err)
	}
}

// UploadFile posts the file as multipart field "file" alongside the end-user form field,
// authorized by the app key, and returns the id a run's file input references.
func TestUploadFile(t *testing.T) {
	var gotAuth, gotUser, gotName string
	var gotBody []byte
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files/upload" {
			t.Errorf("path = %q, want /files/upload", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		gotUser = r.FormValue("user")
		f, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("FormFile: %v", err)
		}
		defer f.Close()
		gotName = header.Filename
		gotBody, _ = io.ReadAll(f)
		w.Write([]byte(`{"id":"file-1","name":"研报.pdf","size":7}`))
	}))
	defer s.Close()

	up, err := New(s.URL, "app-key", s.Client()).UploadFile(context.Background(), "研报.pdf", strings.NewReader("PDFDATA"), "kazuha@example.org")
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if up.ID != "file-1" || up.Name != "研报.pdf" || up.Size != 7 {
		t.Fatalf("uploaded = %+v", up)
	}
	if gotAuth != "Bearer app-key" {
		t.Errorf("auth = %q, want Bearer app-key", gotAuth)
	}
	if gotUser != "kazuha@example.org" {
		t.Errorf("user = %q, want the run's end-user identity", gotUser)
	}
	if gotName != "研报.pdf" || string(gotBody) != "PDFDATA" {
		t.Errorf("sent file %q = %q", gotName, gotBody)
	}
}

// A rejected upload surfaces Dify's own message and status, and a 2xx with no id is an
// error too — a blank upload_file_id would only fail later, inside the workflow.
func TestUploadFileErrors(t *testing.T) {
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		w.Write([]byte(`{"code":"file_too_large","message":"File size exceeded"}`))
	}))
	defer rejecting.Close()
	idless := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer idless.Close()

	_, err := New(rejecting.URL, "k", rejecting.Client()).UploadFile(context.Background(), "big.pdf", strings.NewReader("x"), "u")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusRequestEntityTooLarge || !strings.Contains(ae.Message, "File size exceeded") {
		t.Fatalf("err = %v, want a 413 APIError carrying Dify's message", err)
	}
	if _, err := New(idless.URL, "k", idless.Client()).UploadFile(context.Background(), "f.pdf", strings.NewReader("x"), "u"); err == nil {
		t.Fatal("a 2xx response with no file id must be an error")
	}
}
