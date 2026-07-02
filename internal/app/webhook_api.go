package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/webhook"
)

// Admin HTTP handlers for managing outbound webhooks (PermManage). The secret is
// write-only: it is never returned to the browser (only has_secret).

func availableEvents() []string { return []string{EventReportIngested, EventBatchFinished} }

func webhookJSON(w Webhook) map[string]any {
	return map[string]any{
		"id": w.ID, "url": w.URL, "events": w.Events, "active": w.Active, "created_at": w.Created,
		"has_secret": w.Secret != "", "last_status": w.LastStatus, "last_error": w.LastError,
		"last_delivered_at": w.LastDelivered,
	}
}

func (s *Server) apiWebhooks(w http.ResponseWriter, r *http.Request, user string) {
	out := make([]map[string]any, 0)
	for _, wh := range s.st.ListWebhooks() {
		out = append(out, webhookJSON(wh))
	}
	writeJSON(w, map[string]any{"webhooks": out, "events": availableEvents()})
}

func (s *Server) apiWebhookAdd(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		URL    string   `json:"url"`
		Events []string `json:"events"`
		Secret string   `json:"secret"`
	}
	if err := readJSON(r, &in); err != nil || strings.TrimSpace(in.URL) == "" || len(in.Events) == 0 {
		jsonError(w, http.StatusBadRequest, "url and at least one event are required")
		return
	}
	id, err := s.st.CreateWebhook(strings.TrimSpace(in.URL), in.Events, in.Secret)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

func (s *Server) apiWebhookDelete(w http.ResponseWriter, r *http.Request, user string) {
	if err := s.st.DeleteWebhook(pathID(r, "id")); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

// apiWebhookTest delivers a synchronous "ping" so the admin can confirm a
// subscriber is reachable and verifying signatures correctly.
func (s *Server) apiWebhookTest(w http.ResponseWriter, r *http.Request, user string) {
	wh, ok := s.st.GetWebhook(pathID(r, "id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "webhook not found")
		return
	}
	body, _ := json.Marshal(map[string]any{"event": "ping", "message": "test delivery from report portal", "at": nowStr()})
	ctx, cancel := context.WithTimeout(r.Context(), webhookTimeout)
	defer cancel()
	status, err := webhook.Deliver(ctx, &http.Client{Timeout: webhookTimeout}, wh.URL, wh.Secret, "ping", body)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	s.st.UpdateWebhookStatus(wh.ID, status, msg)
	writeJSON(w, map[string]any{"ok": err == nil && status < 400, "status": status, "error": msg})
}
