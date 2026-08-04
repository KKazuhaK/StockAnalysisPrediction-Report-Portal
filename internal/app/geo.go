package app

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/geoip"
)

// IP → place, for the audit log.
//
// The database is a .mmdb file the operator puts in <data>/geoip/. Nothing is
// downloaded and nothing is looked up remotely: the addresses being resolved are the
// portal's own visitors, including people who failed to sign in, and handing that list
// to a third party to be geolocated is the thing this feature must not do.
//
// Exactly ONE database is active at a time, so two sources can never disagree about
// the same address. When several files are present the newest by modification time
// wins, which is what an operator means by dropping in a fresh one.
//
// Resolution happens when the log is READ, never when it is written. A database gets
// better — and an address's owner changes — so a region stored beside the row would be
// a snapshot of what one build of one database once thought, presented later as fact.
// The address is the record; the place is a rendering of it.

// geoService owns the active reader.
type geoService struct {
	dir string
	st  *Store // settings: which file is active, and whether the feature is on at all

	mu      sync.RWMutex
	reader  *geoip.Reader
	path    string
	modTime time.Time
	size    int64
}

func newGeoService(dataDir string) *geoService {
	return &geoService{dir: filepath.Join(dataDir, "geoip")}
}

// pick and enabled are read through the store on every call rather than cached, so a settings
// change takes effect on the next request and not on the next restart.
func (g *geoService) pick() string {
	if g.st == nil {
		return ""
	}
	return g.st.GetSetting(setGeoFile, "")
}

// Enabled reports whether locations should be resolved at all. Off means the log shows addresses
// and nothing else — the database stays installed, so turning it back on costs nothing.
func (g *geoService) Enabled() bool {
	if g == nil || g.st == nil {
		return true
	}
	return g.st.GetSetting(setGeoEnabled, "1") != "0"
}

// Dir is where an operator puts the .mmdb.
func (g *geoService) Dir() string { return g.dir }

// available lists every .mmdb present, with what each one is, so the admin can pick between them
// knowing which is which rather than by filename alone.
func (g *geoService) available() []geoDBEntry {
	entries, err := os.ReadDir(g.dir)
	if err != nil {
		return nil
	}
	out := make([]geoDBEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".mmdb") {
			continue
		}
		item := geoDBEntry{File: e.Name()}
		if info, err := e.Info(); err == nil {
			item.Modified = info.ModTime().UTC().Format(time.RFC3339)
		}
		// Opened just to read the metadata. A file that will not open is listed anyway, marked —
		// hiding it would leave the admin picking from a list that silently omits their file.
		if rd, err := geoip.Open(filepath.Join(g.dir, e.Name())); err == nil {
			item.Info = rd.Info()
			item.OK = true
			rd.Close()
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

// geoDBEntry is one installed database, for the picker.
type geoDBEntry struct {
	File     string       `json:"file"`
	Modified string       `json:"modified,omitempty"`
	OK       bool         `json:"ok"` // false = present but unreadable
	Info     geoip.DBInfo `json:"info"`
}

// activeFile is the database to use: the one the admin chose, or — when they have chosen nothing —
// the most recently modified, which is what dropping a fresh file in means.
func (g *geoService) activeFile() (path string, mod time.Time, size int64) {
	if pick := strings.TrimSpace(g.pick()); pick != "" {
		// Base, because the value reaches here from a setting an admin typed.
		p := filepath.Join(g.dir, filepath.Base(pick))
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, info.ModTime(), info.Size()
		}
		// A chosen file that has gone missing falls through to the automatic rule rather than
		// turning the feature off: losing locations because a filename changed is worse than
		// quietly using the other database that is sitting right there.
	}
	return g.newestFile()
}

func (g *geoService) newestFile() (path string, mod time.Time, size int64) {
	entries, err := os.ReadDir(g.dir)
	if err != nil {
		return "", time.Time{}, 0
	}
	type cand struct {
		path string
		mod  time.Time
		size int64
	}
	var found []cand
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".mmdb") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		found = append(found, cand{filepath.Join(g.dir, e.Name()), info.ModTime(), info.Size()})
	}
	if len(found) == 0 {
		return "", time.Time{}, 0
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].mod.Equal(found[j].mod) {
			return found[i].path < found[j].path // stable when two files share a timestamp
		}
		return found[i].mod.After(found[j].mod)
	})
	return found[0].path, found[0].mod, found[0].size
}

// ensure opens or re-opens the active database when the file on disk has changed, so
// replacing it takes effect without a restart — an operator who has just copied a new
// file in should not have to wonder whether it is live.
//
// Size as well as mtime: a file replaced within the same second, which a scripted
// download does, changes size far more reliably than it changes a whole-second stamp.
func (g *geoService) ensure() *geoip.Reader {
	path, mod, size := g.activeFile()

	g.mu.RLock()
	cur, curPath, curMod, curSize := g.reader, g.path, g.modTime, g.size
	g.mu.RUnlock()
	if path == curPath && mod.Equal(curMod) && size == curSize {
		return cur
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	// Re-check under the write lock: two readers can race here and only one should open.
	if path == g.path && mod.Equal(g.modTime) && size == g.size {
		return g.reader
	}
	if g.reader != nil {
		g.reader.Close()
	}
	g.reader, g.path, g.modTime, g.size = nil, path, mod, size
	if path == "" {
		return nil
	}
	rd, err := geoip.Open(path)
	if err != nil {
		// Left nil, so lookups answer "unknown" rather than failing the page. A broken
		// or half-copied file is the common case and it fixes itself on the next write.
		log.Printf("geoip: cannot open %s: %v", filepath.Base(path), err)
		return nil
	}
	g.reader = rd
	log.Printf("geoip: using %s (%s)", filepath.Base(path), rd.Info().Type)
	return rd
}

// replace swaps a validated download in for the active database.
//
// The live reader is closed and the rename done under the write lock, because a memory-mapped file
// cannot be renamed over on Windows while a lookup holds it — and because clearing the cached path
// here is what makes the next lookup reopen rather than keep serving the file that no longer exists.
func (g *geoService) replace(tmp, final string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reader != nil {
		g.reader.Close()
		g.reader = nil
	}
	g.path, g.modTime, g.size = "", time.Time{}, 0
	return os.Rename(tmp, final)
}

// Lookup resolves one address, or an empty location when there is no database or the feature is off.
func (g *geoService) Lookup(ip string) geoip.Location {
	if g == nil || !g.Enabled() {
		return geoip.Location{}
	}
	return g.ensure().Lookup(ip)
}

// Status describes what is loaded, for the admin view. It reports the DIRECTORY even
// when nothing is installed, because the first question an operator has is where to
// put the file.
type geoStatus struct {
	Enabled  bool         `json:"enabled"`
	Pick     string       `json:"pick"` // the admin's choice; "" = automatic
	Files    []geoDBEntry `json:"files"`
	Dir      string       `json:"dir"`
	File     string       `json:"file,omitempty"`
	Loaded   bool         `json:"loaded"`
	Info     geoip.DBInfo `json:"info"`
	Modified string       `json:"modified,omitempty"`
}

func (g *geoService) Status() geoStatus {
	if g == nil {
		return geoStatus{}
	}
	rd := g.ensure()
	g.mu.RLock()
	defer g.mu.RUnlock()
	st := geoStatus{Dir: g.dir, Loaded: rd != nil, Enabled: g.Enabled(), Pick: g.pick()}
	if g.path != "" {
		st.File = filepath.Base(g.path)
		st.Modified = g.modTime.UTC().Format(time.RFC3339)
	}
	if rd != nil {
		st.Info = rd.Info()
	}
	g.mu.RUnlock()
	st.Files = g.available() // opens files, so not under the lock
	g.mu.RLock()
	return st
}
