package app

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// Outbound fetches for SSO (ADR 0023): IdP metadata, OIDC discovery, JWKS, token and userinfo.
//
// Every one of these URLs is admin-supplied — and the discovery document then supplies MORE urls —
// so this is a server-side request forgery surface aimed straight at whatever the portal can reach:
// cloud metadata endpoints, internal admin panels, databases. The codebase had no address filtering
// before this.
//
// Two layers, because either alone is bypassable. The URL check rejects an obviously-bad target up
// front so an admin gets a clear error at save time; the dial-time control hook re-checks the IP the
// connection actually goes to, which is what defeats DNS rebinding (a name that resolves public on
// the first lookup and to 127.0.0.1 on the second).

type safeClient struct {
	http                 *http.Client
	allowPrivate         bool // RFC1918: self-hosted IdPs are the common case, so this is a setting
	allowInsecureForTest bool // test-only: permit plain http to an httptest server
}

const (
	safeFetchTimeout   = 10 * time.Second
	safeFetchRedirects = 3
)

func newSafeClient(allowPrivate bool) *safeClient {
	c := &safeClient{allowPrivate: allowPrivate}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second,
		// Control runs after DNS resolution with the concrete address, on every connection
		// including each redirect hop — so a name cannot resolve to something safe for the check
		// and something internal for the connection.
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			return c.checkIP(net.ParseIP(host))
		}}
	c.http = &http.Client{
		Timeout:   safeFetchTimeout,
		Transport: &http.Transport{DialContext: dialer.DialContext, ForceAttemptHTTP2: true},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= safeFetchRedirects {
				return fmt.Errorf("too many redirects")
			}
			// Re-run the full policy per hop: a permitted first request must not be able to
			// redirect onto a blocked target.
			return c.checkURL(req.URL.String())
		},
	}
	return c
}

// checkURL applies the scheme and address policy to a URL before any connection is attempted.
func (c *safeClient) checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url")
	}
	if u.Scheme != "https" && !(c.allowInsecureForTest && u.Scheme == "http") {
		return fmt.Errorf("only https is allowed")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("url has no host")
	}
	// A literal address is checked directly. A name is checked here on a best-effort basis (a
	// clear error at save time) and authoritatively again at dial time.
	if ip := net.ParseIP(host); ip != nil {
		return c.checkIP(ip)
	}
	if host == "localhost" {
		return fmt.Errorf("localhost is not allowed")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("cannot resolve %s", host)
	}
	for _, ip := range ips {
		if err := c.checkIP(ip); err != nil {
			return err
		}
	}
	return nil
}

// checkIP is the address policy. Loopback, link-local (which includes the 169.254.169.254 cloud
// metadata endpoint), CGNAT, unspecified and IPv6 unique-local are refused unconditionally —
// nothing legitimate serves an IdP there. RFC1918 is separately selectable, because a self-hosted
// Keycloak or ADFS on a private network is a normal deployment.
func (c *safeClient) checkIP(ip net.IP) error {
	if ip == nil {
		return fmt.Errorf("unresolvable address")
	}
	if c.allowInsecureForTest && ip.IsLoopback() {
		return nil // test-only, paired with the http allowance above
	}
	switch {
	case ip.IsLoopback():
		return fmt.Errorf("loopback addresses are not allowed")
	case ip.IsUnspecified(), ip.IsMulticast(), ip.IsInterfaceLocalMulticast():
		return fmt.Errorf("address is not routable")
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return fmt.Errorf("link-local addresses (incl. cloud metadata) are not allowed")
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return fmt.Errorf("CGNAT addresses are not allowed")
	}
	if !c.allowPrivate && (ip.IsPrivate() || isULA(ip)) {
		return fmt.Errorf("private addresses are not allowed (enable it in SSO settings if your IdP is internal)")
	}
	return nil
}

// isULA reports an IPv6 unique-local address (fc00::/7), which net.IP.IsPrivate covers but which is
// spelled out here so the intent survives a stdlib change.
func isULA(ip net.IP) bool {
	v6 := ip.To16()
	return ip.To4() == nil && v6 != nil && v6[0]&0xfe == 0xfc
}

// fetch GETs a URL under the full policy and returns at most limit bytes. The body is always
// bounded: a hostile or broken endpoint must not be able to exhaust memory.
func (c *safeClient) fetch(raw string, limit int64) ([]byte, error) {
	if err := c.checkURL(raw); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), safeFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("upstream returned %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}
