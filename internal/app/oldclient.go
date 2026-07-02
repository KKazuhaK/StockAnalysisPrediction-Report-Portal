package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OldClient reads through to the old portal: authentication + fetching metadata (for syncing) + fetching content (for reading, cached). Read-only.
type OldClient struct {
	base, user, pw string
	hc             *http.Client
	mu             sync.Mutex
	token          string
	tokenExp       time.Time
	detail         map[int64]detailEntry
	dmu            sync.Mutex
}

type detailEntry struct {
	v   OldDetail
	exp time.Time
}

// OldDetail is the response from the old /api/reports/{id}.
type OldDetail struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Category    string `json:"category"`
	Author      string `json:"author"`
	Time        string `json:"time"`
	ReportDate  string `json:"reportDate"`
	StockCode   string `json:"stockCode"`
	Content     string `json:"content"`     // markdown
	ContentHTML string `json:"contentHtml"` // rendered HTML
}

func NewOldClient(base, user, pw string) *OldClient {
	return &OldClient{
		base: strings.TrimRight(base, "/"), user: user, pw: pw,
		hc:     &http.Client{Timeout: 30 * time.Second},
		detail: map[int64]detailEntry{},
	}
}

// SetCreds updates the old portal's URL/username/password at runtime (called after system settings change), and clears the cached token.
func (c *OldClient) SetCreds(base, user, pw string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.base = strings.TrimRight(base, "/")
	c.user = user
	c.pw = pw
	c.token = ""
	c.tokenExp = time.Time{}
}

func (c *OldClient) getToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExp) {
		return c.token, nil
	}
	form := url.Values{"username": {c.user}, "password": {c.pw}}
	resp, err := c.hc.PostForm(c.base+"/auth/token", form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("auth/token: %s", resp.Status)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	c.token = out.AccessToken
	c.tokenExp = time.Now().Add(50 * time.Minute)
	return c.token, nil
}

func (c *OldClient) authGet(path string, q url.Values) ([]byte, error) {
	tok, err := c.getToken()
	if err != nil {
		return nil, err
	}
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 8192)
	for {
		n, e := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if e != nil {
			break
		}
	}
	return buf, nil
}

type oldListResp struct {
	Reports []OldRaw `json:"reports"`
	Total   int      `json:"total"`
}

// listPage fetches one page of metadata (for syncing).
func (c *OldClient) listPage(page, pageSize int) (oldListResp, error) {
	q := url.Values{
		"page":      {fmt.Sprint(page)},
		"page_size": {fmt.Sprint(pageSize)},
		"order":     {"date_desc"},
	}
	b, err := c.authGet("/api/reports", q)
	if err != nil {
		return oldListResp{}, err
	}
	var r oldListResp
	return r, json.Unmarshal(b, &r)
}

// ListAllMeta paginates through all report metadata (no DB write). Used by the
// one-shot legacy import to enumerate the full history.
func (c *OldClient) ListAllMeta() ([]OldRaw, error) {
	var out []OldRaw
	page, total := 1, -1
	for {
		r, err := c.listPage(page, 100)
		if err != nil {
			return out, err
		}
		if len(r.Reports) == 0 {
			break
		}
		out = append(out, r.Reports...)
		total = r.Total
		if total >= 0 && len(out) >= total {
			break
		}
		page++
	}
	return out, nil
}

// Detail fetches a single report's content (cached for 30 minutes).
func (c *OldClient) Detail(id int64) (OldDetail, error) {
	c.dmu.Lock()
	if e, ok := c.detail[id]; ok && time.Now().Before(e.exp) {
		c.dmu.Unlock()
		return e.v, nil
	}
	c.dmu.Unlock()
	b, err := c.authGet(fmt.Sprintf("/api/reports/%d", id), nil)
	if err != nil {
		return OldDetail{}, err
	}
	var d OldDetail
	if err := json.Unmarshal(b, &d); err != nil {
		return OldDetail{}, err
	}
	c.dmu.Lock()
	c.detail[id] = detailEntry{v: d, exp: time.Now().Add(30 * time.Minute)}
	c.dmu.Unlock()
	return d, nil
}
