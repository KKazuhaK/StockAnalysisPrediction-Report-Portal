package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/dify"
)

// seedDifyTarget creates a Dify target with a known secret and returns its id.
func seedDifyTarget(t *testing.T, s *Server, name string) int64 {
	t.Helper()
	if err := s.st.UpsertPlugin(difyPluginSlug, "Dify Workflow", "1.0.0", "{}", "bundled"); err != nil {
		t.Fatalf("UpsertPlugin: %v", err)
	}
	cfg, _ := json.Marshal(difyTargetConfig{
		BaseURL: "https://dify.example/v1", APIKey: "app-secret",
		Inputs: []dify.Input{{Variable: "symbol", Required: true}},
	})
	id, err := s.st.CreateTarget(difyPluginSlug, name, string(cfg))
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	return id
}

func getTarget(t *testing.T, h func(http.ResponseWriter, *http.Request, string), id int64) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("id", fmt.Sprint(id))
	h(rec, req, "admin")
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func putTarget(t *testing.T, h func(http.ResponseWriter, *http.Request, string), id int64, body string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/x", strings.NewReader(body))
	req.SetPathValue("id", fmt.Sprint(id))
	h(rec, req, "admin")
	return rec.Code
}

// Editing a Dify target: GET returns its editable config (never the api_key), and
// PUT updates name + inputs while keeping the stored key when the client sends none.
func TestDifyTargetEditRoundTrip(t *testing.T) {
	s := batchServer(t)
	id := seedDifyTarget(t, s, "Old name")

	// GET surfaces name, base_url, inputs, has_key — but not the secret.
	code, got := getTarget(t, s.apiBatchDifyTargetGet, id)
	if code != http.StatusOK {
		t.Fatalf("GET → %d: %v", code, got)
	}
	if got["name"] != "Old name" || got["base_url"] != "https://dify.example/v1" || got["has_key"] != true {
		t.Fatalf("GET body = %v", got)
	}
	if _, leaked := got["api_key"]; leaked {
		t.Fatalf("GET must not surface api_key: %v", got)
	}
	if b, _ := json.Marshal(got); strings.Contains(string(b), "app-secret") {
		t.Fatalf("GET leaked the api_key: %s", b)
	}

	// PUT with a blank api_key updates name + inputs and preserves the stored key.
	body := `{"name":"New name","base_url":"https://dify.example/v1","api_key":"",` +
		`"inputs":[{"variable":"symbol","required":true},{"variable":"rumor"}]}`
	if code := putTarget(t, s.apiBatchDifyTargetUpdate, id, body); code != http.StatusOK {
		t.Fatalf("PUT → %d", code)
	}
	tgt, _ := s.st.GetTarget(id)
	if tgt.Name != "New name" {
		t.Errorf("name = %q, want New name", tgt.Name)
	}
	var after difyTargetConfig
	json.Unmarshal([]byte(tgt.Config), &after)
	if after.APIKey != "app-secret" {
		t.Errorf("blank api_key should preserve stored key, got %q", after.APIKey)
	}
	if len(after.Inputs) != 2 || after.Inputs[1].Variable != "rumor" {
		t.Errorf("inputs = %+v", after.Inputs)
	}

	// A fresh api_key rotates the stored one.
	if code := putTarget(t, s.apiBatchDifyTargetUpdate, id,
		`{"name":"New name","base_url":"https://dify.example/v1","api_key":"app-rotated","inputs":[{"variable":"symbol"}]}`); code != http.StatusOK {
		t.Fatalf("PUT2 → %d", code)
	}
	tgt, _ = s.st.GetTarget(id)
	json.Unmarshal([]byte(tgt.Config), &after)
	if after.APIKey != "app-rotated" {
		t.Errorf("api_key = %q, want app-rotated", after.APIKey)
	}
}

// The Dify edit endpoints only serve Dify targets, and never a missing one.
func TestDifyTargetEditRejectsNonDifyAndMissing(t *testing.T) {
	s := batchServer(t)
	if err := s.st.UpsertPlugin("custom", "Custom", "1.0.0", "{}", "bundled"); err != nil {
		t.Fatalf("UpsertPlugin: %v", err)
	}
	custom, _ := s.st.CreateTarget("custom", "C", "{}")

	if code, _ := getTarget(t, s.apiBatchDifyTargetGet, custom); code != http.StatusNotFound {
		t.Errorf("GET non-dify → %d, want 404", code)
	}
	if code, _ := getTarget(t, s.apiBatchDifyTargetGet, 99999); code != http.StatusNotFound {
		t.Errorf("GET missing → %d, want 404", code)
	}
	if code := putTarget(t, s.apiBatchDifyTargetUpdate, custom,
		`{"name":"x","base_url":"y","api_key":"z","inputs":[]}`); code != http.StatusNotFound {
		t.Errorf("PUT non-dify → %d, want 404", code)
	}
}

// PUT rejects an empty name or base_url, and refuses to save a target with no key
// when none is stored yet.
func TestDifyTargetUpdateValidation(t *testing.T) {
	s := batchServer(t)
	id := seedDifyTarget(t, s, "T")

	if code := putTarget(t, s.apiBatchDifyTargetUpdate, id,
		`{"name":"  ","base_url":"https://dify.example/v1","api_key":"k","inputs":[{"variable":"symbol"}]}`); code != http.StatusBadRequest {
		t.Errorf("blank name → %d, want 400", code)
	}
	if code := putTarget(t, s.apiBatchDifyTargetUpdate, id,
		`{"name":"T","base_url":"","api_key":"k","inputs":[{"variable":"symbol"}]}`); code != http.StatusBadRequest {
		t.Errorf("blank base_url → %d, want 400", code)
	}
}

// Re-probing an existing target with a blank key reuses the target's stored key +
// base URL, so the admin never has to re-paste the secret to refresh inputs.
func TestDifyProbeReusesStoredKey(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/info":
			fmt.Fprint(w, `{"name":"WF","mode":"workflow"}`)
		case "/parameters":
			fmt.Fprint(w, `{"user_input_form":[{"text-input":{"variable":"symbol","required":true}}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := batchServer(t)
	if err := s.st.UpsertPlugin(difyPluginSlug, "Dify", "1.0.0", "{}", "bundled"); err != nil {
		t.Fatalf("UpsertPlugin: %v", err)
	}
	cfg, _ := json.Marshal(difyTargetConfig{BaseURL: srv.URL, APIKey: "app-stored"})
	id, err := s.st.CreateTarget(difyPluginSlug, "T", string(cfg))
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}

	rec := httptest.NewRecorder()
	body := fmt.Sprintf(`{"base_url":"","api_key":"","target_id":%d}`, id)
	s.apiBatchDifyProbe(rec, httptest.NewRequest("POST", "/x", strings.NewReader(body)), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("probe → %d: %s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer app-stored" {
		t.Errorf("probe used auth %q, want Bearer app-stored (the stored key)", gotAuth)
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["name"] != "WF" {
		t.Errorf("probe name = %v, want WF", out["name"])
	}
}

// uploadToTarget posts one multipart file to the Dify upload proxy for a target.
func uploadToTarget(t *testing.T, s *Server, id int64, user, filename string, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/admin/batch/targets/x/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.SetPathValue("id", fmt.Sprint(id))
	rec := httptest.NewRecorder()
	s.apiBatchDifyFileUpload(rec, req, user)
	return rec
}

// The upload proxy forwards the file to the target's Dify with the STORED api key (which
// never reaches the browser) and hands back the file id a row will carry.
func TestDifyFileUploadProxy(t *testing.T) {
	var gotAuth, gotUser string
	var gotBody []byte
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files/upload" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		r.ParseMultipartForm(1 << 20)
		gotUser = r.FormValue("user")
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("FormFile: %v", err)
		}
		defer f.Close()
		gotBody, _ = io.ReadAll(f)
		fmt.Fprint(w, `{"id":"file-9","name":"a.pdf","size":5}`)
	}))
	defer dst.Close()

	s := batchServer(t)
	if err := s.st.UpsertPlugin(difyPluginSlug, "Dify", "1.0.0", "{}", "bundled"); err != nil {
		t.Fatalf("UpsertPlugin: %v", err)
	}
	cfg, _ := json.Marshal(difyTargetConfig{BaseURL: dst.URL, APIKey: "app-secret"})
	id, err := s.st.CreateTarget(difyPluginSlug, "T", string(cfg))
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}

	rec := uploadToTarget(t, s, id, "admin", "a.pdf", []byte("HELLO"))
	if rec.Code != http.StatusOK {
		t.Fatalf("upload → %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["ok"] != true || out["file_id"] != "file-9" || out["name"] != "a.pdf" || out["size"] != float64(5) {
		t.Fatalf("body = %v", out)
	}
	if gotAuth != "Bearer app-secret" {
		t.Errorf("forwarded auth = %q, want the stored key", gotAuth)
	}
	if gotUser != s.difyEndUser("admin") {
		t.Errorf("upload user = %q, want the run's end-user %q", gotUser, s.difyEndUser("admin"))
	}
	if string(gotBody) != "HELLO" {
		t.Errorf("forwarded bytes = %q", gotBody)
	}
}

// A file over Dify's own 15MB limit is refused here, before it is pushed upstream; and the
// proxy only serves Dify targets.
func TestDifyFileUploadLimitsAndScope(t *testing.T) {
	var forwarded int
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded++
		fmt.Fprint(w, `{"id":"file-1"}`)
	}))
	defer dst.Close()

	s := batchServer(t)
	if err := s.st.UpsertPlugin(difyPluginSlug, "Dify", "1.0.0", "{}", "bundled"); err != nil {
		t.Fatalf("UpsertPlugin: %v", err)
	}
	cfg, _ := json.Marshal(difyTargetConfig{BaseURL: dst.URL, APIKey: "k"})
	id, _ := s.st.CreateTarget(difyPluginSlug, "T", string(cfg))

	if rec := uploadToTarget(t, s, id, "admin", "big.bin", make([]byte, dify.MaxUploadBytes+1)); rec.Code != http.StatusBadRequest {
		t.Errorf("oversized upload → %d, want 400", rec.Code)
	}
	if forwarded != 0 {
		t.Errorf("an oversized file must not reach dify (forwarded %d times)", forwarded)
	}
	if rec := uploadToTarget(t, s, 99999, "admin", "a.pdf", []byte("x")); rec.Code != http.StatusNotFound {
		t.Errorf("missing target → %d, want 404", rec.Code)
	}
	if err := s.st.UpsertPlugin("custom", "Custom", "1.0.0", "{}", "bundled"); err != nil {
		t.Fatalf("UpsertPlugin: %v", err)
	}
	custom, _ := s.st.CreateTarget("custom", "C", "{}")
	if rec := uploadToTarget(t, s, custom, "admin", "a.pdf", []byte("x")); rec.Code != http.StatusNotFound {
		t.Errorf("non-dify target → %d, want 404", rec.Code)
	}
}

// A restricted OU may only upload to a workflow its allow-list grants — the file goes up with
// the target's own key, so an ungranted target must not be usable as an upload endpoint.
func TestDifyFileUploadRespectsAllowList(t *testing.T) {
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"file-1"}`)
	}))
	defer dst.Close()

	s := batchServer(t)
	if err := s.st.UpsertPlugin(difyPluginSlug, "Dify", "1.0.0", "{}", "bundled"); err != nil {
		t.Fatalf("UpsertPlugin: %v", err)
	}
	cfg, _ := json.Marshal(difyTargetConfig{BaseURL: dst.URL, APIKey: "k"})
	granted, _ := s.st.CreateTarget(difyPluginSlug, "granted", string(cfg))
	denied, _ := s.st.CreateTarget(difyPluginSlug, "denied", string(cfg))

	root := s.st.EnsureDefaultGroup()
	org, _ := s.st.CreateUserGroup("ext-org", "", 0)
	s.st.SetGroupParent(org, root)
	s.st.SetGroupRestricted(org, true)
	s.st.UpsertUser(User{Username: "ext", PasswordHash: "h", Role: "user"})
	s.st.SetPrimaryGroup("ext", org)
	if err := s.st.SetGroupTargets(org, []GroupTarget{{TargetID: granted}}); err != nil {
		t.Fatalf("SetGroupTargets: %v", err)
	}

	if rec := uploadToTarget(t, s, granted, "ext", "a.pdf", []byte("x")); rec.Code != http.StatusOK {
		t.Errorf("granted target → %d: %s", rec.Code, rec.Body.String())
	}
	if rec := uploadToTarget(t, s, denied, "ext", "a.pdf", []byte("x")); rec.Code != http.StatusForbidden {
		t.Errorf("ungranted target → %d, want 403", rec.Code)
	}
}
