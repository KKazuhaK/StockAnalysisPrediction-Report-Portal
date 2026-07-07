# 12. Interactive chat / assistant surface

## Status

Accepted — 2026-07-07

## Context

Dify apps come in three shapes and the portal now runs all three:

- **Workflow** (`/workflows/run`) — one-shot report generation, driven by the batch/run
  queue (ADR 0004/0011).
- **Chat** and **Agent** (`agent-chat`) apps (`/chat-messages`) — conversational.

Batch is inherently **single-turn**: every run sends `conversation_id: ""`, so each row is
a fresh, memoryless message that produces a report. There was no way to hold a **continuous
conversation** — ask a follow-up, explore, keep context. Dify chat/agent apps support
multi-turn natively (pass the same `conversation_id` and Dify assembles the history), so the
capability is a thin surface away.

Options considered for where a conversation lives: (a) a dedicated interactive chat page;
(b) follow-ups bound to a generated report; (c) chaining a batch's rows into one thread.
(c) fights the run-level scheduler (a conversation must be sequential — it can't parallelise,
so it wastes the queue's concurrency) and mixes two unrelated models; rejected. (b) is
valuable but is a **layer on top of** (a): once a chat surface exists, a report page can seed
it with the report as context. So (a) is the foundation.

## Decision

A dedicated **interactive chat/assistant page** (`/chat`), a first-party cookie-session
surface gated by `PermRunBatch` (a chat turn runs a Dify app — the same money gate as batch).
Pick a chat/agent target, hold a continuous conversation; workflow targets are excluded.

### The portal is a passthrough; Dify owns the conversation

- Each turn the portal sends only `{query, conversation_id, user}` to `/chat-messages`. **Dify
  owns the entire context/memory** — it looks up the conversation, assembles the prompt
  (system + history + memory/summary + the new query), and stores the turn. The portal never
  assembles a prompt, threads history into the model, or manages a token window.
- **Blocking mode**, not streaming: one request → the whole answer + the `conversation_id`.
  Simpler than SSE and proxy-friendly (no long-held stream — the failure mode that forced
  poll mode on batch). Token-by-token streaming and agent tool-thoughts are a later layer.
- `user` is `difyEndUser(created_by)`, held constant across turns so Dify ties them to one
  person's conversation.

### Storage: a thin index, no message mirror

Dify already stores every message (keyed by `conversation_id` + `user`), so the portal stores
**no message content** — only enough to list a user's conversations and reopen them:

```
chat_conversations(id, target_id, conv_id, created_by, title, created_at, updated_at)
```

`conv_id` is Dify's `conversation_id`, empty until the first reply assigns one. On the first
turn the portal binds it and titles the conversation from the first message; every turn bumps
`updated_at` for ordering. History for display on reopen is fetched live from Dify's
`/messages` (display only — it does not drive context). Conversations are **private** to their
owner (no admin override).

Rejected alternatives: fully stateless (list via Dify's `/conversations`) — couples the list
to the Dify `user`-mapping and its list API, and can't title/organise; a full message mirror —
duplicates Dify's data and adds streaming/sync complexity for no v1 benefit.

### No schema migration

One additive `CREATE TABLE IF NOT EXISTS chat_conversations` (+ a `(created_by, target_id,
updated_at)` index), same pattern as the batch tables; SQLite + Postgres both. No existing
table is touched (ADR: additive-only schema).

## Consequences

- Continuous, context-keeping conversations with any Dify chat/agent app, without the portal
  owning context — memory behaviour is whatever the Dify app is configured to do.
- Blocking mode sidesteps the SSE/proxy-buffering issue that batch hit; a chat turn is a plain
  request/response.
- The portal stays a thin orchestrator: it indexes conversations, Dify holds the messages —
  the same "Dify owns the data" split as reports and runs.
- **Durable across leaving the page.** The send handler runs the Dify call on a detached
  context (not the request's), so a turn finishes and is stored by Dify even if the user
  navigates away mid-generation; on return, reopening the conversation refetches it from
  Dify's `/messages`. (Not durable across a *server* restart mid-turn — a chat turn is not
  persisted as resumable work the way a batch run is; the user re-asks. Streaming/async
  turns are a follow-up.)
- **Agent caveat (inherited):** agent-chat apps don't emit a `workflow_run_id`; irrelevant here
  because chat is blocking (no run-id reconcile needed), but the batch-side caveat still stands
  for agent apps run as workflows.

## Follow-ups

- Token-streaming replies (SSE portal→browser) + agent `agent_thought` (tool-call) display.
- Report-anchored follow-ups (option b): a "discuss this report" action that opens `/chat`
  seeded with the report as the first-turn context.
