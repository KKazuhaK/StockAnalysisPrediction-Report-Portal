package app

import "database/sql"

// This file is the persistence layer for interactive chat/assistant conversations
// (docs/adr/0012-interactive-chat.md). It is deliberately a THIN index: Dify owns the
// messages and the whole conversation context (keyed by conv_id + the Dify end-user);
// the portal only records enough to list a user's conversations per target and reopen
// them. No message content is stored here.

// ChatConversation is one interactive thread with a Dify chat/agent target.
type ChatConversation struct {
	ID        int64
	TargetID  int64
	ConvID    string // Dify's conversation_id ("" until the first reply assigns one)
	CreatedBy string
	Title     string
	Starred   bool // pinned to the top of the user's list
	CreatedAt string
	UpdatedAt string
}

// CreateConversation starts a new (empty) conversation for a target, owned by user.
func (s *Store) CreateConversation(targetID int64, user string) (int64, error) {
	now := nowStr()
	return s.insertID(
		`INSERT INTO chat_conversations(target_id, conv_id, created_by, title, created_at, updated_at)
		 VALUES(?,?,?,?,?,?)`, targetID, "", user, "", now, now)
}

// ListConversations returns a user's conversations, most-recently-updated first,
// optionally scoped to one target (targetID 0 = all targets).
func (s *Store) ListConversations(user string, targetID int64) []ChatConversation {
	q := `SELECT id, target_id, COALESCE(conv_id,''), COALESCE(title,''), COALESCE(starred,0), created_at, updated_at
		FROM chat_conversations WHERE created_by=?`
	args := []any{user}
	if targetID != 0 {
		q += ` AND target_id=?`
		args = append(args, targetID)
	}
	// Starred conversations pin to the top; within each group it stays recency-ordered.
	q += ` ORDER BY starred DESC, updated_at DESC, id DESC`
	rows, err := s.query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ChatConversation
	for rows.Next() {
		c := ChatConversation{CreatedBy: user}
		var starred int
		if rows.Scan(&c.ID, &c.TargetID, &c.ConvID, &c.Title, &starred, &c.CreatedAt, &c.UpdatedAt) == nil {
			c.Starred = starred != 0
			out = append(out, c)
		}
	}
	return out
}

// ListAllConversations returns every user's conversations for the admin oversight view (chats are
// owner-private in the normal UI), most-recently-updated first, optionally filtered by owner and/or
// target. Unlike ListConversations it carries created_by so the admin sees whose thread it is.
func (s *Store) ListAllConversations(filterUser string, targetID int64) []ChatConversation {
	q := `SELECT id, target_id, COALESCE(conv_id,''), COALESCE(created_by,''), COALESCE(title,''), COALESCE(starred,0), created_at, updated_at
		FROM chat_conversations WHERE 1=1`
	var args []any
	if filterUser != "" {
		q += ` AND created_by=?`
		args = append(args, filterUser)
	}
	if targetID != 0 {
		q += ` AND target_id=?`
		args = append(args, targetID)
	}
	q += ` ORDER BY updated_at DESC, id DESC`
	rows, err := s.query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ChatConversation
	for rows.Next() {
		var c ChatConversation
		var starred int
		if rows.Scan(&c.ID, &c.TargetID, &c.ConvID, &c.CreatedBy, &c.Title, &starred, &c.CreatedAt, &c.UpdatedAt) == nil {
			c.Starred = starred != 0
			out = append(out, c)
		}
	}
	return out
}

// GetConversation loads one conversation by id (ok=false if absent).
func (s *Store) GetConversation(id int64) (ChatConversation, bool) {
	var c ChatConversation
	var conv, title, by sql.NullString
	var starred int
	err := s.queryRow(
		`SELECT id, target_id, conv_id, created_by, title, COALESCE(starred,0), created_at, updated_at
		 FROM chat_conversations WHERE id=?`, id).
		Scan(&c.ID, &c.TargetID, &conv, &by, &title, &starred, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return ChatConversation{}, false
	}
	c.ConvID, c.CreatedBy, c.Title, c.Starred = conv.String, by.String, title.String, starred != 0
	return c, true
}

// AfterTurn records the result of one chat turn: bind Dify's conversation_id if not yet
// set, set the title from the first message if it is still empty, and bump updated_at —
// all in one write so the conversation list stays ordered and reopenable.
func (s *Store) AfterTurn(id int64, convID, titleIfEmpty string) error {
	_, err := s.exec(
		`UPDATE chat_conversations SET
		   updated_at = ?,
		   conv_id    = CASE WHEN (conv_id IS NULL OR conv_id='') AND ? <> '' THEN ? ELSE conv_id END,
		   title      = CASE WHEN (title   IS NULL OR title  ='')             THEN ? ELSE title   END
		 WHERE id = ?`,
		nowStr(), convID, convID, titleIfEmpty, id)
	return err
}

// RenameConversation sets a conversation's title.
func (s *Store) RenameConversation(id int64, title string) error {
	_, err := s.exec(`UPDATE chat_conversations SET title=? WHERE id=?`, title, id)
	return err
}

// SetConversationStarred pins/unpins a conversation to the top of its owner's list.
func (s *Store) SetConversationStarred(id int64, starred bool) error {
	v := 0
	if starred {
		v = 1
	}
	_, err := s.exec(`UPDATE chat_conversations SET starred=? WHERE id=?`, v, id)
	return err
}

// DeleteConversation removes the portal's index row (Dify still holds the messages).
func (s *Store) DeleteConversation(id int64) error {
	_, err := s.exec(`DELETE FROM chat_conversations WHERE id=?`, id)
	return err
}
