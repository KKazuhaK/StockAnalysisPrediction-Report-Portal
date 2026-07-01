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
			log.Printf("stock-name auto-fetch skipped (%v, %d so far); run `report-portal fetchnames` later", err, len(m))
			return
		}
		n.merge(m)
		_ = n.save(m)
		log.Printf("stock names fetched: %d", n.count())
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
// 注意：用 push2.eastmoney.com（82.push2 子域在部分网络被墙）；diff 兼容对象/数组两种格式；每页失败重试并轮换主机。
func FetchAShareNames() (map[string]string, error) {
	const fs = "m:0+t:6,m:0+t:80,m:1+t:2,m:1+t:23,m:0+t:81+s:2048"
	hosts := []string{"push2.eastmoney.com", "push2delay.eastmoney.com", "82.push2.eastmoney.com"}
	hc := &http.Client{Timeout: 25 * time.Second}
	m := map[string]string{}
	for pn := 1; pn <= 80; pn++ {
		ok, got := false, 0
		for attempt := 0; attempt < 4; attempt++ {
			host := hosts[attempt%len(hosts)]
			u := fmt.Sprintf("https://%s/api/qt/clist/get?pn=%d&pz=100&po=1&np=1"+
				"&fltt=2&invt=2&fid=f12&fs=%s&fields=f12,f14", host, pn, fs)
			req, _ := http.NewRequest("GET", u, nil)
			req.Header.Set("User-Agent", "Mozilla/5.0")
			req.Header.Set("Referer", "https://quote.eastmoney.com/")
			resp, err := hc.Do(req)
			if err != nil {
				time.Sleep(time.Duration(attempt+1) * 800 * time.Millisecond)
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			ok, got = true, mergeDiff(body, m)
			break
		}
		if !ok || got == 0 { // 连续失败，或已到末页
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	if len(m) == 0 {
		return m, fmt.Errorf("no stock names fetched (eastmoney may be unreachable)")
	}
	return m, nil
}

// mergeDiff 解析 clist 响应的 data.diff（对象 {"0":{…}} 或数组 [{…}] 都支持），并入 m，返回解析到的条数。
func mergeDiff(body []byte, m map[string]string) int {
	var raw struct {
		Data *struct {
			Diff json.RawMessage `json:"diff"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &raw) != nil || raw.Data == nil || len(raw.Data.Diff) == 0 {
		return 0
	}
	var items []map[string]any
	if json.Unmarshal(raw.Data.Diff, &items) != nil {
		var obj map[string]map[string]any
		if json.Unmarshal(raw.Data.Diff, &obj) != nil {
			return 0
		}
		for _, v := range obj {
			items = append(items, v)
		}
	}
	n := 0
	for _, d := range items {
		code, _ := d["f12"].(string)
		name, _ := d["f14"].(string)
		if code != "" && name != "" {
			m[code] = name
			n++
		}
	}
	return n
}
