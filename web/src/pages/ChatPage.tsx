import { useEffect, useRef, useState } from 'react'
import { App, Button, Empty, Input, Popconfirm, Select, Space, Spin, Typography, theme } from 'antd'
import { DeleteOutlined, PlusOutlined, RobotOutlined, SendOutlined } from '@ant-design/icons'
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
  const [targets, setTargets] = useState<ChatTarget[]>([])
  const [targetId, setTargetId] = useState<number>()
  const [convs, setConvs] = useState<ChatConversation[]>([])
  const [convId, setConvId] = useState<number>()
  const [msgs, setMsgs] = useState<Msg[]>([])
  const [input, setInput] = useState('')
  const [sending, setSending] = useState(false)
  const [loadingHist, setLoadingHist] = useState(false)
  const scrollRef = useRef<HTMLDivElement>(null)

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
    loadConvs(targetId)
  }, [targetId])

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: 'smooth' })
  }, [msgs, sending])

  const openConv = async (id: number) => {
    setConvId(id)
    setMsgs([])
    setLoadingHist(true)
    try {
      const r = await api.get<{ turns: ChatTurn[] }>(`/api/chat/conversations/${id}/messages`)
      const m: Msg[] = []
      for (const tn of r.turns || []) {
        if (tn.query) m.push({ role: 'user', content: tn.query })
        if (tn.answer) m.push({ role: 'assistant', content: tn.answer })
      }
      setMsgs(m)
    } catch {
      /* history unavailable — leave the thread empty */
    } finally {
      setLoadingHist(false)
    }
  }

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

  const send = async () => {
    const q = input.trim()
    if (!q || sending) return
    const id = await ensureConv()
    if (!id) return
    setInput('')
    setMsgs((m) => [...m, { role: 'user', content: q }])
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

  return (
    <div style={{ display: 'flex', gap: 16, height: 'calc(100vh - 140px)', minHeight: 420 }}>
      {/* Left: target picker + conversation list */}
      <div style={{ width: 260, flexShrink: 0, display: 'flex', flexDirection: 'column', gap: 10 }}>
        <Select
          style={{ width: '100%' }}
          value={targetId}
          onChange={setTargetId}
          options={targets.map((tg) => ({ value: tg.id, label: tg.name }))}
        />
        <Button icon={<PlusOutlined />} onClick={newConv} block>
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
                onClick={() => openConv(c.id)}
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

      {/* Right: message thread + composer */}
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column', border: `1px solid ${token.colorBorderSecondary}`, borderRadius: 10, overflow: 'hidden' }}>
        <div style={{ padding: '8px 14px', borderBottom: `1px solid ${token.colorBorderSecondary}` }}>
          <Space>
            <RobotOutlined />
            <Typography.Text strong>{target?.name}</Typography.Text>
            {difyModeTag(t, target?.mode)}
          </Space>
        </div>
        <div ref={scrollRef} style={{ flex: 1, overflowY: 'auto', padding: 16 }}>
          {loadingHist ? (
            <div style={{ textAlign: 'center', paddingTop: 40 }}>
              <Spin />
            </div>
          ) : msgs.length === 0 ? (
            <div style={{ textAlign: 'center', color: token.colorTextTertiary, paddingTop: 60 }}>
              <RobotOutlined style={{ fontSize: 32 }} />
              <div style={{ marginTop: 8 }}>{t('chat.emptyThread')}</div>
            </div>
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
          <Button type="primary" icon={<SendOutlined />} loading={sending} onClick={send} disabled={!input.trim()}>
            {t('chat.send')}
          </Button>
        </div>
      </div>
    </div>
  )
}
