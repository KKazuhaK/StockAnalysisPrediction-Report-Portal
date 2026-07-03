package app

import (
	"context"
	"encoding/json"
	"net/http"
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
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	base, key := strings.TrimSpace(in.BaseURL), strings.TrimSpace(in.APIKey)
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
	cfg, _ := json.Marshal(difyTargetConfig{BaseURL: base, APIKey: key, Inputs: in.Inputs})
	id, err := s.st.CreateTarget(difyPluginSlug, name, string(cfg))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

// difyInputsJSON maps a Dify target's stored inputs to the {key,label,required}
// shape the run form expects (same as a manifest plugin's InputDecl).
func difyInputsJSON(configJSON string) []map[string]any {
	ins := difyTargetInputs(configJSON)
	out := make([]map[string]any, 0, len(ins))
	for _, in := range ins {
		out = append(out, map[string]any{"key": in.Variable, "label": in.Label, "required": in.Required})
	}
	return out
}
