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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/geoip"
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

	setGeoEnabled    = "geoip_enabled"
	setGeoFile       = "geoip_db_file"    // which .mmdb to use when several are present ("" = auto)
	setGeoAuto       = "geoip_auto"       // run the updater on a timer
	setGeoAutoHours  = "geoip_auto_hours" // how often, minimum 1
	setGeoSource     = "geoip_source"     // maxmind | dbip | ipinfo | custom
	setGeoEdition    = "geoip_edition"    // the vendor's product name, e.g. GeoLite2-City
	setGeoToken      = "geoip_token"      // licence key / token; write-only on the API
	setGeoURL        = "geoip_url"        // custom source only
	geoDefaultHours  = 12
	geoDefaultSource = "maxmind"
)

// geoSources is the set an admin may pick. candidateURLs implements exactly one branch per entry,
// and a test walks this list to prove none has been added here without being handled there — the
// two drifting apart would produce a source that saves and then fails at download time.
var geoSources = []string{"maxmind", "dbip", "ipinfo", "custom"}

func validGeoSource(v string) bool {
	if strings.TrimSpace(v) == "" {
		return true // means the default
	}
	for _, s := range geoSources {
		if s == v {
			return true
		}
	}
	return false
}

// candidateURLs builds the download URL(s) and the filename to write, for the configured source.
//
// The URL is CONSTRUCTED rather than typed, because the shape is the vendor's business and the
// admin only knows their own key: pasting a whole URL is how people end up with the wrong edition,
// a missing suffix, or a key in their clipboard history.
//
// The target filename is derived from the EDITION, not from the archive, so an update replaces the
// previous file instead of accumulating one per download and leaving the picker to guess.
//
// DB-IP publishes a month-stamped file with no "latest" alias, so early in a month the current
// month may not exist yet — hence a list, tried in order.
func candidateURLs(src, edition, token, custom string) (urls []string, target string, err error) {
	src = strings.TrimSpace(src)
	if src == "" {
		src = geoDefaultSource
	}
	edition, token, custom = strings.TrimSpace(edition), strings.TrimSpace(token), strings.TrimSpace(custom)
	switch src {
	case "maxmind":
		if token == "" {
			return nil, "", fmt.Errorf("MaxMind needs a licence key")
		}
		if edition == "" {
			edition = "GeoLite2-City"
		}
		u := "https://download.maxmind.com/app/geoip_download?edition_id=" + url.QueryEscape(edition) +
			"&license_key=" + url.QueryEscape(token) + "&suffix=tar.gz"
		return []string{u}, filepath.Base(edition) + ".mmdb", nil
	case "ipinfo":
		if token == "" {
			return nil, "", fmt.Errorf("IPinfo needs a token")
		}
		if edition == "" {
			edition = "ipinfo_lite"
		}
		// The edition goes in the PATH here, so it is path-escaped; Base on the target is what
		// stops an edition of "../../x" writing outside the directory.
		u := "https://ipinfo.io/data/" + url.PathEscape(edition) + ".mmdb?token=" + url.QueryEscape(token)
		return []string{u}, filepath.Base(edition) + ".mmdb", nil
	case "dbip":
		now := time.Now()
		return []string{dbipURL(now), dbipURL(prevMonthOf(now))}, "dbip-city-lite.mmdb", nil
	case "custom":
		if custom == "" {
			return nil, "", fmt.Errorf("a custom source needs a download URL")
		}
		return []string{custom}, customTarget(custom), nil
	}
	return nil, "", fmt.Errorf("unknown source %q", src)
}

func dbipURL(t time.Time) string {
	return "https://download.db-ip.com/free/dbip-city-lite-" + t.Format("2006-01") + ".mmdb.gz"
}

// prevMonthOf is the month before t's. Written as "the 1st of this month, minus a day" rather than
// AddDate(0,-1,0), which normalises the 31st back into the SAME month — silently making the
// fallback URL identical to the one that just failed.
func prevMonthOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location()).AddDate(0, 0, -1)
}

// customTarget derives a filename from an arbitrary URL: the base name without any .gz/.tar
// wrapper. Base is what blocks traversal, and the query is dropped so a credential in it cannot
// end up in a filename that appears in logs and directory listings.
func customTarget(raw string) string {
	u := raw
	if i := strings.IndexByte(u, '?'); i >= 0 {
		u = u[:i]
	}
	base := filepath.Base(u)
	base = strings.TrimSuffix(base, ".gz")
	base = strings.TrimSuffix(base, ".tar")
	if !strings.HasSuffix(strings.ToLower(base), ".mmdb") || base == ".mmdb" {
		base = "custom.mmdb"
	}
	return base
}

// geoUpdateState is what the admin page polls while a download runs.
type geoUpdateState struct {
	Updating bool   `json:"updating"`
	LastErr  string `json:"last_error,omitempty"`
	LastFile string `json:"last_file,omitempty"`
	LastAt   string `json:"last_at,omitempty"` // RFC3339 UTC; "" = never run
	HasKey   bool   `json:"has_key"`           // whether a credential is stored (never the credential)
	// The rest of the configuration, so the form can bind to it. The credential is not here and
	// never will be: has_key is the only thing this surface says about it.
	Auto      bool   `json:"auto"`
	AutoHours int    `json:"auto_hours"`
	Source    string `json:"source"`
	Edition   string `json:"edition"`
	URL       string `json:"url"`
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
	hours := geoDefaultHours
	if n, err := strconv.Atoi(strings.TrimSpace(u.st.GetSetting(setGeoAutoHours, ""))); err == nil && n >= 1 {
		hours = n
	}
	src := u.st.GetSetting(setGeoSource, "")
	if src == "" {
		src = geoDefaultSource
	}
	st := geoUpdateState{
		Updating: u.updating, LastErr: u.lastErr, LastFile: u.lastFile,
		HasKey:    strings.TrimSpace(u.st.GetSetting(setGeoToken, "")) != "",
		Auto:      u.st.GetSetting(setGeoAuto, "0") == "1",
		AutoHours: hours,
		Source:    src,
		Edition:   u.st.GetSetting(setGeoEdition, ""),
		// The custom URL is shown back because it is the one source where the admin typed the whole
		// thing and cannot re-derive it — and where a credential belongs in the token field, not here.
		URL: u.st.GetSetting(setGeoURL, ""),
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
	urls, target, err := u.plan()
	if err != nil {
		return err
	}
	u.mu.Lock()
	if u.updating {
		u.mu.Unlock()
		return fmt.Errorf("an update is already running")
	}
	u.updating = true
	u.mu.Unlock()

	go func() {
		// The portal has no graceful shutdown — it is ListenAndServe and nothing else — so there is
		// no lifecycle to hang this off, and a download in flight dies with the process. That is
		// survivable precisely because of the .part file: a killed download leaves a temp file and
		// never a half-written database.
		ctx, cancel := context.WithTimeout(context.Background(), geoDownloadTimeout+30*time.Second)
		defer cancel()
		file, err := u.run(ctx, urls, target)

		u.mu.Lock()
		u.updating = false
		u.lastAt = time.Now()
		if err != nil {
			u.lastErr = err.Error() // already redacted
			log.Printf("geoip: update failed: %v", err)
		} else {
			u.lastErr, u.lastFile = "", file
			log.Printf("geoip: updated to %s", file)
		}
		u.mu.Unlock()
	}()
	return nil
}

// plan resolves the configured source into URLs and a target filename, before anything is marked
// as running — so a misconfiguration is an immediate error on the button rather than a background
// failure the admin has to go and poll for.
func (u *geoUpdater) plan() ([]string, string, error) {
	return candidateURLs(
		u.st.GetSetting(setGeoSource, ""),
		u.st.GetSetting(setGeoEdition, ""),
		u.st.GetSetting(setGeoToken, ""),
		u.st.GetSetting(setGeoURL, ""),
	)
}

// run tries each candidate in turn and installs the first that works.
func (u *geoUpdater) run(ctx context.Context, urls []string, target string) (string, error) {
	var last error
	for _, raw := range urls {
		name, err := u.download(ctx, raw, target)
		if err == nil {
			return name, nil
		}
		last = err
	}
	return "", last
}

// download fetches, unpacks, VALIDATES and installs one candidate.
//
// Every error it returns is safe to show and to log: the URL carries a credential, so nothing
// derived from it leaves this function unredacted.
// vendorMessage is the vendor's own explanation of a rejection, made safe to show.
//
// The status alone does not say which thing was wrong: MaxMind answers a bad licence key with
// exactly "Invalid license key", and an admin left with "401 Unauthorized" has nothing to act on.
// What makes a rejection body unsafe to echo is that it can quote the request back, credential
// included — so every query value we did not put there ourselves is scrubbed out of it first, and
// an HTML page (noise, and the place a key is most likely to hide inside markup) is dropped whole.
func vendorMessage(raw string, body io.Reader) string {
	b, err := io.ReadAll(io.LimitReader(body, 1024))
	if err != nil {
		return ""
	}
	msg := strings.Join(strings.Fields(string(b)), " ") // one line, so it fits an error box
	if msg == "" || strings.HasPrefix(msg, "<") {
		return ""
	}
	if u, perr := url.Parse(raw); perr == nil {
		for name, vals := range u.Query() {
			// An allowlist, not a list of credential-looking names: a custom source may call its
			// key anything, so anything we do not recognise is assumed to be one.
			if name == "edition_id" || name == "suffix" {
				continue
			}
			for _, v := range vals {
				if v != "" {
					msg = strings.ReplaceAll(msg, v, "\u2026")
				}
			}
		}
	}
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "\u2026" // runes, so a multi-byte character is never cut in half
	}
	return msg
}

func (u *geoUpdater) download(ctx context.Context, raw, target string) (string, error) {
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
		if msg := vendorMessage(raw, resp.Body); msg != "" {
			return "", fmt.Errorf("%s returned %s: %s", redactURL(raw), resp.Status, msg)
		}
		return "", fmt.Errorf("%s returned %s", redactURL(raw), resp.Status)
	}

	dir := u.svc.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cannot create %s: %w", dir, err)
	}
	// The PID is in the temp name because the in-process single-flight does not span PROCESSES:
	// during a rolling restart two of them can share the data volume, and without it they would
	// write the same .part over each other. The rename at the end is atomic, so the later one wins
	// cleanly rather than producing a torn file.
	tmp := filepath.Join(dir, fmt.Sprintf("%s.part.%d", target, os.Getpid()))
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	cleanup := func() { f.Close(); os.Remove(tmp) }

	body := io.LimitReader(resp.Body, geoMaxDownload+1)
	if err := extractMMDB(body, raw, f); err != nil {
		cleanup()
		return "", err
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}

	// Parsed BEFORE the live file is replaced. A truncated download or an HTML error page served
	// with a 200 would otherwise overwrite a working database with something unreadable, and the
	// feature would go dark until somebody noticed.
	if rd, oerr := geoip.Open(tmp); oerr != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("%s: the downloaded file is not a usable database: %w", redactURL(raw), oerr)
	} else {
		rd.Close()
	}

	if err := u.svc.replace(tmp, filepath.Join(dir, filepath.Base(target))); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return filepath.Base(target), nil
}

// extractMMDB writes the database out of whatever the vendor served.
//
// MaxMind ships a .tar.gz with the .mmdb nested in a dated directory; ipinfo serves the .mmdb
// directly. Sniffing the gzip magic rather than trusting Content-Type, because a CDN in front of
// either will happily label a tarball application/octet-stream.
func extractMMDB(r io.Reader, rawURL string, out io.Writer) error {
	br := &peekReader{r: r}
	magic, err := br.peek(2)
	if err != nil {
		return fmt.Errorf("%s: empty response", redactURL(rawURL))
	}
	gzipped := len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b
	if !gzipped {
		// A bare .mmdb, as IPinfo serves.
		_, err := io.Copy(out, br)
		return err
	}
	gz, err := gzip.NewReader(br)
	if err != nil {
		return fmt.Errorf("%s: not a usable archive", redactURL(rawURL))
	}
	defer gz.Close()

	// Two gzip shapes: MaxMind wraps a tar, DB-IP gzips the database directly. Peek at the
	// DECOMPRESSED stream for the tar magic ("ustar" at offset 257) rather than trusting the file
	// extension, which a redirect or a CDN can make a liar of.
	inner := &peekReader{r: gz}
	head, _ := inner.peek(262)
	if len(head) >= 262 && string(head[257:262]) == "ustar" {
		tr := tar.NewReader(inner)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("%s: the archive contains no .mmdb", redactURL(rawURL))
			}
			if err != nil {
				return fmt.Errorf("%s: %w", redactURL(rawURL), err)
			}
			if h.Typeflag != tar.TypeReg || !strings.HasSuffix(strings.ToLower(h.Name), ".mmdb") {
				continue
			}
			_, err = io.Copy(out, tr)
			return err
		}
	}
	_, err = io.Copy(out, inner)
	return err
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

// autoLoop refreshes the database on a timer.
//
// MaxMind's GeoLite2 licence requires the data to be refreshed at least every 30 days, so leaving
// this to a human is leaving it to a licence breach. It ticks every minute and compares elapsed
// time against the configured interval rather than sleeping for the interval, so changing the
// interval — or turning it on — takes effect within a minute instead of at the next restart.
func (u *geoUpdater) autoLoop() {
	for range time.Tick(time.Minute) {
		if u.st.GetSetting(setGeoAuto, "0") != "1" {
			continue
		}
		hours := geoDefaultHours
		if n, err := strconv.Atoi(strings.TrimSpace(u.st.GetSetting(setGeoAutoHours, ""))); err == nil && n >= 1 {
			hours = n
		}
		u.mu.Lock()
		due := u.lastAt.IsZero() || time.Since(u.lastAt) >= time.Duration(hours)*time.Hour
		running := u.updating
		u.mu.Unlock()
		if !due || running {
			continue
		}
		// Start does its own single-flight and its own configuration check, so a missing key here
		// is a recorded failure rather than a crash — and lastAt advances either way, which is what
		// stops a broken configuration from retrying every minute for ever.
		if err := u.Start(); err != nil {
			u.mu.Lock()
			u.lastErr, u.lastAt = err.Error(), time.Now()
			u.mu.Unlock()
		}
	}
}

// GET /api/admin/geoip — what is loaded and how the last download went.
func (s *Server) apiGeoStatus(w http.ResponseWriter, r *http.Request, user string) {
	writeJSON(w, map[string]any{"status": s.geo.Status(), "update": s.geoUp.State()})
}

// POST /api/admin/geoip — save the settings.
//
// The credential is a POINTER, so "omitted" and "cleared" are different: the form sends it only
// when the admin actually typed a new one, which is what lets the field show "saved, unchanged"
// instead of either echoing the key back or silently wiping it on every save. Same rule the SMTP
// password and every client secret already follow.
func (s *Server) apiGeoSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Enabled   *bool   `json:"enabled"`
		File      *string `json:"file"`
		Auto      *bool   `json:"auto"`
		AutoHours *int    `json:"auto_hours"`
		Source    *string `json:"source"`
		Edition   *string `json:"edition"`
		URL       *string `json:"url"`
		Token     *string `json:"token"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	if in.Source != nil && !validGeoSource(*in.Source) {
		jsonErrorCode(w, http.StatusBadRequest, "geoip_bad_source", "未知的更新来源")
		return
	}
	if in.URL != nil {
		if raw := strings.TrimSpace(*in.URL); raw != "" {
			if err := s.ssoClient().checkURL(raw); err != nil {
				// Redacted: an admin pasting a URL with a key in it should not get it echoed back
				// into a toast that might be screenshotted.
				jsonErrorCode(w, http.StatusBadRequest, "geoip_bad_url", redactURL(raw)+"："+err.Error())
				return
			}
		}
	}
	set := func(key string, v *string) {
		if v != nil {
			s.st.SetSetting(key, strings.TrimSpace(*v))
		}
	}
	setBool := func(key string, v *bool) {
		if v != nil {
			s.st.SetSetting(key, boolSetting(*v))
		}
	}
	setBool(setGeoEnabled, in.Enabled)
	setBool(setGeoAuto, in.Auto)
	// filepath.Base, because this names a file inside the geoip directory and arrives from a form.
	if in.File != nil {
		s.st.SetSetting(setGeoFile, filepath.Base(strings.TrimSpace(*in.File)))
	}
	set(setGeoSource, in.Source)
	set(setGeoEdition, in.Edition)
	set(setGeoURL, in.URL)
	set(setGeoToken, in.Token)
	if in.AutoHours != nil {
		h := *in.AutoHours
		if h < 1 {
			h = 1 // an interval of zero would be a download loop
		}
		s.st.SetSetting(setGeoAutoHours, strconv.Itoa(h))
	}
	// Field names, never the credential.
	s.recordChange(r, user, AuditPolicyChange, "geoip", "",
		map[string]any{"op": "save", "fields": changedSettingFields(in)})
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
