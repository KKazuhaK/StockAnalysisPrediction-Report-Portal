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

	mu      sync.RWMutex
	reader  *geoip.Reader
	path    string
	modTime time.Time
	size    int64
}

func newGeoService(dataDir string) *geoService {
	return &geoService{dir: filepath.Join(dataDir, "geoip")}
}

// Dir is where an operator puts the .mmdb.
func (g *geoService) Dir() string { return g.dir }

// activeFile is the database to use: the most recently modified .mmdb in the dir.
func (g *geoService) activeFile() (path string, mod time.Time, size int64) {
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

// Lookup resolves one address, or an empty location when there is no database.
func (g *geoService) Lookup(ip string) geoip.Location {
	if g == nil {
		return geoip.Location{}
	}
	return g.ensure().Lookup(ip)
}

// Status describes what is loaded, for the admin view. It reports the DIRECTORY even
// when nothing is installed, because the first question an operator has is where to
// put the file.
type geoStatus struct {
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
	st := geoStatus{Dir: g.dir, Loaded: rd != nil}
	if g.path != "" {
		st.File = filepath.Base(g.path)
		st.Modified = g.modTime.UTC().Format(time.RFC3339)
	}
	if rd != nil {
		st.Info = rd.Info()
	}
	return st
}
