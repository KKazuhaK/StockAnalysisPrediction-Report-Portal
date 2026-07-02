package app

import (
	"database/sql"
	"strings"
)

// Persistence for outbound event webhooks. See docs/adr/0002-extension-architecture.md.

type Webhook struct {
	ID            int64
	URL           string
	Events        []string
	Secret        string
	Active        bool
	Created       string
	LastStatus    int
	LastError     string
	LastDelivered string
}

func (s *Store) CreateWebhook(url string, events []string, secret string) (int64, error) {
	return s.insertID(`INSERT INTO webhooks(url,events,secret,active,created_at) VALUES(?,?,?,1,?)`,
		url, strings.Join(events, ","), secret, nowStr())
}

func scanWebhook(sc interface{ Scan(...any) error }) Webhook {
	var w Webhook
	var url, events, secret, created, lastErr, lastAt sql.NullString
	var active, lastStatus sql.NullInt64
	sc.Scan(&w.ID, &url, &events, &secret, &active, &created, &lastStatus, &lastErr, &lastAt)
	w.URL, w.Secret, w.Created, w.LastError, w.LastDelivered = url.String, secret.String, created.String, lastErr.String, lastAt.String
	w.Active = active.Int64 != 0
	w.LastStatus = int(lastStatus.Int64)
	if events.String != "" {
		w.Events = strings.Split(events.String, ",")
	}
	return w
}

const webhookCols = "id,url,events,secret,active,created_at,last_status,last_error,last_delivered_at"

func (s *Store) ListWebhooks() []Webhook {
	rows, err := s.query(`SELECT ` + webhookCols + ` FROM webhooks ORDER BY id DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		out = append(out, scanWebhook(rows))
	}
	return out
}

func (s *Store) GetWebhook(id int64) (Webhook, bool) {
	row := s.queryRow(`SELECT `+webhookCols+` FROM webhooks WHERE id=?`, id)
	w := scanWebhook(row)
	return w, w.ID != 0
}

func (s *Store) DeleteWebhook(id int64) error {
	_, err := s.exec("DELETE FROM webhooks WHERE id=?", id)
	return err
}

// WebhooksForEvent returns the active webhooks subscribed to event. Membership is
// filtered in Go (not SQL LIKE) so an event name can't partial-match another.
func (s *Store) WebhooksForEvent(event string) []Webhook {
	var out []Webhook
	for _, w := range s.ListWebhooks() {
		if !w.Active {
			continue
		}
		for _, e := range w.Events {
			if e == event {
				out = append(out, w)
				break
			}
		}
	}
	return out
}

// UpdateWebhookStatus records the outcome of the most recent delivery attempt.
func (s *Store) UpdateWebhookStatus(id int64, status int, errMsg string) {
	s.exec("UPDATE webhooks SET last_status=?, last_error=?, last_delivered_at=? WHERE id=?",
		status, errMsg, nowStr(), id)
}
