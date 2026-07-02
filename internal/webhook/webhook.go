// Package webhook is the outbound-event delivery core: it signs and POSTs event
// payloads to subscriber URLs. It is one of the portal's extension points — see
// docs/adr/0002-extension-architecture.md. Subscriber lookup and retry policy live
// in internal/app; this package is the pure, testable delivery primitive.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
)

const (
	// SignatureHeader carries "sha256=<hex>" so subscribers can verify authenticity.
	SignatureHeader = "X-Report-Portal-Signature"
	// EventHeader carries the event type (e.g. "report.ingested").
	EventHeader = "X-Report-Portal-Event"
)

// Sign returns the hex HMAC-SHA256 of body keyed by secret.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// Deliver POSTs a signed webhook to url and returns the response status code. A
// non-2xx status is returned without an error; only transport failures return an
// error. The caller decides retry and logging.
func Deliver(ctx context.Context, client *http.Client, url, secret, event string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(EventHeader, event)
	if secret != "" {
		req.Header.Set(SignatureHeader, "sha256="+Sign(secret, body))
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, nil
}
