package app

import (
	"net/http"
	"sort"
	"strconv"
	"time"
)

// Chat concurrency gate + admin observability (docs/adr/0012-interactive-chat.md).
//
// A chat turn is NOT scheduled through the batch run queue. That queue exists to *defer* slow,
// expensive report runs and hand them out by priority — deferral is exactly wrong for an
// interactive turn, where the user is waiting for the reply and must not sit behind a batch of
// deep-research runs (head-of-line blocking). Chat also hits a different Dify surface with its
// own rate profile, so coupling the two would create false contention.
//
// Instead chat gets its own lightweight ceiling: a plain count of in-flight turns that *sheds*
// load (rejects with 429) when full, so a burst can't overwhelm Dify — no queuing, no waiting.
// The same in-memory registry powers the admin "who is chatting right now" view.

// chatTurn is one in-flight chat turn, tracked for the ceiling and the admin live view.
type chatTurn struct {
	ID         int64     // monotonic in-process id (not persisted)
	User       string    // who submitted the turn
	TargetID   int64     // Dify chat/agent target
	TargetName string    // its display name
	ConvID     int64     // portal conversation row id
	ConvTitle  string    // conversation title (may be empty on the first turn)
	Started    time.Time // when the turn began
}

// chatMaxConcurrent is the ceiling on simultaneous in-flight chat turns; 0 = unlimited.
// Independent of the run queue's batch budget. Admin-set; the default is 0 so an upgrade never
// silently caps a running deployment — the admin opts into a number from the live view.
func (s *Server) chatMaxConcurrent() int {
	n, err := strconv.Atoi(s.st.GetSetting("chat_max_concurrent", "0"))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// chatAcquire registers an in-flight turn if the ceiling allows, returning its id. ok=false
// means the ceiling is full and the caller should shed the turn (HTTP 429) rather than queue
// it — an interactive turn never waits behind others. The registry is lazily created so tests
// that build a bare Server work without a constructor.
func (s *Server) chatAcquire(t *chatTurn) (int64, bool) {
	limit := s.chatMaxConcurrent()
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	if s.chatLive == nil {
		s.chatLive = map[int64]*chatTurn{}
	}
	if limit > 0 && len(s.chatLive) >= limit {
		return 0, false
	}
	s.chatSeq++
	t.ID = s.chatSeq
	s.chatLive[t.ID] = t
	return t.ID, true
}

// chatRelease frees a turn's slot (deferred at the end of a turn, success or failure).
func (s *Server) chatRelease(id int64) {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	delete(s.chatLive, id)
}

// chatLiveTurns snapshots the in-flight turns, oldest first.
func (s *Server) chatLiveTurns() []*chatTurn {
	s.chatMu.Lock()
	turns := make([]*chatTurn, 0, len(s.chatLive))
	for _, t := range s.chatLive {
		turns = append(turns, t)
	}
	s.chatMu.Unlock()
	sort.Slice(turns, func(i, j int) bool { return turns[i].Started.Before(turns[j].Started) })
	return turns
}

// apiAdminChatLive returns the chat turns currently in flight plus the configured ceiling —
// the "what's running now" view for the assistant, mirroring the run queue's live summary.
func (s *Server) apiAdminChatLive(w http.ResponseWriter, r *http.Request, user string) {
	turns := s.chatLiveTurns()
	out := make([]map[string]any, 0, len(turns))
	for _, t := range turns {
		out = append(out, map[string]any{
			"id": t.ID, "user": t.User, "target_id": t.TargetID, "target": t.TargetName,
			"conv_id": t.ConvID, "title": t.ConvTitle, "started_at": t.Started.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, map[string]any{"turns": out, "max_concurrent": s.chatMaxConcurrent()})
}

// apiAdminChatConfigSave sets the chat concurrency ceiling (0 = unlimited).
func (s *Server) apiAdminChatConfigSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		MaxConcurrent *int `json:"max_concurrent"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	if in.MaxConcurrent != nil && *in.MaxConcurrent >= 0 {
		s.st.SetSetting("chat_max_concurrent", strconv.Itoa(*in.MaxConcurrent))
	}
	writeJSON(w, okJSON)
}
