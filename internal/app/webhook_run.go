package app

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/webhook"
)

// Outbound event dispatch. fireEvent is the single entry point the rest of the app
// calls when something worth broadcasting happens. See docs/adr/0002-extension-architecture.md.

// Event type constants (the initial catalogue).
const (
	EventReportIngested = "report.ingested"
	EventBatchFinished  = "batch.job.finished"
)

const (
	webhookTimeout = 15 * time.Second
	webhookRetries = 2 // transport / 5xx failures are retried this many extra times
)

// fireEvent delivers an event to every active subscriber, in the background so it
// never blocks the request that triggered it. Safe to call even with no subscribers.
func (s *Server) fireEvent(event string, payload any) {
	subs := s.st.WebhooksForEvent(event)
	if len(subs) == 0 {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("webhook: marshal %s: %v", event, err)
		return
	}
	for _, w := range subs {
		go s.deliverWebhook(w, event, body)
	}
}

// deliverWebhook delivers one event to one subscriber, retrying transport/5xx
// failures with a linear backoff, and records the final outcome for the admin UI.
func (s *Server) deliverWebhook(w Webhook, event string, body []byte) {
	client := &http.Client{Timeout: webhookTimeout}
	var status int
	var lastErr error
	for attempt := 0; attempt <= webhookRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		ctx, cancel := context.WithTimeout(context.Background(), webhookTimeout)
		status, lastErr = webhook.Deliver(ctx, client, w.URL, w.Secret, event, body)
		cancel()
		if lastErr == nil && status < 500 {
			break // 2xx/3xx/4xx are final; only transport errors and 5xx retry
		}
	}
	msg := ""
	if lastErr != nil {
		msg = lastErr.Error()
	}
	s.st.UpdateWebhookStatus(w.ID, status, msg)
}
