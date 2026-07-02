package app

import "testing"

func TestWebhookCRUDAndEventMatching(t *testing.T) {
	st := newTestStore(t)
	id1, err := st.CreateWebhook("https://a.example/hook", []string{"report.ingested"}, "s1")
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}
	st.CreateWebhook("https://b.example/hook", []string{"batch.job.finished"}, "")
	st.CreateWebhook("https://c.example/hook", []string{"report.ingested", "batch.job.finished"}, "s3")

	if got := len(st.ListWebhooks()); got != 3 {
		t.Fatalf("ListWebhooks = %d, want 3", got)
	}

	// report.ingested → a and c (not b)
	ri := st.WebhooksForEvent("report.ingested")
	if len(ri) != 2 {
		t.Errorf("WebhooksForEvent(report.ingested) = %d, want 2", len(ri))
	}
	// event names must match exactly, not partial
	if got := st.WebhooksForEvent("report"); len(got) != 0 {
		t.Errorf("partial event name matched %d webhooks, want 0", len(got))
	}

	w, ok := st.GetWebhook(id1)
	if !ok || w.URL != "https://a.example/hook" || w.Secret != "s1" || len(w.Events) != 1 {
		t.Errorf("GetWebhook = %+v", w)
	}

	// Inactive webhooks are excluded from event matching.
	st.exec("UPDATE webhooks SET active=0 WHERE id=?", id1)
	if len(st.WebhooksForEvent("report.ingested")) != 1 {
		t.Error("an inactive webhook should not match")
	}

	st.UpdateWebhookStatus(id1, 200, "")
	if w, _ := st.GetWebhook(id1); w.LastStatus != 200 || w.LastDelivered == "" {
		t.Errorf("status not recorded: %+v", w)
	}

	if err := st.DeleteWebhook(id1); err != nil {
		t.Fatalf("DeleteWebhook: %v", err)
	}
	if _, ok := st.GetWebhook(id1); ok {
		t.Error("webhook should be gone after delete")
	}
}
