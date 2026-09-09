package app

import "time"

// quoteUnknownRefresh bounds both the cache and the visible-page retry for a response whose source
// cannot report market state. It is deliberately slower than the shipped open-market TTL while
// remaining short enough for the batch endpoint, which always reports unknown, to move on screen.
const quoteUnknownRefresh = 2 * time.Minute

const quoteOpeningGrace = 5 * time.Minute

// quoteRefreshAdvice is scheduling metadata for a visible client. Exactly one field is populated:
// a relative delay while repeated refresh is useful, or an absolute market-zone instant after the
// client has been told to sleep.
type quoteRefreshAdvice struct {
	RefreshAfterSecs int    `json:"refreshAfterSecs,omitempty"`
	RefreshAt        string `json:"refreshAt,omitempty"`
}

type quoteSessionSpan struct {
	startMinute int
	endMinute   int
}

func quoteRegularSpans(market string) []quoteSessionSpan {
	switch market {
	case "sh", "sz", "bj":
		return []quoteSessionSpan{{9*60 + 30, 11*60 + 30}, {13 * 60, 15 * 60}}
	case "hk":
		return []quoteSessionSpan{{9*60 + 30, 12 * 60}, {13 * 60, 16 * 60}}
	case "us":
		return []quoteSessionSpan{{9*60 + 30, 16 * 60}}
	default:
		return nil
	}
}

func quoteSessionBoundary(day time.Time, minute int) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), minute/60, minute%60, 0, 0, day.Location())
}

func quoteWeekday(day time.Time) bool {
	return day.Weekday() != time.Saturday && day.Weekday() != time.Sunday
}

// quoteSessionPosition returns the current regular span, when any, and the next candidate opening.
// It is not an exchange calendar: holidays are learned from the response at that candidate instant.
func quoteSessionPosition(now time.Time, market *quoteMarket) (start, end, next time.Time, inside bool) {
	if market == nil {
		return
	}
	loc, err := quoteZone(market.zone)
	if err != nil {
		return
	}
	local := now.In(loc)
	spans := quoteRegularSpans(market.id)
	if len(spans) == 0 {
		return
	}
	if quoteWeekday(local) {
		for _, span := range spans {
			s := quoteSessionBoundary(local, span.startMinute)
			e := quoteSessionBoundary(local, span.endMinute)
			if !local.Before(s) && local.Before(e) {
				start, end, inside = s, e, true
			}
			if next.IsZero() && local.Before(s) {
				next = s
			}
		}
	}
	if !next.IsZero() {
		return
	}
	for n := 1; n <= 7; n++ {
		day := local.AddDate(0, 0, n)
		if quoteWeekday(day) {
			next = quoteSessionBoundary(day, spans[0].startMinute)
			return
		}
	}
	return
}

func quoteRefreshSeconds(d time.Duration) int {
	if d <= 0 {
		return 1
	}
	return int((d + time.Second - 1) / time.Second)
}

func quoteMinDuration(a, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if b <= 0 || a < b {
		return a
	}
	return b
}

func quoteAdviceAt(at time.Time) quoteRefreshAdvice {
	if at.IsZero() {
		return quoteRefreshAdvice{}
	}
	return quoteRefreshAdvice{RefreshAt: at.Format(time.RFC3339)}
}

// quoteRefreshAdviceFor is the one policy shared by the single and batch endpoints. The vendor is
// authoritative when it says open. Regular hours are used only for uncertain or sleeping answers.
func quoteRefreshAdviceFor(now time.Time, enabled bool, cfg quoteConfig, target quoteTarget, iv quoteInterval, ttl time.Duration, resp *QuoteResp) quoteRefreshAdvice {
	if !enabled || target.Market == nil {
		return quoteRefreshAdvice{}
	}
	cfg = cfg.orDefaults()
	if resp != nil && resp.Snapshot.Session == quoteSessionOpen {
		if ttl <= 0 {
			ttl = cfg.ttlFor(resp, iv)
		}
		return quoteRefreshAdvice{RefreshAfterSecs: quoteRefreshSeconds(ttl)}
	}

	start, end, next, inside := quoteSessionPosition(now, target.Market)
	if resp != nil && resp.Snapshot.Session == quoteSessionClose {
		if inside && now.Before(start.Add(quoteOpeningGrace)) {
			return quoteAdviceAt(start.Add(quoteOpeningGrace))
		}
		return quoteAdviceAt(next)
	}

	// A total batch/source failure has no vendor timestamp to judge. Retry conservatively during a
	// candidate session so a temporary outage cannot disable the page until lunch or tomorrow.
	if resp == nil {
		if inside {
			return quoteRefreshAdvice{RefreshAfterSecs: quoteRefreshSeconds(quoteUnknownRefresh)}
		}
		return quoteAdviceAt(next)
	}

	if !inside {
		return quoteAdviceAt(next)
	}
	asOf, err := time.Parse(time.RFC3339, resp.Snapshot.AsOf)
	if err == nil && !asOf.Before(start) && asOf.Before(end.Add(time.Second)) && !asOf.After(now.Add(5*time.Minute)) {
		delay := quoteMinDuration(ttl, quoteUnknownRefresh)
		if delay <= 0 {
			delay = quoteUnknownRefresh
		}
		return quoteRefreshAdvice{RefreshAfterSecs: quoteRefreshSeconds(delay)}
	}
	if now.Before(start.Add(quoteOpeningGrace)) {
		return quoteAdviceAt(start.Add(quoteOpeningGrace))
	}
	return quoteAdviceAt(next)
}

func quoteAdviceDue(now time.Time, advice quoteRefreshAdvice) (time.Time, bool) {
	if advice.RefreshAfterSecs > 0 {
		return now.Add(time.Duration(advice.RefreshAfterSecs) * time.Second), true
	}
	if advice.RefreshAt != "" {
		at, err := time.Parse(time.RFC3339, advice.RefreshAt)
		return at, err == nil
	}
	return time.Time{}, false
}

func quoteAggregateAdvice(now time.Time, advices []quoteRefreshAdvice) quoteRefreshAdvice {
	var out quoteRefreshAdvice
	var earliest time.Time
	for _, advice := range advices {
		due, ok := quoteAdviceDue(now, advice)
		if !ok || (!earliest.IsZero() && !due.Before(earliest)) {
			continue
		}
		earliest, out = due, advice
	}
	return out
}

func quoteAdviceMap(out map[string]any, advice quoteRefreshAdvice) {
	if advice.RefreshAfterSecs > 0 {
		out["refreshAfterSecs"] = advice.RefreshAfterSecs
	}
	if advice.RefreshAt != "" {
		out["refreshAt"] = advice.RefreshAt
	}
}
