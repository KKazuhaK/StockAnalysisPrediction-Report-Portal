package app

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Downloading the IP database.
//
// The URL is an admin setting because every vendor spells it differently and most of them put a
// credential in the query string: MaxMind takes ?license_key=, ipinfo takes ?token=. That is the
// fact this file is built around — the URL is a SECRET, and the two places a secret escapes are
// the log and the error message shown to whoever pressed the button. Everything that can print a
// URL here goes through redactURL first.
//
// The download does not run on the request. A city database is tens of megabytes and a reverse
// proxy in front of the portal will cut a multi-minute response off at its own timeout, so the
// admin gets a 502 for a download that is actually still working. The button starts it and
// returns; the page polls the status.

const (
	geoDownloadTimeout = 5 * time.Minute
	geoMaxDownload     = 512 << 20 // a ceiling, not an expectation: GeoLite2-City is ~60 MiB
	setGeoURL          = "geoip_url"
)

// geoUpdateState is what the admin page polls while a download runs.
type geoUpdateState struct {
	Updating bool   `json:"updating"`
	LastErr  string `json:"last_error,omitempty"`
	LastFile string `json:"last_file,omitempty"`
	LastAt   string `json:"last_at,omitempty"` // RFC3339 UTC; "" = never run
	HasURL   bool   `json:"has_url"`           // whether a source is configured (never the URL itself)
}

// geoUpdater owns the download. Separate from geoService, which owns the reader: one downloads a
// file, the other notices the file changed. Keeping them apart is why an admin dropping a file in
// by hand and the updater writing one take exactly the same path afterwards.
type geoUpdater struct {
	svc *geoService
	st  *Store
	// newClient is the SSRF-guarded client factory, so a URL pointing at the portal's own network
	// is refused with the same policy that governs SSO metadata fetches.
	newClient func() *safeClient

	mu       sync.Mutex
	updating bool
	lastErr  string
	lastFile string
	lastAt   time.Time
}

func newGeoUpdater(svc *geoService, st *Store, newClient func() *safeClient) *geoUpdater {
	return &geoUpdater{svc: svc, st: st, newClient: newClient}
}

// State reports progress for the admin page.
func (u *geoUpdater) State() geoUpdateState {
	if u == nil {
		return geoUpdateState{}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	st := geoUpdateState{
		Updating: u.updating, LastErr: u.lastErr, LastFile: u.lastFile,
		HasURL: strings.TrimSpace(u.st.GetSetting(setGeoURL, "")) != "",
	}
	if !u.lastAt.IsZero() {
		st.LastAt = u.lastAt.UTC().Format(time.RFC3339)
	}
	return st
}

// Start kicks off a download and returns immediately. It refuses a second concurrent run rather
// than queueing one: two downloads would race on the same temp file, and "an update is already
// running" is the honest answer to a second click.
func (u *geoUpdater) Start() error {
	if u == nil {
		return fmt.Errorf("the updater is not configured")
	}
	raw := strings.TrimSpace(u.st.GetSetting(setGeoURL, ""))
	if raw == "" {
		return fmt.Errorf("set a database URL first")
	}
	u.mu.Lock()
	if u.updating {
		u.mu.Unlock()
		return fmt.Errorf("an update is already running")
	}
	u.updating = true
	u.mu.Unlock()

	run := func() {
		// The portal has no graceful shutdown — it is ListenAndServe and nothing else — so there is
		// no lifecycle to hang this off, and a download in flight dies with the process. That is
		// survivable precisely because of the .part file: a killed download leaves a temp file and
		// never a half-written database, and pressing the button again starts over.
		ctx, cancel := context.WithTimeout(context.Background(), geoDownloadTimeout+30*time.Second)
		defer cancel()
		file, err := u.download(ctx, raw)

		u.mu.Lock()
		u.updating = false
		u.lastAt = time.Now()
		if err != nil {
			u.lastErr = err.Error() // already redacted by download
			log.Printf("geoip: update failed: %v", err)
		} else {
			u.lastErr, u.lastFile = "", file
			log.Printf("geoip: updated to %s", file)
		}
		u.mu.Unlock()
	}
	go run()
	return nil
}

// download fetches, unpacks and installs the database, returning the filename written.
//
// Every error it returns is safe to show and to log: the URL carries a credential, so nothing
// derived from it leaves this function unredacted.
func (u *geoUpdater) download(ctx context.Context, raw string) (string, error) {
	c := u.newClient()
	if err := c.checkURL(raw); err != nil {
		return "", fmt.Errorf("%s: %w", redactURL(raw), err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", redactURLErr(raw, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", redactURLErr(raw, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The STATUS, not the body: a vendor's 401 page can echo the key back.
		return "", fmt.Errorf("%s returned %s", redactURL(raw), resp.Status)
	}

	if err := os.MkdirAll(u.svc.Dir(), 0o755); err != nil {
		return "", fmt.Errorf("cannot create %s: %w", u.svc.Dir(), err)
	}
	// Written beside the target and renamed into place, so a download that dies half way leaves a
	// .part behind and never a truncated .mmdb that the reader would then try to open.
	tmp, err := os.CreateTemp(u.svc.Dir(), "download-*.part")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // no-op once renamed
	}()

	body := io.LimitReader(resp.Body, geoMaxDownload+1)
	name, err := extractMMDB(body, resp.Header.Get("Content-Type"), raw, tmp)
	if err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	dst := filepath.Join(u.svc.Dir(), name)
	if err := os.Rename(tmpName, dst); err != nil {
		return "", err
	}
	// The reader notices by itself on the next lookup — it compares the file's mtime and size —
	// so there is nothing to signal and no restart.
	return name, nil
}

// extractMMDB writes the database out of whatever the vendor served.
//
// MaxMind ships a .tar.gz with the .mmdb nested in a dated directory; ipinfo serves the .mmdb
// directly. Sniffing the gzip magic rather than trusting Content-Type, because a CDN in front of
// either will happily label a tarball application/octet-stream.
func extractMMDB(r io.Reader, contentType, rawURL string, out io.Writer) (string, error) {
	br := &peekReader{r: r}
	magic, err := br.peek(2)
	if err != nil {
		return "", fmt.Errorf("%s: empty response", redactURL(rawURL))
	}
	if !(len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b) {
		// Not gzip: a bare .mmdb. Name it from the URL's path so two vendors' files can coexist.
		name := mmdbNameFromURL(rawURL)
		if _, err := io.Copy(out, br); err != nil {
			return "", err
		}
		return name, nil
	}
	gz, err := gzip.NewReader(br)
	if err != nil {
		return "", fmt.Errorf("%s: not a usable archive", redactURL(rawURL))
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("%s: the archive contains no .mmdb", redactURL(rawURL))
		}
		if err != nil {
			return "", fmt.Errorf("%s: %w", redactURL(rawURL), err)
		}
		if h.Typeflag != tar.TypeReg || !strings.HasSuffix(strings.ToLower(h.Name), ".mmdb") {
			continue
		}
		// The BASE name only. A tar entry's path is attacker-influenced in the general case, and
		// "../../etc/something" is the classic way an archive writes outside its directory.
		name := filepath.Base(h.Name)
		if _, err := io.Copy(out, tr); err != nil {
			return "", err
		}
		return name, nil
	}
}

// mmdbNameFromURL derives a filename for a bare download. The query is dropped, which is also
// what keeps the credential out of a filename that ends up in logs and directory listings.
func mmdbNameFromURL(raw string) string {
	name := "geoip.mmdb"
	if u, err := url.Parse(raw); err == nil {
		if b := filepath.Base(u.Path); strings.HasSuffix(strings.ToLower(b), ".mmdb") {
			name = b
		}
	}
	return name
}

// peekReader lets extractMMDB sniff the first bytes without consuming them.
type peekReader struct {
	r   io.Reader
	buf []byte
}

func (p *peekReader) peek(n int) ([]byte, error) {
	for len(p.buf) < n {
		chunk := make([]byte, n-len(p.buf))
		m, err := p.r.Read(chunk)
		p.buf = append(p.buf, chunk[:m]...)
		if err != nil {
			if len(p.buf) > 0 {
				return p.buf, nil
			}
			return nil, err
		}
	}
	return p.buf, nil
}

func (p *peekReader) Read(b []byte) (int, error) {
	if len(p.buf) > 0 {
		n := copy(b, p.buf)
		p.buf = p.buf[n:]
		return n, nil
	}
	return p.r.Read(b)
}

// redactURL keeps the host and path and drops the query, because the query is where every vendor
// puts the credential — MaxMind's license_key, ipinfo's token. An operator needs to know WHICH
// host failed; nobody needs the key in a log file.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "the configured URL"
	}
	return u.Scheme + "://" + u.Host + u.Path
}

// redactURLErr rebuilds an error that carries a URL. net/http returns *url.Error, whose Error()
// prints the whole URL including the query — so wrapping it without this puts the key straight
// into the log line and into the admin's status box.
func redactURLErr(raw string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s %s: %w", ue.Op, redactURL(raw), ue.Err)
	}
	// Belt and braces: an error from elsewhere may still have interpolated the URL.
	msg := strings.ReplaceAll(err.Error(), raw, redactURL(raw))
	return errors.New(msg)
}

// GET /api/admin/geoip — what is loaded and how the last download went.
func (s *Server) apiGeoStatus(w http.ResponseWriter, r *http.Request, user string) {
	writeJSON(w, map[string]any{"status": s.geo.Status(), "update": s.geoUp.State()})
}

// POST /api/admin/geoip — set where the database is downloaded from.
//
// The URL carries the vendor's credential, so it is write-only on this surface: the status
// endpoint reports whether one is configured and never what it is, the same rule the SMTP password
// and every client secret already follow. An empty value clears it.
func (s *Server) apiGeoSetURL(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		URL string `json:"url"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	raw := strings.TrimSpace(in.URL)
	if raw != "" {
		if err := s.ssoClient().checkURL(raw); err != nil {
			// Redacted, because an admin pasting a URL with a key in it should not get it echoed
			// back into a toast that may be screenshotted.
			jsonErrorCode(w, http.StatusBadRequest, "geoip_bad_url",
				redactURL(raw)+"："+err.Error())
			return
		}
	}
	s.st.SetSetting(setGeoURL, raw)
	s.recordChange(r, user, AuditPolicyChange, "geoip", "", map[string]any{"op": "set_url", "configured": raw != ""})
	writeJSON(w, okJSON)
}

// POST /api/admin/geoip/update — start a download and return at once.
//
// Returning immediately is the point: a city database is tens of megabytes, and a reverse proxy
// will cut a multi-minute response off at its own timeout, so an admin would see a 502 for a
// download that is in fact still running. The page polls the status instead.
func (s *Server) apiGeoUpdate(w http.ResponseWriter, r *http.Request, user string) {
	if err := s.geoUp.Start(); err != nil {
		jsonErrorCode(w, http.StatusConflict, "geoip_update_busy", err.Error())
		return
	}
	s.recordChange(r, user, AuditPolicyChange, "geoip", "", map[string]any{"op": "update"})
	writeJSON(w, okJSON)
}
