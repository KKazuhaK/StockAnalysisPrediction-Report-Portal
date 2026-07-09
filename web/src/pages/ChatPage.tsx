import { useEffect, useMemo, useRef, useState } from 'react'
import { App, Avatar, Button, Drawer, Dropdown, Empty, Grid, Input, Modal, Select, Spin, Typography, theme } from 'antd'
import type { MenuProps } from 'antd'
import { ArrowUpOutlined, DeleteOutlined, EditOutlined, MessageOutlined, MoreOutlined, PlusOutlined, RobotOutlined, StarFilled, StarOutlined, StopOutlined, UnorderedListOutlined } from '@ant-design/icons'
import { useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api, ApiError } from '../api/client'
import { useAuth } from '../auth'
import Markdown from '../components/Markdown'
import { difyModeTag } from '../lib/batchUi'
import type { ChatConversation, ChatTarget, ChatTurn } from '../api/types'

type Msg = { role: 'user' | 'assistant'; content: string }

// Starred conversations pin to the top; within each group the server's recency order is kept
// (Array.prototype.sort is stable), so this mirrors the backend ORDER BY.
const sortConvs = (cs: ChatConversation[]) => [...cs].sort((a, b) => Number(b.starred) - Number(a.starred))

// An interactive chat/assistant surface (docs/adr/0012-interactive-chat.md): pick a Dify
// chat/agent target and hold a continuous, context-keeping conversation. The portal is a
// passthrough — Dify owns the history/memory (via conversation_id); this page just lists
// the user's conversations and renders the turns.
//
// Layout is modeled on a modern chat assistant: a new conversation opens as a centered
// greeting hero with the composer in the middle; once messages exist the composer docks at
// the bottom and the thread scrolls above it, filling the panel width. Assistant replies
// render as full-width markdown next to an avatar (best for long reports); the user's own
// messages sit in a soft right-aligned bubble. Each conversation has a context menu to
// rename, star (pin to top), or delete it.
export default function ChatPage() {
  const { t } = useTranslation()
  const { message, modal } = App.useApp()
  const { token } = theme.useToken()
  const { name } = useAuth()
  const [sp] = useSearchParams()
  const compact = !Grid.useBreakpoint().md // phone / small tablet: fold the sidebar into a drawer
  const padX = compact ? 16 : 32 // the thread/composer fill the panel width with this side gutter
  const [navOpen, setNavOpen] = useState(false)
  const [targets, setTargets] = useState<ChatTarget[]>([])
  const [targetId, setTargetId] = useState<number>()
  const [convs, setConvs] = useState<ChatConversation[]>([])
  const [convId, setConvId] = useState<number>()
  const [hoverConv, setHoverConv] = useState<number | null>(null)
  const [menuConv, setMenuConv] = useState<number | null>(null) // which row's ⋮ menu is open
  const [renameId, setRenameId] = useState<number | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [msgs, setMsgs] = useState<Msg[]>([])
  const [input, setInput] = useState('')
  const [focused, setFocused] = useState(false)
  const [sending, setSending] = useState(false)
  const [loadingHist, setLoadingHist] = useState(false)
  const [intro, setIntro] = useState<{ opening: string } | null>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  // Whether the thread is scrolled to (near) the bottom. Auto-scroll only follows when it is,
  // so a poll refresh or a new message never yanks the user back down while they read above.
  const pinnedRef = useRef(true)
  const onThreadScroll = () => {
    const el = scrollRef.current
    if (el) pinnedRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 120
  }
  // Mirrors of state read inside the poll interval (so the closure sees current values).
  const sendingRef = useRef(false)
  const msgsLenRef = useRef(0)
  useEffect(() => {
    sendingRef.current = sending
  }, [sending])
  useEffect(() => {
    msgsLenRef.current = msgs.length
  }, [msgs])

  useEffect(() => {
    api
      .get<{ targets: ChatTarget[] }>('/api/chat/targets')
      .then((r) => {
        setTargets(r.targets || [])
        // A pinned entry-button shortcut may deep-link to a specific assistant via ?target=<id>;
        // fall back to the first target if it's absent, unknown, or no longer accessible.
        const want = Number(sp.get('target'))
        const initial = r.targets?.find((tg) => tg.id === want) ?? r.targets?.[0]
        if (initial) setTargetId(initial.id)
      })
      .catch(() => {})
    // Read ?target once on mount (the intended "open pre-selected" UX); later manual switching wins.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const loadConvs = (tid?: number) => {
    if (!tid) return
    api
      .get<{ conversations: ChatConversation[] }>(`/api/chat/conversations?target_id=${tid}`)
      .then((r) => setConvs(r.conversations || []))
      .catch(() => {})
  }
  useEffect(() => {
    setConvId(undefined)
    setMsgs([])
    setIntro(null)
    loadConvs(targetId)
    if (targetId) {
      api
        .get<{ opening: string }>(`/api/chat/targets/${targetId}/intro`)
        .then((r) => setIntro({ opening: r.opening }))
        .catch(() => setIntro(null))
    }
  }, [targetId])

  useEffect(() => {
    if (pinnedRef.current) scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: 'smooth' })
  }, [msgs, sending])

  // Dify's history for a conversation, flattened into a message thread.
  const fetchHistory = async (id: number): Promise<Msg[]> => {
    const r = await api.get<{ turns: ChatTurn[] }>(`/api/chat/conversations/${id}/messages`)
    const m: Msg[] = []
    for (const tn of r.turns || []) {
      if (tn.query) m.push({ role: 'user', content: tn.query })
      if (tn.answer) m.push({ role: 'assistant', content: tn.answer })
    }
    return m
  }

  const openConv = async (id: number) => {
    setConvId(id)
    setMsgs([])
    setLoadingHist(true)
    try {
      setMsgs(await fetchHistory(id))
    } catch {
      /* history unavailable — leave the thread empty */
    } finally {
      setLoadingHist(false)
    }
  }

  // When a conversation is opened, briefly poll Dify's history so a turn that finished while
  // the user was away (reloaded / another tab) shows up on its own — then STOP, so an idle
  // conversation isn't hitting Dify forever. Only a short window is needed: a turn sent in
  // this tab is shown directly by send(); polling only backstops the arrive-from-elsewhere
  // case. Skips while a send is in flight, never shrinks the thread, and stops once an answer
  // lands. Re-opening the conversation restarts the window.
  useEffect(() => {
    if (convId == null) return
    let cancelled = false
    let attempts = 0
    let timer: ReturnType<typeof setTimeout>
    const tick = async () => {
      if (cancelled) return
      if (!sendingRef.current) {
        attempts += 1
        try {
          const m = await fetchHistory(convId)
          if (!cancelled && m.length >= msgsLenRef.current) setMsgs(m)
        } catch {
          /* ignore transient errors */
        }
      }
      if (!cancelled && attempts < 6) timer = setTimeout(tick, 5000) // ~30s window, then stop
    }
    timer = setTimeout(tick, 5000)
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [convId])

  // Reconcile: adopt Dify's real conversation state when it is at least as complete as the local
  // view. This is the recovery primitive for a client-side interruption (phone lock, network drop)
  // that rejected the send request even though the turn ran/completed server-side in Dify.
  const reconcile = async (id: number): Promise<boolean> => {
    try {
      const m = await fetchHistory(id)
      if (m.length >= msgsLenRef.current) {
        setMsgs(m)
        return true
      }
    } catch {
      /* transient — the caller retries or gives up */
    }
    return false
  }
  // Poll reconcile for a short window: when the request dropped the turn may still be finishing, so
  // give Dify a chance to persist the answer before we conclude anything.
  const reconcilePoll = async (id: number): Promise<boolean> => {
    for (let i = 0; i < 5; i++) {
      if (await reconcile(id)) return true
      await new Promise((r) => setTimeout(r, 4000))
    }
    return false
  }

  // Return-from-background recovery: a turn interrupted while the phone was locked / the tab hidden
  // rejects client-side and renders "failed", but actually completed in Dify. The moment the tab
  // becomes visible again, reconcile so the real answer replaces the false failure — no manual reload.
  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState === 'visible' && convId != null && !sendingRef.current) reconcile(convId)
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [convId])

  // Create a conversation if none is open; returns its id (or undefined on failure).
  const ensureConv = async (): Promise<number | undefined> => {
    if (convId) return convId
    if (!targetId) {
      message.warning(t('chat.pickTarget'))
      return undefined
    }
    try {
      const c = await api.post<ChatConversation>('/api/chat/conversations', { target_id: targetId })
      setConvs((cs) => [c, ...cs])
      setConvId(c.id)
      return c.id
    } catch (e) {
      message.error((e as Error).message || 'failed')
      return undefined
    }
  }

  const newConv = () => {
    setConvId(undefined)
    setMsgs([])
    setInput('')
  }

  const send = async (text?: string) => {
    const q = (text ?? input).trim()
    if (!q || sending) return
    const id = await ensureConv()
    if (!id) return
    if (text == null) setInput('')
    pinnedRef.current = true // follow one's own new message to the bottom
    setMsgs((m) => [...m, { role: 'user', content: q }])
    // Title an untitled conversation from its first message right away (the backend does the
    // same; loadConvs later reconciles) — so the list shows the message, not "Untitled".
    setConvs((cs) => cs.map((c) => (c.id === id && !c.title ? { ...c, title: q.length > 24 ? q.slice(0, 24) + '…' : q } : c)))
    setSending(true)
    try {
      const r = await api.post<{ answer: string; stopped?: boolean }>(`/api/chat/conversations/${id}/messages`, { query: q })
      // A stopped turn returns its partial answer + stopped:true; show the partial, or a muted
      // note if the turn was stopped before anything streamed.
      setMsgs((m) => [...m, { role: 'assistant', content: r.answer || (r.stopped ? '_' + t('chat.stopped') + '_' : '') }])
      loadConvs(targetId) // refresh titles + ordering
    } catch (e) {
      // The assistant is at its concurrency ceiling — nothing was sent. Undo the optimistic
      // user bubble, restore what they typed, and ask them to retry (don't queue interactively).
      if (e instanceof ApiError && e.status === 429) {
        setMsgs((m) => m.slice(0, -1))
        if (text == null) setInput(q)
        message.warning(t('chat.busy'))
      } else {
        // A client-side interruption (phone lock, dropped network) rejects the request even though
        // the turn is running/finished in Dify. Reconcile from history in the background before
        // declaring failure — show the ⚠️ note only if the turn genuinely never landed.
        reconcilePoll(id).then((recovered) => {
          if (!recovered) {
            setMsgs((m) => [...m, { role: 'assistant', content: '⚠️ ' + ((e as Error).message || t('chat.sendFailed')) }])
          }
        })
      }
    } finally {
      setSending(false)
    }
  }

  // Stop the in-flight turn: the backend cancels the Dify stream (the send() promise then
  // resolves with the partial answer + stopped:true) and best-effort stops the Dify run so it
  // stops billing.
  const stopTurn = async () => {
    if (!convId || !sending) return
    try {
      await api.post(`/api/chat/conversations/${convId}/stop`)
    } catch {
      /* best-effort — the pending send() still resolves and renders the partial */
    }
  }

  const delConv = async (id: number) => {
    try {
      await api.del(`/api/chat/conversations/${id}`)
      setConvs((cs) => cs.filter((c) => c.id !== id))
      if (convId === id) newConv()
    } catch (e) {
      message.error((e as Error).message || 'failed')
    }
  }

  const confirmDelete = (c: ChatConversation) =>
    modal.confirm({
      title: t('chat.deleteConfirm'),
      okText: t('common.delete'),
      okButtonProps: { danger: true },
      onOk: () => delConv(c.id),
    })

  // Star/unstar optimistically (snappy pin), reverting the local flag if the write fails.
  const toggleStar = async (c: ChatConversation) => {
    const next = !c.starred
    setConvs((cs) => sortConvs(cs.map((x) => (x.id === c.id ? { ...x, starred: next } : x))))
    try {
      await api.post(`/api/chat/conversations/${c.id}/star`, { starred: next })
    } catch (e) {
      setConvs((cs) => sortConvs(cs.map((x) => (x.id === c.id ? { ...x, starred: c.starred } : x))))
      message.error((e as Error).message || 'failed')
    }
  }

  const openRename = (c: ChatConversation) => {
    setRenameValue(c.title || '')
    setRenameId(c.id)
  }
  const submitRename = async () => {
    if (renameId == null) return
    const id = renameId
    const title = renameValue.trim()
    setRenameId(null)
    if (!title) return
    try {
      await api.post(`/api/chat/conversations/${id}/rename`, { title })
      setConvs((cs) => cs.map((x) => (x.id === id ? { ...x, title } : x)))
    } catch (e) {
      message.error((e as Error).message || 'failed')
    }
  }

  const target = targets.find((tg) => tg.id === targetId)

  // Time-of-day greeting, computed once per render from the browser clock.
  const greeting = useMemo(() => {
    const h = new Date().getHours()
    const key = h < 12 ? 'greetingMorning' : h < 18 ? 'greetingAfternoon' : 'greetingEvening'
    return t(`chat.${key}`, { name: name || '' }).replace(/[，,、]\s*$/, '')
  }, [t, name])

  if (targets.length === 0) {
    return (
      <div style={{ padding: 48 }}>
        <Empty description={t('chat.noTargets')} />
      </div>
    )
  }

  const assistantAvatar = (size: number) => (
    <Avatar
      size={size}
      icon={<RobotOutlined />}
      style={{ flexShrink: 0, background: token.colorPrimary, display: 'inline-flex', alignItems: 'center', justifyContent: 'center' }}
    />
  )

  const bubble = (m: Msg, i: number) => {
    if (m.role === 'user') {
      return (
        <div key={i} style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 22 }}>
          <div
            style={{
              maxWidth: '84%',
              padding: '10px 15px',
              borderRadius: 18,
              borderTopRightRadius: 6,
              background: token.colorFillSecondary,
              color: token.colorText,
              whiteSpace: 'pre-wrap',
              overflowWrap: 'anywhere',
              fontSize: 15,
              lineHeight: 1.6,
            }}
          >
            {m.content}
          </div>
        </div>
      )
    }
    return (
      <div key={i} style={{ display: 'flex', gap: 12, marginBottom: 26 }}>
        {assistantAvatar(30)}
        <div style={{ flex: 1, minWidth: 0, paddingTop: 3 }}>
          <Markdown md={m.content} />
        </div>
      </div>
    )
  }

  // The composer: a rounded, elevated box with the textarea and a circular send button. It
  // fills the panel width; `hero` = the centered new-conversation variant (taller default).
  const composer = (hero: boolean) => (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        gap: 4,
        padding: 8,
        borderRadius: 22,
        background: token.colorBgContainer,
        border: `1px solid ${focused ? token.colorPrimary : token.colorBorder}`,
        boxShadow: focused ? `0 0 0 3px ${token.colorPrimaryBg}` : token.boxShadowTertiary,
        transition: 'border-color .15s, box-shadow .15s',
      }}
    >
      <Input.TextArea
        value={input}
        variant="borderless"
        onChange={(e) => setInput(e.target.value)}
        onFocus={() => setFocused(true)}
        onBlur={() => setFocused(false)}
        onPressEnter={(e) => {
          if (!e.shiftKey) {
            e.preventDefault()
            send()
          }
        }}
        autoSize={{ minRows: hero ? 2 : 1, maxRows: 8 }}
        placeholder={t('chat.inputPlaceholder')}
        style={{ fontSize: 15, padding: '6px 8px', background: 'transparent', resize: 'none' }}
      />
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', paddingInline: 6, paddingBottom: 2 }}>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {t('chat.enterHint')}
        </Typography.Text>
        {sending ? (
          <Button shape="circle" danger icon={<StopOutlined />} onClick={stopTurn} title={t('chat.stop')} />
        ) : (
          <Button type="primary" shape="circle" icon={<ArrowUpOutlined />} disabled={!input.trim()} onClick={() => send()} title={t('chat.send')} />
        )}
      </div>
    </div>
  )

  const pickConv = (id: number) => {
    setNavOpen(false)
    openConv(id)
  }
  const startNew = () => {
    setNavOpen(false)
    newConv()
  }

  // The per-conversation context menu: rename, star/unstar, delete. domEvent.stopPropagation
  // keeps a menu click from also opening the conversation (the row's onClick).
  const convMenu = (c: ChatConversation): MenuProps['items'] => [
    {
      key: 'rename',
      icon: <EditOutlined />,
      label: t('chat.rename'),
      onClick: ({ domEvent }) => {
        domEvent.stopPropagation()
        openRename(c)
      },
    },
    {
      key: 'star',
      icon: c.starred ? <StarFilled /> : <StarOutlined />,
      label: c.starred ? t('chat.unstar') : t('chat.star'),
      onClick: ({ domEvent }) => {
        domEvent.stopPropagation()
        toggleStar(c)
      },
    },
    { type: 'divider' },
    {
      key: 'delete',
      icon: <DeleteOutlined />,
      danger: true,
      label: t('common.delete'),
      onClick: ({ domEvent }) => {
        domEvent.stopPropagation()
        confirmDelete(c)
      },
    },
  ]

  // Sidebar: target picker + new-conversation + conversation list. A left column on desktop,
  // folded into a drawer on mobile.
  const sidebar = (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10, height: '100%' }}>
      <Select
        style={{ width: '100%' }}
        value={targetId}
        onChange={setTargetId}
        options={targets.map((tg) => ({ value: tg.id, label: tg.name }))}
      />
      <Button type="primary" ghost icon={<PlusOutlined />} onClick={startNew} block style={{ fontWeight: 500 }}>
        {t('chat.newConversation')}
      </Button>
      <div style={{ overflowY: 'auto', flex: 1, borderTop: `1px solid ${token.colorBorderSecondary}`, paddingTop: 8 }}>
        {convs.length === 0 ? (
          <Typography.Text type="secondary" style={{ fontSize: 12, padding: 8, display: 'block' }}>
            {t('chat.noConversations')}
          </Typography.Text>
        ) : (
          convs.map((c) => {
            const active = c.id === convId
            const hovered = hoverConv === c.id
            const showMenu = hovered || active || menuConv === c.id
            return (
              <div
                key={c.id}
                onClick={() => pickConv(c.id)}
                onMouseEnter={() => setHoverConv(c.id)}
                onMouseLeave={() => setHoverConv((h) => (h === c.id ? null : h))}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 8,
                  padding: '7px 10px',
                  borderRadius: 8,
                  cursor: 'pointer',
                  background: active ? token.colorFillSecondary : hovered ? token.colorFillTertiary : 'transparent',
                  transition: 'background .12s',
                }}
              >
                {c.starred ? (
                  <StarFilled style={{ fontSize: 13, color: token.colorWarning, flexShrink: 0 }} />
                ) : (
                  <MessageOutlined style={{ fontSize: 13, color: token.colorTextTertiary, flexShrink: 0 }} />
                )}
                <Typography.Text
                  ellipsis
                  style={{ flex: 1, fontSize: 13, color: active ? token.colorText : token.colorTextSecondary }}
                >
                  {c.title || t('chat.untitled')}
                </Typography.Text>
                <Dropdown
                  menu={{ items: convMenu(c) }}
                  trigger={['click']}
                  placement="bottomRight"
                  onOpenChange={(open) => setMenuConv(open ? c.id : null)}
                >
                  <Button
                    size="small"
                    type="text"
                    icon={<MoreOutlined />}
                    onClick={(e) => e.stopPropagation()}
                    title={t('common.more')}
                    style={{ opacity: showMenu ? 1 : 0, transition: 'opacity .12s', flexShrink: 0 }}
                  />
                </Dropdown>
              </div>
            )
          })
        )}
      </div>
    </div>
  )

  const showHero = !loadingHist && msgs.length === 0

  return (
    <div style={{ display: 'flex', gap: 16, flex: 1, minHeight: 0 }}>
      {/* Conversation list: a fixed left column on desktop, a drawer on mobile. */}
      {compact ? (
        <Drawer
          open={navOpen}
          onClose={() => setNavOpen(false)}
          placement="left"
          width={280}
          title={t('chat.conversations')}
          styles={{ body: { padding: 12 } }}
        >
          {sidebar}
        </Drawer>
      ) : (
        <div style={{ width: 260, flexShrink: 0 }}>{sidebar}</div>
      )}

      {/* Message thread + composer */}
      <div
        style={{
          flex: 1,
          minWidth: 0,
          display: 'flex',
          flexDirection: 'column',
          border: `1px solid ${token.colorBorderSecondary}`,
          borderRadius: 12,
          overflow: 'hidden',
          background: token.colorBgContainer,
        }}
      >
        <div
          style={{
            padding: '10px 16px',
            borderBottom: `1px solid ${token.colorBorderSecondary}`,
            display: 'flex',
            alignItems: 'center',
            gap: 10,
          }}
        >
          {compact && (
            <Button icon={<UnorderedListOutlined />} onClick={() => setNavOpen(true)}>
              {t('chat.conversations')}
            </Button>
          )}
          {assistantAvatar(26)}
          <Typography.Text strong ellipsis style={{ flex: 1, minWidth: 0 }}>
            {target?.name}
          </Typography.Text>
          {difyModeTag(t, target?.mode)}
        </div>

        <div ref={scrollRef} onScroll={onThreadScroll} style={{ flex: 1, overflowY: 'auto' }}>
          {loadingHist ? (
            <div style={{ textAlign: 'center', paddingTop: 60 }}>
              <Spin />
            </div>
          ) : showHero ? (
            // New conversation: a centered greeting hero with the composer in the middle and
            // the assistant's opening line (Dify's greeting) above it.
            <div
              style={{
                minHeight: '100%',
                display: 'flex',
                flexDirection: 'column',
                justifyContent: 'center',
                alignItems: 'center',
                padding: `24px ${padX}px 40px`,
                gap: 20,
              }}
            >
              <div style={{ textAlign: 'center' }}>
                {assistantAvatar(52)}
                <Typography.Title level={3} style={{ margin: '16px 0 6px', fontWeight: 600 }}>
                  {greeting}
                </Typography.Title>
                <Typography.Text type="secondary" style={{ fontSize: 15 }}>
                  {intro?.opening || t('chat.emptyThread')}
                </Typography.Text>
              </div>
              <div style={{ width: '100%' }}>{composer(true)}</div>
            </div>
          ) : (
            <div style={{ padding: `${compact ? 20 : 24}px ${padX}px 8px` }}>
              {msgs.map(bubble)}
              {sending && (
                <div style={{ display: 'flex', gap: 12, marginBottom: 26 }}>
                  {assistantAvatar(30)}
                  <div className="rp-typing" style={{ height: 30, color: token.colorTextTertiary }}>
                    <span />
                    <span />
                    <span />
                  </div>
                </div>
              )}
            </div>
          )}
        </div>

        {/* The docked composer only appears once a conversation is under way — a new chat keeps
            its composer centered in the hero above. */}
        {!showHero && (
          <div style={{ padding: `${compact ? 10 : 12}px ${padX}px ${compact ? 12 : 16}px`, borderTop: `1px solid ${token.colorBorderSecondary}` }}>
            {composer(false)}
          </div>
        )}
      </div>

      {/* Rename dialog (opened from a conversation's context menu). */}
      <Modal
        open={renameId != null}
        title={t('chat.renameTitle')}
        onOk={submitRename}
        onCancel={() => setRenameId(null)}
        okButtonProps={{ disabled: !renameValue.trim() }}
        destroyOnClose
      >
        <Input
          value={renameValue}
          onChange={(e) => setRenameValue(e.target.value)}
          onPressEnter={submitRename}
          maxLength={80}
          autoFocus
          placeholder={t('chat.renameTitle')}
        />
      </Modal>
    </div>
  )
}
