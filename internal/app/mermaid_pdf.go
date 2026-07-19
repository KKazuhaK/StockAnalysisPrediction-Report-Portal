package app

import (
	"container/list"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

const (
	mermaidRendererVersion = "11.16.0"
	maxMermaidSourceBytes  = 50_000
	maxMermaidSVGBytes     = 1 << 20
	maxMermaidCacheBytes   = 32 << 20
	maxMermaidCacheEntries = 512
)

type mermaidCacheEntry struct {
	key   string
	svg   string
	theme string
	size  int
}

type mermaidChartCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     list.List
	bytes   int
}

func mermaidCacheKey(user, source, theme string) string {
	sum := sha256.Sum256([]byte(user + "\x00" + mermaidRendererVersion + "\x00" + theme + "\x00" + source))
	return hex.EncodeToString(sum[:])
}

func (c *mermaidChartCache) put(key, svg, theme string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*list.Element)
	}
	if old := c.entries[key]; old != nil {
		entry := old.Value.(*mermaidCacheEntry)
		c.bytes -= entry.size
		entry.svg = svg
		entry.theme = theme
		entry.size = len(svg)
		c.bytes += entry.size
		c.lru.MoveToFront(old)
	} else {
		entry := &mermaidCacheEntry{key: key, svg: svg, theme: theme, size: len(svg)}
		element := c.lru.PushFront(entry)
		c.entries[key] = element
		c.bytes += entry.size
	}
	for len(c.entries) > maxMermaidCacheEntries || c.bytes > maxMermaidCacheBytes {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		entry := oldest.Value.(*mermaidCacheEntry)
		delete(c.entries, entry.key)
		c.bytes -= entry.size
		c.lru.Remove(oldest)
	}
}

func (c *mermaidChartCache) get(key string) (mermaidCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element := c.entries[key]
	if element == nil {
		return mermaidCacheEntry{}, false
	}
	c.lru.MoveToFront(element)
	return *element.Value.(*mermaidCacheEntry), true
}

func (s *Server) putMermaidChart(user, source, theme, rawSVG string) error {
	if user == "" || len(source) == 0 || len(source) > maxMermaidSourceBytes {
		return errors.New("invalid Mermaid chart source")
	}
	if theme != "light" && theme != "dark" {
		return errors.New("invalid Mermaid chart theme")
	}
	if len(rawSVG) == 0 || len(rawSVG) > maxMermaidSVGBytes {
		return errors.New("invalid Mermaid SVG size")
	}
	svg, err := sanitizeMermaidSVG(rawSVG)
	if err != nil {
		return err
	}
	s.mermaidCharts.put(mermaidCacheKey(user, source, theme), svg, theme)
	return nil
}

func (s *Server) getMermaidChart(user, source string) (mermaidCacheEntry, bool) {
	for _, theme := range []string{"light", "dark"} {
		if entry, ok := s.mermaidCharts.get(mermaidCacheKey(user, source, theme)); ok {
			return entry, true
		}
	}
	return mermaidCacheEntry{}, false
}

type mermaidCacheRequest struct {
	Source  string `json:"source"`
	SVG     string `json:"svg"`
	Theme   string `json:"theme"`
	Version string `json:"version"`
}

func (s *Server) apiMermaidCache(w http.ResponseWriter, r *http.Request, user string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMermaidSVGBytes+maxMermaidSourceBytes+(64<<10))
	var req mermaidCacheRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid chart payload")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		jsonError(w, http.StatusBadRequest, "invalid chart payload")
		return
	}
	if req.Version != mermaidRendererVersion {
		jsonError(w, http.StatusConflict, "Mermaid renderer version mismatch")
		return
	}
	if err := s.putMermaidChart(user, req.Source, req.Theme, req.SVG); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid chart SVG")
		return
	}
	writeJSON(w, okJSON)
}

var svgIDPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]{0,127}$`)

var allowedSVGElements = map[string]bool{
	"svg": true, "g": true,
	"path": true, "rect": true, "circle": true, "ellipse": true, "line": true,
	"polyline": true, "polygon": true, "text": true, "tspan": true, "title": true, "desc": true,
}

var allowedSVGAttrs = map[string]bool{
	"id": true, "viewBox": true, "width": true, "height": true, "preserveAspectRatio": true,
	"x": true, "y": true, "x1": true, "y1": true, "x2": true, "y2": true,
	"cx": true, "cy": true, "r": true, "rx": true, "ry": true, "dx": true, "dy": true,
	"d": true, "points": true, "pathLength": true, "transform": true,
	"fill": true, "fill-opacity": true, "fill-rule": true,
	"stroke": true, "stroke-width": true, "stroke-opacity": true, "stroke-linecap": true,
	"stroke-linejoin": true, "stroke-miterlimit": true, "stroke-dasharray": true, "stroke-dashoffset": true,
	"opacity": true, "color": true, "vector-effect": true, "paint-order": true,
	"font-family": true, "font-size": true, "font-weight": true, "font-style": true,
	"text-anchor": true, "dominant-baseline": true, "alignment-baseline": true,
}

func safeSVGAttr(name, value string) error {
	if len(value) > 64<<10 || strings.ContainsAny(value, "\x00\r\n<>") {
		return errors.New("invalid SVG attribute value")
	}
	if name == "id" {
		if !svgIDPattern.MatchString(value) {
			return errors.New("invalid SVG id")
		}
		return nil
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "url(") || strings.Contains(lower, "javascript:") || strings.Contains(lower, "data:") {
		return errors.New("executable SVG attribute")
	}
	return nil
}

func sanitizeMermaidSVG(raw string) (string, error) {
	if len(raw) == 0 || len(raw) > maxMermaidSVGBytes {
		return "", errors.New("invalid SVG size")
	}
	decoder := xml.NewDecoder(strings.NewReader(raw))
	decoder.Strict = true
	var out strings.Builder
	encoder := xml.NewEncoder(&out)
	stack := make([]string, 0, 16)
	ids := make(map[string]bool)
	nodes := 0
	rootSeen := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", errors.New("invalid SVG XML")
		}
		switch value := token.(type) {
		case xml.StartElement:
			nodes++
			if nodes > 20_000 || len(stack) >= 128 || !allowedSVGElements[value.Name.Local] {
				return "", errors.New("denied SVG element")
			}
			if len(stack) == 0 {
				if rootSeen || value.Name.Local != "svg" {
					return "", errors.New("invalid SVG root")
				}
				rootSeen = true
			}
			if value.Name.Space != "" && value.Name.Space != "http://www.w3.org/2000/svg" {
				return "", errors.New("invalid SVG namespace")
			}
			clean := xml.StartElement{Name: xml.Name{Local: value.Name.Local}}
			if len(stack) == 0 {
				clean.Attr = append(clean.Attr, xml.Attr{Name: xml.Name{Local: "xmlns"}, Value: "http://www.w3.org/2000/svg"})
			}
			for _, attr := range value.Attr {
				name := attr.Name.Local
				if name == "xmlns" && len(stack) == 0 {
					continue
				}
				if attr.Name.Space != "" || !allowedSVGAttrs[name] {
					return "", errors.New("denied SVG attribute")
				}
				if err := safeSVGAttr(name, attr.Value); err != nil {
					return "", err
				}
				if name == "id" {
					if ids[attr.Value] {
						return "", errors.New("duplicate SVG id")
					}
					ids[attr.Value] = true
				}
				clean.Attr = append(clean.Attr, xml.Attr{Name: xml.Name{Local: name}, Value: attr.Value})
			}
			if err := encoder.EncodeToken(clean); err != nil {
				return "", err
			}
			stack = append(stack, value.Name.Local)
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1] != value.Name.Local {
				return "", errors.New("invalid SVG nesting")
			}
			stack = stack[:len(stack)-1]
			if err := encoder.EncodeToken(xml.EndElement{Name: xml.Name{Local: value.Name.Local}}); err != nil {
				return "", err
			}
		case xml.CharData:
			if len(stack) == 0 {
				if strings.TrimSpace(string(value)) != "" {
					return "", errors.New("text outside SVG root")
				}
				continue
			}
			current := stack[len(stack)-1]
			if strings.TrimSpace(string(value)) != "" && current != "text" && current != "tspan" && current != "title" && current != "desc" {
				return "", errors.New("text in denied SVG context")
			}
			if err := encoder.EncodeToken(value); err != nil {
				return "", err
			}
		case xml.Comment:
			// Comments carry no geometry and are intentionally omitted.
		default:
			return "", errors.New("denied SVG token")
		}
	}
	if !rootSeen || len(stack) != 0 {
		return "", errors.New("incomplete SVG")
	}
	if err := encoder.Flush(); err != nil {
		return "", err
	}
	if out.Len() > maxMermaidSVGBytes {
		return "", errors.New("sanitized SVG too large")
	}
	return out.String(), nil
}

type mermaidPDFSlot struct {
	token  string
	source string
}

var mermaidFenceOpen = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})[ \\t]*[Mm][Ee][Rr][Mm][Aa][Ii][Dd](?:[ \\t].*)?$")

func mermaidPDFSlots(md, nonce string) (string, []mermaidPDFSlot) {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	slots := make([]mermaidPDFSlot, 0, 2)
	for index := 0; index < len(lines); index++ {
		match := mermaidFenceOpen.FindStringSubmatch(lines[index])
		if match == nil {
			out = append(out, lines[index])
			continue
		}
		marker := match[1]
		closePattern := regexp.MustCompile(fmt.Sprintf(`^ {0,3}%c{%d,}[ \t]*$`, marker[0], len(marker)))
		end := index + 1
		for end < len(lines) && !closePattern.MatchString(lines[end]) {
			end++
		}
		if end == len(lines) {
			out = append(out, lines[index])
			continue
		}
		source := strings.Join(lines[index+1:end], "\n")
		token := fmt.Sprintf("RP_MERMAID_%s_%d", nonce, len(slots))
		out = append(out, "", token, "")
		slots = append(slots, mermaidPDFSlot{token: token, source: source})
		index = end
	}
	return strings.Join(out, "\n"), slots
}

func randomMermaidNonce() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return hex.EncodeToString([]byte(fmt.Sprintf("%p", &buf)))
	}
	return hex.EncodeToString(buf)
}

func (s *Server) renderPDFMarkdown(user, md string) string {
	rewritten, slots := mermaidPDFSlots(md, randomMermaidNonce())
	body := sanitizePDFBody(mdToHTML(rewritten))
	for _, slot := range slots {
		replacement := `<div class="mermaid-pdf-missing"><strong>Mermaid chart was not cached for this PDF.</strong><pre><code>` +
			template.HTMLEscapeString(slot.source) + `</code></pre></div>`
		if entry, ok := s.getMermaidChart(user, slot.source); ok {
			className := "mermaid-pdf"
			if entry.theme == "dark" {
				className += " mermaid-pdf-dark"
			}
			replacement = `<div class="` + className + `">` + entry.svg + `</div>`
		}
		body = strings.Replace(body, "<p>"+slot.token+"</p>", replacement, 1)
	}
	return body
}
