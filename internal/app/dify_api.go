package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/dify"
)

// HTTP surface for Dify-native target configuration (docs/adr/0006-dify-native.md):
// probe a workflow from an API key, then save it as a target. Admin-only.

// apiBatchDifyProbe connects to a Dify workflow with the given base_url + api_key and
// returns its name + input fields, so the admin configures a target by pasting a key.
// Read-only on Dify's side (GET /info + /parameters).
func (s *Server) apiBatchDifyProbe(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		BaseURL  string `json:"base_url"`
		APIKey   string `json:"api_key"`
		TargetID int64  `json:"target_id"` // optional: reuse this Dify target's stored key/base
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	base, key := strings.TrimSpace(in.BaseURL), strings.TrimSpace(in.APIKey)
	// Re-probing an existing target: fall back to its stored key (and base) so refreshing
	// inputs doesn't force re-pasting the secret we already hold.
	if in.TargetID != 0 && (key == "" || base == "") {
		if tgt, ok := s.st.GetTarget(in.TargetID); ok && tgt.PluginSlug == difyPluginSlug {
			var cfg difyTargetConfig
			json.Unmarshal([]byte(tgt.Config), &cfg)
			if key == "" {
				key = cfg.APIKey
			}
			if base == "" {
				base = cfg.BaseURL
			}
		}
	}
	if base == "" || key == "" {
		jsonError(w, http.StatusBadRequest, "base_url and api_key are required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	c := dify.New(base, key, &http.Client{Timeout: 20 * time.Second})
	info, err := c.Info(ctx)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "connect failed: "+err.Error())
		return
	}
	// If /parameters fails (e.g. an unhealthy Dify), still return the name so the
	// admin can add the input columns manually.
	inputs, perr := c.Parameters(ctx)
	if inputs == nil {
		inputs = []dify.Input{}
	}
	// A chat/agent app takes a `query` message; make it a batch column too.
	if difyModeChat(info.Mode) {
		inputs = ensureQueryInput(inputs)
	}
	out := map[string]any{"name": info.Name, "mode": info.Mode, "inputs": inputs}
	if perr != nil {
		out["inputs_error"] = perr.Error()
	}
	writeJSON(w, out)
}

// apiBatchDifyTargetAdd creates a Dify target from a (probed or hand-entered) config.
func (s *Server) apiBatchDifyTargetAdd(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Name    string       `json:"name"`
		BaseURL string       `json:"base_url"`
		APIKey  string       `json:"api_key"`
		Mode    string       `json:"mode"`
		Inputs  []dify.Input `json:"inputs"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	name, base, key := strings.TrimSpace(in.Name), strings.TrimSpace(in.BaseURL), strings.TrimSpace(in.APIKey)
	if name == "" || base == "" || key == "" {
		jsonError(w, http.StatusBadRequest, "name, base_url and api_key are required")
		return
	}
	cfg, _ := json.Marshal(difyTargetConfig{BaseURL: base, APIKey: key, Mode: in.Mode, Inputs: in.Inputs})
	id, err := s.st.CreateTarget(difyPluginSlug, name, string(cfg))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A target is where reports come FROM: its base URL and key decide which external service
	// the portal hands stock codes to and trusts the answers of. The name and the address,
	// never the api_key.
	s.recordChange(r, user, AuditTargetChange, "target", itoa64(id),
		map[string]any{"op": "create", "name": name, "base_url": in.BaseURL})
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

// apiBatchDifyTargetGet returns a Dify target's editable config for the edit modal.
// It deliberately never surfaces the api_key (only whether one is stored), so the
// secret stays server-side; the form leaves the key blank to keep the current one.
func (s *Server) apiBatchDifyTargetGet(w http.ResponseWriter, r *http.Request, user string) {
	tgt, ok := s.st.GetTarget(pathID(r, "id"))
	if !ok || tgt.PluginSlug != difyPluginSlug {
		jsonError(w, http.StatusNotFound, "target not found")
		return
	}
	var cfg difyTargetConfig
	json.Unmarshal([]byte(tgt.Config), &cfg)
	inputs := cfg.Inputs
	if inputs == nil {
		inputs = []dify.Input{}
	}
	writeJSON(w, map[string]any{
		"id": tgt.ID, "name": tgt.Name, "base_url": cfg.BaseURL, "mode": cfg.Mode,
		"inputs": inputs, "has_key": cfg.APIKey != "",
		"output_subtype": cfg.OutputSubtype, "symbol_input": cfg.SymbolInput,
	})
}

// apiBatchDifyTargetUpdate edits a Dify target's name, base_url, inputs, and
// (optionally) api_key. A blank api_key keeps the stored one so editing the
// name/inputs never forces re-entering the secret.
func (s *Server) apiBatchDifyTargetUpdate(w http.ResponseWriter, r *http.Request, user string) {
	tgt, ok := s.st.GetTarget(pathID(r, "id"))
	if !ok || tgt.PluginSlug != difyPluginSlug {
		jsonError(w, http.StatusNotFound, "target not found")
		return
	}
	var in struct {
		Name    string       `json:"name"`
		BaseURL string       `json:"base_url"`
		APIKey  string       `json:"api_key"`
		Mode    string       `json:"mode"`
		Inputs  []dify.Input `json:"inputs"`
		// Pointers so an omitted field keeps the stored value (an ordinary rename must never wipe
		// them and silently disable same-day reuse), while an explicit "" clears it.
		OutputSubtype *string `json:"output_subtype"`
		SymbolInput   *string `json:"symbol_input"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	name, base, key := strings.TrimSpace(in.Name), strings.TrimSpace(in.BaseURL), strings.TrimSpace(in.APIKey)
	if name == "" || base == "" {
		jsonError(w, http.StatusBadRequest, "name and base_url are required")
		return
	}
	var cur difyTargetConfig
	json.Unmarshal([]byte(tgt.Config), &cur)
	subtype, symbolInput := cur.OutputSubtype, cur.SymbolInput
	if in.OutputSubtype != nil {
		subtype = strings.TrimSpace(*in.OutputSubtype)
	}
	if in.SymbolInput != nil {
		symbolInput = strings.TrimSpace(*in.SymbolInput)
	}
	if key == "" {
		key = cur.APIKey // blank → keep the stored key
	}
	if key == "" {
		jsonError(w, http.StatusBadRequest, "api_key is required")
		return
	}
	mode := in.Mode
	if mode == "" {
		mode = cur.Mode // blank → keep the stored mode
	}
	cfg, _ := json.Marshal(difyTargetConfig{BaseURL: base, APIKey: key, Mode: mode, Inputs: in.Inputs,
		OutputSubtype: subtype, SymbolInput: symbolInput})
	if err := s.st.UpdateTarget(tgt.ID, name, string(cfg)); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Both sides of the address, because repointing a target at a different service is the
	// change that matters here and it is invisible in a row that only says "edited".
	s.recordChange(r, user, AuditTargetChange, "target", itoa64(tgt.ID), map[string]any{
		"op": "update", "name": name, "base_url_from": cur.BaseURL, "base_url_to": base,
		"key_rotated": key != cur.APIKey})
	writeJSON(w, okJSON)
}

// ensureQueryInput prepends the chat `query` field to a chat app's discovered inputs
// (the batch CSV needs a query column) unless the app already declares it.
func ensureQueryInput(inputs []dify.Input) []dify.Input {
	for _, in := range inputs {
		if in.Variable == "query" {
			return inputs
		}
	}
	return append([]dify.Input{{Variable: "query", Label: "query", Type: "paragraph", Required: true}}, inputs...)
}

// difyInputsJSON maps a Dify target's stored inputs to the {key,label,type,required}
// shape the run form expects (same as a manifest plugin's InputDecl). type is Dify's own
// field type, which is what tells the form to render a file picker instead of a text box.
func difyInputsJSON(configJSON string) []map[string]any {
	ins := difyTargetInputs(configJSON)
	out := make([]map[string]any, 0, len(ins))
	for _, in := range ins {
		row := map[string]any{"key": in.Variable, "label": in.Label, "type": in.Type, "required": in.Required}
		if len(in.Options) > 0 {
			row["options"] = in.Options // a select without its choices renders as a text box, which is not the field the workflow declared
		}
		out = append(out, row)
	}
	return out
}

// difyUploadTimeout bounds one file-upload proxy call. Generous next to a probe (a 15MB body
// crosses the wire twice, browser → portal → Dify) but far below a run's budget.
const difyUploadTimeout = 2 * time.Minute

// apiBatchDifyFileUpload proxies one file from the run form to the target's own Dify instance
// and hands back the file id the row carries. The browser cannot upload straight to Dify: that
// would need the app's service key in the page, and the key is the one thing this feature keeps
// server-side (same reason apiBatchDifyTargetGet never returns it).
func (s *Server) apiBatchDifyFileUpload(w http.ResponseWriter, r *http.Request, user string) {
	tgt, ok := s.st.GetTarget(pathID(r, "id"))
	if !ok || tgt.PluginSlug != difyPluginSlug {
		jsonError(w, http.StatusNotFound, "target not found")
		return
	}
	// The file is pushed with the target's own key, so a restricted OU may only upload to a
	// workflow its allow-list grants (ADR 0022 R3). The per-surface check stays on submit, where
	// the surface is actually known — an upload is not yet a run.
	if s.viewerScope(user) != nil && !s.targetGranted(user, tgt.ID) {
		jsonError(w, http.StatusForbidden, "this workflow is not available to your group")
		return
	}
	var cfg difyTargetConfig
	json.Unmarshal([]byte(tgt.Config), &cfg)
	if cfg.BaseURL == "" || cfg.APIKey == "" {
		jsonError(w, http.StatusBadRequest, "target is missing base_url or api_key")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, dify.MaxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(dify.MaxUploadBytes + 1024); err != nil {
		jsonErrorCode(w, http.StatusBadRequest, "upload_too_large", "上传文件过大")
		return
	}
	f, header, err := r.FormFile("file")
	if err != nil {
		jsonErrorCode(w, http.StatusBadRequest, "upload_no_file", "请选择要上传的文件")
		return
	}
	defer f.Close()
	// Read one byte past the cap so a file that lies about its size in the multipart header is
	// still caught by what actually arrived.
	raw, err := io.ReadAll(io.LimitReader(f, dify.MaxUploadBytes+1))
	if err != nil || len(raw) == 0 {
		jsonErrorCode(w, http.StatusBadRequest, "upload_unreadable", "读取上传文件失败")
		return
	}
	if len(raw) > dify.MaxUploadBytes {
		jsonErrorCode(w, http.StatusBadRequest, "upload_too_large", "上传文件过大")
		return
	}
	name := filepath.Base(header.Filename)
	if name == "." || name == string(filepath.Separator) {
		name = "file"
	}
	ctx, cancel := context.WithTimeout(r.Context(), difyUploadTimeout)
	defer cancel()
	c := dify.New(cfg.BaseURL, cfg.APIKey, &http.Client{Timeout: difyUploadTimeout})
	// The SAME end-user identity the run will be recorded under: Dify scopes an uploaded file to
	// its uploader, so uploading as anyone else leaves the id unresolvable at run time.
	up, err := c.UploadFile(ctx, name, bytes.NewReader(raw), s.difyEndUser(user))
	if err != nil {
		jsonError(w, http.StatusBadGateway, "upload failed: "+err.Error())
		return
	}
	size := up.Size
	if size == 0 {
		size = int64(len(raw)) // Dify omitted it; what we sent is the honest answer
	}
	if up.Name != "" {
		name = up.Name
	}
	writeJSON(w, map[string]any{"ok": true, "file_id": up.ID, "name": name, "size": size})
}
