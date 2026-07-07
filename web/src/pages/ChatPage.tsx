import { useEffect, useRef, useState } from 'react'
import { App, Button, Drawer, Empty, Grid, Input, Popconfirm, Select, Spin, Typography, theme } from 'antd'
import { DeleteOutlined, MessageOutlined, PlusOutlined, RobotOutlined, SendOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import Markdown from '../components/Markdown'
import { difyModeTag } from '../lib/batchUi'
import type { ChatConversation, ChatTarget, ChatTurn } from '../api/types'

type Msg = { role: 'user' | 'assistant'; content: string }

// An interactive chat/assistant surface (docs/adr/0012-interactive-chat.md): pick a Dify
// chat/agent target and hold a continuous, context-keeping conversation. The portal is a
// passthrough — Dify owns the history/memory (via conversation_id); this page just lists
// the user's conversations and renders the turns.
export default function ChatPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { token } = theme.useToken()
  const compact = !Grid.useBreakpoint().md // phone / small tablet: fold the sidebar into a drawer
  const [navOpen, setNavOpen] = useState(false)
  const [targets, setTargets] = useState<ChatTarget[]>([])
  const [targetId, setTargetId] = useState<number>()
  const [convs, setConvs] = useState<ChatConversation[]>([])
  const [convId, setConvId] = useState<number>()
  const [msgs, setMsgs] = useState<Msg[]>([])
  const [input, setInput] = useState('')
  const [sending, setSending] = useState(false)
  const [loadingHist, setLoadingHist] = useState(false)
  const [intro, setIntro] = useState<{ opening: string; suggested: string[] } | null>(null)
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
        if (r.targets?.length) setTargetId(r.targets[0].id)
      })
      .catch(() => {})
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
        .get<{ opening: string; suggested: string[] }>(`/api/chat/targets/${targetId}/intro`)
        .then(setIntro)
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

  // While a conversation is open, gently poll Dify's history so a turn that finishes after
  // the user reloaded / opened another tab shows up on its own. Skip while a send is in
  // flight (the optimistic bubbles are authoritative then), and never shrink the thread —
  // so a momentarily-empty history (eventual consistency) can't blank a fresh answer.
  useEffect(() => {
    if (convId == null) return
    let cancelled = false
    const id = setInterval(async () => {
      if (sendingRef.current) return
      try {
        const m = await fetchHistory(convId)
        if (!cancelled && m.length >= msgsLenRef.current) setMsgs(m)
      } catch {
        /* ignore transient errors */
      }
    }, 5000)
    return () => {
      cancelled = true
      clearInterval(id)
    }
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
      const r = await api.post<{ answer: string }>(`/api/chat/conversations/${id}/messages`, { query: q })
      setMsgs((m) => [...m, { role: 'assistant', content: r.answer || '' }])
      loadConvs(targetId) // refresh titles + ordering
    } catch (e) {
      setMsgs((m) => [...m, { role: 'assistant', content: '⚠️ ' + ((e as Error).message || t('chat.sendFailed')) }])
    } finally {
      setSending(false)
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

  const target = targets.find((tg) => tg.id === targetId)

  if (targets.length === 0) {
    return (
      <div style={{ padding: 48 }}>
        <Empty description={t('chat.noTargets')} />
      </div>
    )
  }

  const bubble = (m: Msg, i: number) => {
    const mine = m.role === 'user'
    return (
      <div key={i} style={{ display: 'flex', justifyContent: mine ? 'flex-end' : 'flex-start', marginBottom: 12 }}>
        <div
          style={{
            maxWidth: '78%',
            padding: '8px 12px',
            borderRadius: 10,
            background: mine ? token.colorPrimary : token.colorFillSecondary,
            color: mine ? token.colorTextLightSolid : token.colorText,
            overflowWrap: 'anywhere',
          }}
        >
          {mine ? <span style={{ whiteSpace: 'pre-wrap' }}>{m.content}</span> : <Markdown md={m.content} />}
        </div>
      </div>
    )
  }

  const pickConv = (id: number) => {
    setNavOpen(false)
    openConv(id)
  }
  const startNew = () => {
    setNavOpen(false)
    newConv()
  }

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
      <Button icon={<PlusOutlined />} onClick={startNew} block>
        {t('chat.newConversation')}
      </Button>
      <div style={{ overflowY: 'auto', flex: 1, borderTop: `1px solid ${token.colorBorderSecondary}`, paddingTop: 8 }}>
        {convs.length === 0 ? (
          <Typography.Text type="secondary" style={{ fontSize: 12, padding: 8, display: 'block' }}>
            {t('chat.noConversations')}
          </Typography.Text>
        ) : (
          convs.map((c) => (
            <div
              key={c.id}
              onClick={() => pickConv(c.id)}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 6,
                padding: '6px 8px',
                borderRadius: 8,
                cursor: 'pointer',
                background: c.id === convId ? token.colorFillSecondary : 'transparent',
              }}
            >
              <Typography.Text ellipsis style={{ flex: 1, fontSize: 13 }}>
                {c.title || t('chat.untitled')}
              </Typography.Text>
              <Popconfirm title={t('chat.deleteConfirm')} onConfirm={() => delConv(c.id)}>
                <Button
                  size="small"
                  type="text"
                  danger
                  icon={<DeleteOutlined />}
                  onClick={(e) => e.stopPropagation()}
                  title={t('common.delete')}
                />
              </Popconfirm>
            </div>
          ))
        )}
      </div>
    </div>
  )

  return (
    <div style={{ display: 'flex', gap: 16, height: 'calc(100dvh - 130px)', minHeight: 380 }}>
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
      <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', border: `1px solid ${token.colorBorderSecondary}`, borderRadius: 10, overflow: 'hidden' }}>
        <div style={{ padding: '8px 14px', borderBottom: `1px solid ${token.colorBorderSecondary}`, display: 'flex', alignItems: 'center', gap: 8 }}>
          {compact && (
            <Button type="text" size="small" icon={<MessageOutlined />} onClick={() => setNavOpen(true)} title={t('chat.conversations')} />
          )}
          <RobotOutlined />
          <Typography.Text strong ellipsis style={{ flex: 1, minWidth: 0 }}>
            {target?.name}
          </Typography.Text>
          {difyModeTag(t, target?.mode)}
        </div>
        <div ref={scrollRef} onScroll={onThreadScroll} style={{ flex: 1, overflowY: 'auto', padding: 16 }}>
          {loadingHist ? (
            <div style={{ textAlign: 'center', paddingTop: 40 }}>
              <Spin />
            </div>
          ) : msgs.length === 0 ? (
            intro && (intro.opening || intro.suggested.length > 0) ? (
              // The assistant's opening statement + suggested questions (Dify's greeting).
              <div>
                {intro.opening && (
                  <div style={{ display: 'flex', justifyContent: 'flex-start', marginBottom: 12 }}>
                    <div style={{ maxWidth: '78%', padding: '8px 12px', borderRadius: 10, background: token.colorFillSecondary, color: token.colorText, overflowWrap: 'anywhere' }}>
                      <Markdown md={intro.opening} />
                    </div>
                  </div>
                )}
                {intro.suggested.length > 0 && (
                  <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
                    {intro.suggested.map((q) => (
                      <Button key={q} size="small" onClick={() => send(q)}>
                        {q}
                      </Button>
                    ))}
                  </div>
                )}
              </div>
            ) : (
              <div style={{ textAlign: 'center', color: token.colorTextTertiary, paddingTop: 60 }}>
                <RobotOutlined style={{ fontSize: 32 }} />
                <div style={{ marginTop: 8 }}>{t('chat.emptyThread')}</div>
              </div>
            )
          ) : (
            <>
              {msgs.map(bubble)}
              {sending && (
                <div style={{ display: 'flex', justifyContent: 'flex-start' }}>
                  <div style={{ padding: '8px 12px', borderRadius: 10, background: token.colorFillSecondary }}>
                    <Spin size="small" />
                  </div>
                </div>
              )}
            </>
          )}
        </div>
        <div style={{ padding: 12, borderTop: `1px solid ${token.colorBorderSecondary}`, display: 'flex', gap: 8 }}>
          <Input.TextArea
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onPressEnter={(e) => {
              if (!e.shiftKey) {
                e.preventDefault()
                send()
              }
            }}
            autoSize={{ minRows: 1, maxRows: 6 }}
            placeholder={t('chat.inputPlaceholder')}
            disabled={sending}
          />
          <Button type="primary" icon={<SendOutlined />} loading={sending} onClick={() => send()} disabled={!input.trim()}>
            {t('chat.send')}
          </Button>
        </div>
      </div>
    </div>
  )
}
