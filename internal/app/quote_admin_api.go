package app

// quote_admin_api.go —— 管理 → 行情: what an operator can see and change about the quote sources.
//
// ADR 0028 §9 argued for NO setting at all, and that was right when there was one thing to say about
// quotes ("they are on"). It is not right now that there are two sources and five markets: an
// operator watching a vendor misbehave has no way to take it out of the rotation, and the health
// counters quote_cache.go has always kept were readable from nowhere. This file is the panel those
// counters were written for.
//
// What stays fixed is the part §9 was actually protecting: THE URLS ARE NOT EDITABLE. A source here
// is not a URL, it is a PARSER — positional index reads over a body with no schema, guarded by the
// drift gate — so pointing one at another host produces a gate failure, not another source. An
// editable host would also drag in the whole SSRF apparatus (checkURL at save AND the guarded
// transport at request time), for a field that cannot work. Choosing among compiled-in sources is
// safe and useful; typing a host is neither.
//
// Refusals here go through jsonError rather than jsonErrorCode on purpose: a code is a contract the
// SPA translates, and every one of them needs an err.<code> string in all three locale bundles
// (wired_settings_test.go checks). This change does not own those bundles, so it does not invent
// codes for them; the admin form shows the server's message, which is what the other admin saves
// with typed-in fields already do.

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// quoteSourceStatus is one row of the panel: who the source is, whether the order has it switched on
// and where, which markets its parser covers, and its recent record. Consecutive failures is a
// counter rather than a boolean because the question is "down, or blipped".
type quoteSourceStatus struct {
	// Source, not Name: QuoteSourceHealth already calls this field `source` on the wire, and one
	// vendor answering to two key names across two payloads is how a panel ends up joining them
	// wrongly.
	Source   string   `json:"source"`
	Enabled  bool     `json:"enabled"`
	Position int      `json:"position"` // 1-based place in the failover order; 0 when disabled
	Markets  []string `json:"markets"`
	// What the source DECLARES it can serve, split by interval. Markets above is the union over all
	// of them and is therefore a wider claim than either of these: Tencent names the US there for a
	// price it serves correctly, and must not appear in Daily for a series it answers with two rows
	// fifteen years apart. An operator dragging a source up the order is choosing among THESE, so a
	// panel that showed only the union would make that move look like something it is not.
	//
	// All three are always arrays, never null: the page renders an empty one as a dash, which is a
	// real answer ("serves no intraday") and must not be confusable with a field the server did not
	// send.
	//
	// The two intraday windows are separate fields because they are separate claims. They were one
	// union under a 分时 heading, and that is exactly how the panel came to advertise a window
	// nothing served: Tencent declared the one-session interval, the column printed its three
	// markets, and an operator read "5日 works here" while every 5日 request degraded to a snapshot.
	Daily      []string `json:"daily"`
	Intraday   []string `json:"intraday"`
	Intraday5D []string `json:"intraday5d"`
	// The three timestamps are RFC3339 in UTC, and EMPTY when the thing has never happened — not
	// the year 1 that a zero time.Time marshals to, which a panel would render as "0001-01-01" and
	// an operator would read as a real event from a broken clock.
	LastSuccess string `json:"lastSuccess"`
	LastError   string `json:"lastError"`
	LastErrorAt string `json:"lastErrorAt"`
	Failures    int    `json:"consecutiveFailures"`
}

// quoteAdminTime formats an instant the way the audit log does — UTC RFC3339, rendered by the client
// in the panel timezone — and an unset one as "".
func quoteAdminTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// quoteSourceStatuses lays the operator's order and the live counters over the sources this build
// actually holds. Every compiled-in source appears, including a disabled one: a panel that listed
// only the enabled sources would give an operator who has just switched Sina off no way to switch it
// back on.
func (s *Server) quoteSourceStatuses(cfg quoteConfig) []quoteSourceStatus {
	position := map[string]int{}
	for i, name := range cfg.Order {
		position[name] = i + 1
	}
	health := map[string]QuoteSourceHealth{}
	for _, h := range s.QuoteHealth() {
		health[h.Source] = h
	}
	srcs := s.quotes.describeSources()
	out := make([]quoteSourceStatus, 0, len(srcs))
	for _, src := range srcs {
		h := health[src.Name]
		out = append(out, quoteSourceStatus{
			Source:      src.Name,
			Enabled:     position[src.Name] > 0,
			Position:    position[src.Name],
			Markets:     src.Markets,
			Daily:       src.Daily,
			Intraday:    src.Intraday,
			Intraday5D:  src.Intraday5D,
			LastSuccess: quoteAdminTime(h.LastSuccess),
			LastError:   h.LastError,
			LastErrorAt: quoteAdminTime(h.LastErrorAt),
			Failures:    h.Failures,
		})
	}
	// Failover order first, disabled sources after it, so the list reads as the chain a request
	// actually walks rather than as whatever order the source table happens to be in.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Position, out[j].Position
		if a == 0 || b == 0 {
			return b == 0 && a != 0
		}
		return a < b
	})
	return out
}

// quoteAdminState is the whole panel in one object, and ALL THREE handlers answer with it. A save
// that replied "ok" would leave the page showing the number that was typed rather than the one that
// was kept — which for a below-floor TTL is a setting that reads as accepted and was not — and a
// cache purge that replied "ok" would leave the occupancy beside the button unchanged, which is the
// only evidence the button did anything.
//
// The order is not a field of its own: it IS the sources array, whose position says where each
// vendor sits and whose enabled says whether it is in the list at all. Two representations of one
// setting is one for them to disagree over.
func (s *Server) quoteAdminState() map[string]any {
	cfg := s.quoteConfigLoad()
	stats := s.quotes.stats()
	return map[string]any{
		"sources":      s.quoteSourceStatuses(cfg),
		"cacheEntries": stats.Entries,
		"cacheBytes":   stats.Bytes,
		// Already clamped both ways by quoteConfigLoad, so a form that round-trips this back cannot
		// re-save a value the portal is not honouring.
		"ttlOpenSecs":      quoteTTLSecs(cfg.TTLOpen),
		"ttlClosedSecs":    quoteTTLSecs(cfg.TTLClosed),
		"ttlIntradaySecs":  quoteTTLSecs(cfg.TTLIntraday),
		"ttlOpenFloor":     quoteTTLSecs(quoteTTLOpenFloor),
		"ttlClosedFloor":   quoteTTLSecs(quoteTTLClosedFloor),
		"ttlIntradayFloor": quoteTTLSecs(quoteTTLIntradayFloor),
		// The home feed's switch. It is here rather than on the general settings page because it is a
		// fact about the quote vendors — the panel that says which of them this portal talks to is
		// where "and it talks to them on every home page view" belongs.
		"homeCards": s.quoteHomeCards(),
	}
}

// apiAdminQuotes is the panel's read. GET /api/admin/quote
func (s *Server) apiAdminQuotes(w http.ResponseWriter, r *http.Request, user string) {
	writeJSON(w, s.quoteAdminState())
}

// apiAdminQuotesSave writes the three settings. POST /api/admin/quote
//
// The two kinds of bad input are handled differently and deliberately. An unknown SOURCE is refused
// with nothing persisted: there is no sensible interpretation of a vendor this build has no parser
// for, and quietly dropping the name would leave an admin looking at an order they did not type. An
// OUT-OF-RANGE TTL is CLAMPED and saved: that one has an obvious interpretation — the shortest or
// longest TTL they are allowed — and refusing it would leave the previous value in place while the
// form said otherwise. The reply carries the whole panel back, so the clamped number is what the
// form ends up showing; that is the only notice an admin gets that 2000000000 became a day.
func (s *Server) apiAdminQuotesSave(w http.ResponseWriter, r *http.Request, user string) {
	// `order` carries both the sequence and the on/off flags because one SETTING does: a source
	// absent from the comma list is off. A separate `enabled` array would be a second way to say the
	// same thing, and the two would be free to disagree.
	var in struct {
		Order           *string `json:"order"`
		TTLOpenSecs     *int    `json:"ttlOpenSecs"`
		TTLClosedSecs   *int    `json:"ttlClosedSecs"`
		TTLIntradaySecs *int    `json:"ttlIntradaySecs"`
		HomeCards       *bool   `json:"homeCards"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	// Validated BEFORE anything is persisted, so a rejected order cannot leave a saved TTL behind it.
	order := ""
	if in.Order != nil {
		names, unknown := quoteParseOrder(*in.Order)
		if unknown != "" {
			jsonError(w, http.StatusBadRequest, "未知的行情源: "+quoteEchoName(unknown)+
				"（可用: "+strings.Join(quoteSourceNames(), ", ")+"）")
			return
		}
		// A blank order is a RESET to the shipped failover order, not "every source off". Undo is
		// then expressible without knowing the default by heart — the same choice the app-market
		// index URL makes — and turning quotes off entirely stays what ADR 0028 §9 says it is: a
		// real switch on a real page, which nobody has asked for and this is not.
		order = strings.Join(names, ",")
	}

	if in.Order != nil {
		s.st.SetSetting(setQuoteSourceOrder, order)
	}
	if in.TTLOpenSecs != nil {
		s.st.SetSetting(setQuoteTTLOpenSecs, strconv.Itoa(quoteClampSecs(*in.TTLOpenSecs, quoteTTLOpenFloor)))
	}
	if in.TTLClosedSecs != nil {
		s.st.SetSetting(setQuoteTTLClosedSecs, strconv.Itoa(quoteClampSecs(*in.TTLClosedSecs, quoteTTLClosedFloor)))
	}
	if in.TTLIntradaySecs != nil {
		s.st.SetSetting(setQuoteTTLIntradaySecs, strconv.Itoa(quoteClampSecs(*in.TTLIntradaySecs, quoteTTLIntradayFloor)))
	}
	if in.HomeCards != nil {
		// The writer for setQuoteHomeCards, in the same change as its reader (quoteHomeCards) and its
		// place on the panel — the rule wired_settings_test.go exists to enforce. strconv.FormatBool
		// rather than "1"/"0" so that a row an operator greps out of meta reads as what it means; the
		// reader accepts either.
		s.st.SetSetting(setQuoteHomeCards, strconv.FormatBool(*in.HomeCards))
	}

	// Which source answers a price, and for how long that answer is repeated, is a policy about what
	// the portal tells its readers — the same class of change as the retention settings next door,
	// and recorded the same way.
	s.recordChange(r, user, AuditPolicyChange, "quote_config", "",
		map[string]any{"fields": changedSettingFields(in)})

	writeJSON(w, s.quoteAdminState())
}

// apiAdminQuotesCacheClear empties the quote cache. POST /api/admin/quote/cache/clear
//
// It is the button that makes a shortened TTL take effect at once: entries already stored keep the
// expiry they were written with, so lowering quote_ttl_closed_secs from five minutes to thirty
// seconds does nothing for a symbol fetched a minute ago until it expires or this drops it.
//
// No audit row. The audit vocabulary is a closed list of decisions with durable effects (audit.go),
// and this one has none: nothing was configured, nothing was deleted that anybody can miss, and the
// next page view refills what it dropped at the cost of one vendor call.
func (s *Server) apiAdminQuotesCacheClear(w http.ResponseWriter, r *http.Request, user string) {
	s.quotes.clear()
	// The state comes back rather than an ok, so the occupancy beside the button is READ rather than
	// assumed to be zero — the cache refills the moment the next reader opens a stock page, and a
	// hard-coded 0 would be a number this endpoint made up.
	writeJSON(w, s.quoteAdminState())
}

// quoteTTLSecs renders a TTL for the wire. The panel talks in seconds because that is what the
// settings hold; nothing here should be re-deriving them from a Duration at each call site.
func quoteTTLSecs(d time.Duration) int { return int(d / time.Second) }

// quoteClampSecs is the save-side half of the clamp discipline: the submitted seconds, floored and
// capped, back as the integer that goes into meta. quoteConfigLoad applies the same bounds on the
// way out, so a value that reached the row some other way is clamped too. Both ends matter here and
// not only on read: meta is what a backup carries and what an operator greps, and a row holding a
// number the portal does not honour is a lie told to whoever reads it next.
func quoteClampSecs(secs int, floor time.Duration) int {
	return quoteTTLSecs(quoteClampTTL(time.Duration(secs)*time.Second, floor))
}

// quoteEchoName bounds a name taken from the request before it is echoed in an error message. The
// point of naming it is that an admin can see which of the two they mistyped; a megabyte of it
// coming back is not that.
func quoteEchoName(name string) string {
	const max = 24
	if r := []rune(name); len(r) > max {
		return string(r[:max]) + "…"
	}
	return name
}
