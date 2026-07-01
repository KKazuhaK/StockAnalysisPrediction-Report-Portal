package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

//go:embed names.json
var embeddedNames []byte

// Names 股票代码→名称。内嵌种子(常见/演示标的) + 运行时全量表(data/names.json)。
type Names struct {
	mu  sync.RWMutex
	m   map[string]string
	dir string
}

func LoadNames(dataDir string) *Names {
	n := &Names{m: map[string]string{}, dir: dataDir}
	var seed map[string]string
	_ = json.Unmarshal(embeddedNames, &seed)
	for k, v := range seed {
		n.m[k] = v
	}
	if b, err := os.ReadFile(filepath.Join(dataDir, "names.json")); err == nil {
		var ext map[string]string
		if json.Unmarshal(b, &ext) == nil {
			for k, v := range ext {
				n.m[k] = v
			}
		}
	}
	return n
}

func (n *Names) Get(code string) string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.m[code]
}

func (n *Names) count() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return len(n.m)
}

func (n *Names) merge(ext map[string]string) {
	n.mu.Lock()
	for k, v := range ext {
		n.m[k] = v
	}
	n.mu.Unlock()
}

// ensureFull 若本地没有全量表，后台 best-effort 抓一次(生产机网络好时自动补全)。
func (n *Names) ensureFull() {
	if _, err := os.Stat(filepath.Join(n.dir, "names.json")); err == nil {
		return // 已有全量表
	}
	go func() {
		m, err := FetchAShareNames()
		if err != nil || len(m) < 3000 {
			log.Printf("股票名称自动抓取跳过(%v, %d 条)——可稍后手动 `report-portal fetchnames`", err, len(m))
			return
		}
		n.merge(m)
		_ = n.save(m)
		log.Printf("股票名称已自动补全: %d 条", n.count())
	}()
}

func (n *Names) save(m map[string]string) error {
	b, _ := json.Marshal(m)
	return os.WriteFile(filepath.Join(n.dir, "names.json"), b, 0o644)
}

// FetchNamesToFile 抓全量并写入 <dir>/names.json（fetchnames 子命令用）。
func FetchNamesToFile(dir string) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	m, err := FetchAShareNames()
	if err != nil && len(m) == 0 {
		return 0, err
	}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "names.json"), b, 0o644); err != nil {
		return len(m), err
	}
	return len(m), nil
}

// FetchAShareNames 从 eastmoney 分页抓取全部 A 股 代码→名称。
func FetchAShareNames() (map[string]string, error) {
	const fs = "m:0+t:6,m:0+t:80,m:1+t:2,m:1+t:23,m:0+t:81+s:2048"
	hc := &http.Client{Timeout: 25 * time.Second}
	m := map[string]string{}
	for pn := 1; pn <= 80; pn++ {
		u := fmt.Sprintf("https://82.push2.eastmoney.com/api/qt/clist/get?pn=%d&pz=100&po=1&np=1"+
			"&fltt=2&invt=2&fid=f12&fs=%s&fields=f12,f14", pn, fs)
		req, _ := http.NewRequest("GET", u, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0")
		req.Header.Set("Referer", "https://quote.eastmoney.com/")
		resp, err := hc.Do(req)
		if err != nil {
			return m, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var raw struct {
			Data *struct {
				Diff []map[string]any `json:"diff"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &raw) != nil || raw.Data == nil || len(raw.Data.Diff) == 0 {
			break
		}
		for _, d := range raw.Data.Diff {
			code, _ := d["f12"].(string)
			name, _ := d["f14"].(string)
			if code != "" && name != "" {
				m[code] = name
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return m, nil
}
