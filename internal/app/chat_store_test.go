package app

import "testing"

// The chat conversation index: create/list (per-user, per-target), get, AfterTurn binds
// the Dify conversation_id + title once (sticky), rename, delete.
func TestChatConversationCRUD(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)

	id, err := st.CreateConversation(tgt, "alice")
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	// A fresh conversation has no Dify id or title yet.
	c, ok := st.GetConversation(id)
	if !ok || c.CreatedBy != "alice" || c.ConvID != "" || c.Title != "" {
		t.Fatalf("fresh conversation = %+v ok=%v", c, ok)
	}

	// Private to the owner.
	if n := len(st.ListConversations("alice", 0)); n != 1 {
		t.Fatalf("alice sees %d, want 1", n)
	}
	if n := len(st.ListConversations("bob", 0)); n != 0 {
		t.Fatalf("bob sees %d, want 0 (chats are private)", n)
	}

	// First turn binds the Dify conversation_id and titles from the first message.
	if err := st.AfterTurn(id, "dify-conv-1", "what is 600519"); err != nil {
		t.Fatalf("AfterTurn: %v", err)
	}
	c, _ = st.GetConversation(id)
	if c.ConvID != "dify-conv-1" || c.Title != "what is 600519" {
		t.Fatalf("after first turn = {conv:%q title:%q}, want dify-conv-1 / first message", c.ConvID, c.Title)
	}
	// Later turns keep the id + title (sticky) but still bump ordering.
	if err := st.AfterTurn(id, "dify-conv-1", "a different message"); err != nil {
		t.Fatalf("AfterTurn 2: %v", err)
	}
	c, _ = st.GetConversation(id)
	if c.ConvID != "dify-conv-1" || c.Title != "what is 600519" {
		t.Fatalf("conv_id/title must be sticky, got {conv:%q title:%q}", c.ConvID, c.Title)
	}

	// Per-target filtering.
	other := seedTarget(t, st)
	st.CreateConversation(other, "alice")
	if n := len(st.ListConversations("alice", tgt)); n != 1 {
		t.Fatalf("alice/target %d sees %d, want 1", tgt, n)
	}
	if n := len(st.ListConversations("alice", 0)); n != 2 {
		t.Fatalf("alice all-targets sees %d, want 2", n)
	}

	// Rename + delete.
	if err := st.RenameConversation(id, "renamed"); err != nil {
		t.Fatalf("RenameConversation: %v", err)
	}
	if c, _ := st.GetConversation(id); c.Title != "renamed" {
		t.Fatalf("rename: title = %q, want renamed", c.Title)
	}
	if err := st.DeleteConversation(id); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if _, ok := st.GetConversation(id); ok {
		t.Fatal("conversation still present after delete")
	}
}

// ListAllConversations is the admin oversight view: unlike ListConversations it returns every
// user's threads (not just the caller's), carries created_by, and honors the owner + target filters.
func TestListAllConversations(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	other := seedTarget(t, st)
	st.CreateConversation(tgt, "alice")
	st.CreateConversation(tgt, "bob")
	st.CreateConversation(other, "bob")

	all := st.ListAllConversations("", 0)
	if len(all) != 3 {
		t.Fatalf("ListAllConversations() = %d, want 3 across users", len(all))
	}
	for _, c := range all {
		if c.CreatedBy == "" {
			t.Fatalf("conversation %d missing created_by in admin list", c.ID)
		}
	}
	if n := len(st.ListAllConversations("bob", 0)); n != 2 {
		t.Fatalf("ListAllConversations(bob) = %d, want 2", n)
	}
	if n := len(st.ListAllConversations("alice", 0)); n != 1 {
		t.Fatalf("ListAllConversations(alice) = %d, want 1", n)
	}
	if n := len(st.ListAllConversations("", tgt)); n != 2 {
		t.Fatalf("ListAllConversations(target=%d) = %d, want 2", tgt, n)
	}
}

// Starring a conversation is per-row (reflected by Get + List) and sorts starred
// conversations ahead of the rest, independent of recency.
func TestChatConversationStar(t *testing.T) {
	st := newTestStore(t)
	tgt := seedTarget(t, st)

	older, _ := st.CreateConversation(tgt, "alice")
	newer, _ := st.CreateConversation(tgt, "alice")
	// By default the newest lists first and nothing is starred.
	if got := st.ListConversations("alice", 0); got[0].ID != newer || got[0].Starred {
		t.Fatalf("default order: want newest (%d) first and unstarred, got %+v", newer, got[0])
	}

	// Star the older one — it jumps ahead of the newer, unstarred one.
	if err := st.SetConversationStarred(older, true); err != nil {
		t.Fatalf("SetConversationStarred: %v", err)
	}
	if got := st.ListConversations("alice", 0); got[0].ID != older || !got[0].Starred {
		t.Fatalf("after star: want %d first and starred, got %+v", older, got[0])
	}
	if c, _ := st.GetConversation(older); !c.Starred {
		t.Fatal("GetConversation: starred = false, want true")
	}

	// Unstarring restores recency order.
	if err := st.SetConversationStarred(older, false); err != nil {
		t.Fatalf("unstar: %v", err)
	}
	if got := st.ListConversations("alice", 0); got[0].ID != newer {
		t.Fatalf("after unstar: want newest (%d) first, got %d", newer, got[0].ID)
	}
}
