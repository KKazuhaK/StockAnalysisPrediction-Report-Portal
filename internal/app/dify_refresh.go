package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/dify"
)

// Pulling a target's parameter list back from Dify.
//
// A workflow is edited on the Dify side — a field is added, renamed, made required, moved — and the
// portal's copy of its parameter list silently goes stale. Re-opening the editor and re-probing one
// target at a time is the only way to notice, which means nobody does.
//
// Two endpoints, deliberately split: a read-only preview that says what WOULD change, and an apply
// that takes the reviewed list back. Changing a target's parameters changes how every future run of
// it is submitted, so it gets the same preview-then-confirm treatment as a destructive cleanup.
//
// **Apply accepts no name.** Not "the client does not send one" — the request has nowhere to put
// one. The name is the admin's label for this target; Dify's name is shown beside it for reference
// and never written. The report subtype, the stock-code column, the address and the credential are
// equally local and equally untouched: apply rewrites the parameter list and the app mode, nothing
// else.

// difyInputDiff is what changed between the stored parameter list and the one Dify now reports.
// Each kind is named separately because "it differs" is not something an admin can approve.
type difyInputDiff struct {
	Added           []string `json:"added"`
	Removed         []string `json:"removed"`
	RequiredChanged []string `json:"required_changed"`
	Reordered       bool     `json:"reordered"`
	Changed         bool     `json:"changed"`
}

func diffInputs(local, remote []dify.Input) difyInputDiff {
	var d difyInputDiff
	lreq := map[string]bool{}
	lseen := map[string]bool{}
	for _, i := range local {
		lreq[i.Variable] = i.Required
		lseen[i.Variable] = true
	}
	rseen := map[string]bool{}
	for _, i := range remote {
		rseen[i.Variable] = true
		if !lseen[i.Variable] {
			d.Added = append(d.Added, i.Variable)
			continue
		}
		if lreq[i.Variable] != i.Required {
			d.RequiredChanged = append(d.RequiredChanged, i.Variable)
		}
	}
	for _, i := range local {
		if !rseen[i.Variable] {
			d.Removed = append(d.Removed, i.Variable)
		}
	}
	// Order is what the batch console shows as its column order, so a swap upstream is a real
	// change even when the set is identical. Reported apart from the rest: an admin who sees only
	// "reordered" can agree to it without reading a list of names.
	if len(d.Added) == 0 && len(d.Removed) == 0 {
		for i := range local {
			if i < len(remote) && local[i].Variable != remote[i].Variable {
				d.Reordered = true
				break
			}
		}
	}
	d.Changed = len(d.Added) > 0 || len(d.Removed) > 0 || len(d.RequiredChanged) > 0 || d.Reordered
	return d
}

// symbolInputLost names the stock-code parameter when the refreshed list no longer has it.
//
// Losing it does not break a run: the target keeps working and quietly stops reusing an existing
// same-day report (ADR 0022), so the portal pays to regenerate what it already has. A silent
// regression that costs money is exactly the kind that has to be said out loud.
func symbolInputLost(symbolInput string, remote []dify.Input) string {
	if strings.TrimSpace(symbolInput) == "" {
		return ""
	}
	for _, i := range remote {
		if i.Variable == symbolInput {
			return ""
		}
	}
	return symbolInput
}

// difyRefreshResult is one target's preview row.
type difyRefreshResult struct {
	ID         int64        `json:"id"`
	LocalName  string       `json:"local_name"`
	RemoteName string       `json:"remote_name"` // shown for reference; never written
	LocalMode  string       `json:"local_mode"`
	RemoteMode string       `json:"remote_mode"`
	Inputs     []dify.Input `json:"inputs"` // exactly what a confirm would write
	difyInputDiff
	SymbolInputLost string `json:"symbol_input_lost,omitempty"`
	NameDiffers     bool   `json:"name_differs"`
	Error           string `json:"error,omitempty"`        // the probe failed; nothing to apply
	InputsError     string `json:"inputs_error,omitempty"` // connected, but /parameters did not answer
}

// apiBatchDifyRefresh previews what a pull would change. Read-only: it writes nothing, so an admin
// can run it over every target without committing to anything.
func (s *Server) apiBatchDifyRefresh(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		IDs []int64 `json:"ids"` // empty = every Dify target
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	want := map[int64]bool{}
	for _, id := range in.IDs {
		want[id] = true
	}

	// One budget for the whole sweep rather than per target: "refresh all" against an unreachable
	// Dify would otherwise hang for 20s × the number of targets.
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	out := []difyRefreshResult{}
	for _, tgt := range s.st.ListTargets() {
		if tgt.PluginSlug != difyPluginSlug || (len(want) > 0 && !want[tgt.ID]) {
			continue
		}
		out = append(out, s.previewDifyRefresh(ctx, tgt))
	}
	writeJSON(w, map[string]any{"results": out})
}

func (s *Server) previewDifyRefresh(ctx context.Context, tgt BatchTarget) difyRefreshResult {
	res := difyRefreshResult{ID: tgt.ID, LocalName: tgt.Name}
	var cfg difyTargetConfig
	if err := json.Unmarshal([]byte(tgt.Config), &cfg); err != nil {
		res.Error = err.Error()
		return res
	}
	res.LocalMode = cfg.Mode
	if cfg.BaseURL == "" || cfg.APIKey == "" {
		res.Error = "target has no stored address or key"
		return res
	}

	c := dify.New(cfg.BaseURL, cfg.APIKey, &http.Client{Timeout: 20 * time.Second})
	info, err := c.Info(ctx)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.RemoteName, res.RemoteMode = info.Name, info.Mode
	res.NameDiffers = info.Name != "" && info.Name != tgt.Name

	inputs, perr := c.Parameters(ctx)
	if perr != nil {
		// Connected, but the parameter list did not answer. There is nothing to propose: an empty
		// list here means "we could not ask", not "this workflow has no inputs", and applying it
		// would wipe whatever the admin has — including anything they typed in by hand.
		res.InputsError = perr.Error()
		return res
	}
	if inputs == nil {
		inputs = []dify.Input{}
	}
	if difyModeChat(info.Mode) {
		inputs = ensureQueryInput(inputs)
	}
	res.Inputs = inputs
	res.difyInputDiff = diffInputs(cfg.Inputs, inputs)
	res.SymbolInputLost = symbolInputLost(cfg.SymbolInput, inputs)
	if res.RemoteMode != "" && res.RemoteMode != res.LocalMode {
		res.Changed = true // the app itself changed kind; that is worth applying on its own
	}
	return res
}

// apiBatchDifyRefreshApply writes back the reviewed parameter lists.
//
// It takes the inputs the admin saw rather than re-probing, so what gets written is what was on
// screen when they agreed to it.
func (s *Server) apiBatchDifyRefreshApply(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Items []struct {
			ID     int64        `json:"id"`
			Mode   string       `json:"mode"`
			Inputs []dify.Input `json:"inputs"`
		} `json:"items"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	if len(in.Items) == 0 {
		jsonError(w, http.StatusBadRequest, "no targets to apply")
		return
	}
	// Validated before anything is written, so a bad item cannot leave half the batch applied.
	for _, it := range in.Items {
		if len(it.Inputs) == 0 {
			// An empty list is what a failed /parameters looks like, and writing it would wipe a
			// list the admin may have built by hand. A workflow with genuinely no inputs has
			// nothing to refresh either, so refusing costs nothing.
			jsonErrorCode(w, http.StatusBadRequest, "dify_refresh_empty",
				"拉取到的参数为空，已放弃写入")
			return
		}
		if tgt, ok := s.st.GetTarget(it.ID); !ok || tgt.PluginSlug != difyPluginSlug {
			jsonError(w, http.StatusNotFound, "target not found")
			return
		}
	}

	applied := 0
	for _, it := range in.Items {
		tgt, _ := s.st.GetTarget(it.ID)
		var cfg difyTargetConfig
		if err := json.Unmarshal([]byte(tgt.Config), &cfg); err != nil {
			continue
		}
		before := cfg.Inputs
		cfg.Inputs = it.Inputs
		if it.Mode != "" {
			cfg.Mode = it.Mode
		}
		// Everything else on the struct rides through untouched: name is not even in scope here,
		// and BaseURL / APIKey / OutputSubtype / SymbolInput are re-marshalled as they were read.
		b, err := json.Marshal(cfg)
		if err != nil {
			continue
		}
		if err := s.st.UpdateTarget(tgt.ID, tgt.Name, string(b)); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		d := diffInputs(before, it.Inputs)
		s.recordChange(r, user, AuditTargetChange, "target", itoa64(tgt.ID), map[string]any{
			"op": "refresh", "name": tgt.Name, "added": d.Added, "removed": d.Removed,
			"required_changed": d.RequiredChanged, "reordered": d.Reordered})
		applied++
	}
	writeJSON(w, map[string]any{"ok": true, "applied": applied})
}
