import { useEffect, useState } from 'react'
import { Alert, App, Button, Card, Divider, InputNumber, Select, Space, Switch, Table, Tag, Typography, theme } from 'antd'
import { DatabaseOutlined, FileTextOutlined, KeyOutlined, MessageOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { CleanupConfig, CleanupResult, CleanupRun, CleanupUsage, CleanupUsageCategory } from '../../api/types'
import StickyActionBar from '../../components/StickyActionBar'

// Storage management console (docs/adr/0017-storage-cleanup.md): a per-category usage dashboard (icon
// cards + a proportion bar), a self-explanatory manual cleanup (the button names what and how old),
// an optional daily/weekly/monthly scheduled retention pass, and the cleanup_runs audit history.
// Reports (core content) are fail-closed, floored, and guarded by a live-count confirmation.

// Category identity colors — the dataviz categorical palette (validated CVD-safe as an ordered set;
// identity is also carried by the icon + name, satisfying the relief rule for the sub-3:1 slots).
const CAT_COLOR: Record<string, { light: string; dark: string }> = {
  batch: { light: '#2a78d6', dark: '#3987e5' }, // blue
  tokens: { light: '#1baf7a', dark: '#199e70' }, // aqua
  reports: { light: '#eda100', dark: '#c98500' }, // yellow
  chat: { light: '#008300', dark: '#008300' }, // green
}
const CAT_ICON: Record<string, React.ReactNode> = {
  batch: <ThunderboltOutlined />,
  tokens: <KeyOutlined />,
  reports: <FileTextOutlined />,
  chat: <MessageOutlined />,
}

// fmtBytes renders an approximate byte count in human units.
function fmtBytes(n: number): string {
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

// isDarkSurface decides light/dark from the resolved antd container color, so the category hues track
// whichever theme is active (not the OS preference).
function isDarkSurface(c: string): boolean {
  const m = /^#?([0-9a-fA-F]{6})$/.exec(c.trim())
  if (!m) return false
  const n = parseInt(m[1], 16)
  const r = (n >> 16) & 255
  const g = (n >> 8) & 255
  const b = n & 255
  return (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255 < 0.5
}

export default function StoragePage() {
  const { t } = useTranslation()
  const { message, modal } = App.useApp()
  const { token } = theme.useToken()
  const dark = isDarkSurface(token.colorBgContainer)
  const catColor = (k: string) => (CAT_COLOR[k] ? (dark ? CAT_COLOR[k].dark : CAT_COLOR[k].light) : token.colorPrimary)

  const [freq, setFreq] = useState<CleanupConfig['freq']>('off')
  const [time, setTime] = useState('03:00')
  const [weekday, setWeekday] = useState(1)
  const [monthday, setMonthday] = useState(1)
  const [batchEnabled, setBatchEnabled] = useState(false)
  const [batchDays, setBatchDays] = useState(90)
  const [tokensEnabled, setTokensEnabled] = useState(false)
  const [tokensGraceDays, setTokensGraceDays] = useState(30)
  const [reportsEnabled, setReportsEnabled] = useState(false)
  const [reportsDays, setReportsDays] = useState(730)
  const [batchFloor, setBatchFloor] = useState(7)
  const [reportsFloor, setReportsFloor] = useState(365)
  const [cfg, setCfg] = useState<CleanupConfig | null>(null) // last-saved view, drives the usage cards
  const [usage, setUsage] = useState<CleanupUsage | null>(null)
  const [history, setHistory] = useState<CleanupRun[]>([])

  const loadConfig = () =>
    api.get<CleanupConfig>('/api/admin/cleanup/config').then((r) => {
      setCfg(r)
      setFreq(r.freq)
      setTime(r.time)
      setWeekday(r.weekday)
      setMonthday(r.monthday)
      setBatchEnabled(r.batch_enabled)
      setBatchDays(r.batch_days)
      setTokensEnabled(r.tokens_enabled)
      setTokensGraceDays(r.tokens_grace_days)
      setReportsEnabled(r.reports_enabled)
      setReportsDays(r.reports_days)
      setBatchFloor(r.batch_floor)
      setReportsFloor(r.reports_floor)
    })
  const loadUsage = () => api.get<CleanupUsage>('/api/admin/cleanup/usage').then(setUsage)
  const loadHistory = () => api.get<{ runs: CleanupRun[] }>('/api/admin/cleanup/history').then((r) => setHistory(r.runs ?? []))

  useEffect(() => {
    loadConfig()
    loadUsage()
    loadHistory()
  }, [])

  const save = async () => {
    await api.post('/api/admin/cleanup/config', {
      freq,
      time,
      weekday,
      monthday,
      batch_enabled: batchEnabled,
      batch_days: batchDays,
      tokens_enabled: tokensEnabled,
      tokens_grace_days: tokensGraceDays,
      reports_enabled: reportsEnabled,
      reports_days: reportsDays,
    })
    message.success(t('common.saved'))
    loadConfig()
    loadUsage()
  }

  const doRun = async (targets: string[]) => {
    const r = await api.post<CleanupResult>('/api/admin/cleanup/run', { targets })
    message.success(t('storage.cleaned', { batch: r.batch, reports: r.reports, tokens: r.tokens }))
    loadUsage()
    loadHistory()
    loadConfig()
  }

  // Reports (core content): preview the live count, then a strict confirm before arming or running.
  const guardReports = async (onConfirm: () => void) => {
    const r = await api.post<CleanupResult>('/api/admin/cleanup/preview', { targets: ['reports'] })
    modal.confirm({
      title: t('storage.confirmReportsTitle'),
      content: t('storage.confirmReportsBody', { count: r.reports, days: cfg?.reports_days ?? reportsDays }),
      okText: t('storage.doClean'),
      cancelText: t('common.cancel'),
      okButtonProps: { danger: true },
      onOk: onConfirm,
    })
  }

  const onToggleReports = (checked: boolean) => {
    if (!checked) {
      setReportsEnabled(false)
      return
    }
    guardReports(() => setReportsEnabled(true))
  }

  // Manual "clean now" for one category: the button already says what & how old; the confirm restates
  // the live count. Reports route through the stricter guardReports path.
  const cleanCategory = async (key: string, days: number) => {
    if (key === 'reports') {
      guardReports(() => doRun(['reports']))
      return
    }
    const r = await api.post<CleanupResult>('/api/admin/cleanup/preview', { targets: [key] })
    const n = key === 'batch' ? r.batch : r.tokens
    modal.confirm({
      title: t('storage.confirmTitle'),
      content: t('storage.confirmBody', { n, days, cat: t(`storage.cat.${key}`) }),
      okText: t('storage.doClean'),
      cancelText: t('common.cancel'),
      okButtonProps: { danger: true },
      onOk: () => doRun([key]),
    })
  }

  // retention/grace currently in effect for each category (last-saved, so labels match what a run does)
  const catDays = (key: string) =>
    key === 'batch' ? cfg?.batch_days ?? batchDays : key === 'tokens' ? cfg?.tokens_grace_days ?? tokensGraceDays : cfg?.reports_days ?? reportsDays

  const cats = usage?.categories ?? []
  const totalBytes = cats.reduce((s, c) => s + c.bytes, 0)

  const renderCard = (c: CleanupUsageCategory) => {
    const color = catColor(c.key)
    const days = catDays(c.key)
    return (
      <div key={c.key} style={{ border: `1px solid ${token.colorBorderSecondary}`, borderRadius: 10, padding: 16, display: 'flex', flexDirection: 'column', gap: 10 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <span style={{ width: 36, height: 36, borderRadius: 9, display: 'grid', placeItems: 'center', background: color + '22', color, fontSize: 18 }}>{CAT_ICON[c.key]}</span>
          <Typography.Text strong>{t(`storage.cat.${c.key}`)}</Typography.Text>
        </div>
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
          <Typography.Title level={4} style={{ margin: 0 }}>
            {fmtBytes(c.bytes)}
          </Typography.Title>
          <Typography.Text type="secondary">{t('storage.rowsN', { n: c.rows })}</Typography.Text>
        </div>
        {c.key === 'chat' ? (
          <Typography.Text type="secondary">{t('storage.ruleChat')}</Typography.Text>
        ) : c.eligible > 0 ? (
          <>
            <Tag color="orange" style={{ width: 'fit-content' }}>
              {t('storage.eligibleN', { n: c.eligible })}
            </Tag>
            <Button
              size="small"
              danger={c.key === 'reports'}
              type={c.key === 'reports' ? 'default' : 'primary'}
              ghost={c.key !== 'reports'}
              onClick={() => cleanCategory(c.key, days)}
              style={{ width: 'fit-content' }}
            >
              {t('storage.act', { days, cat: t(`storage.cat.${c.key}`) })}
            </Button>
          </>
        ) : (
          <Typography.Text type="secondary">{t('storage.noCleanup')}</Typography.Text>
        )}
      </div>
    )
  }

  const freqOptions = [
    { value: 'off', label: t('storage.freqOff') },
    { value: 'daily', label: t('run.freq.daily') },
    { value: 'weekly', label: t('run.freq.weekly') },
    { value: 'monthly', label: t('run.freq.monthly') },
  ]
  const weekdayOptions = [0, 1, 2, 3, 4, 5, 6].map((d) => ({ value: d, label: t(`run.weekday.${d}`) }))

  const row = (label: string, control: React.ReactNode, hint?: string) => (
    <Space wrap>
      <span style={{ display: 'inline-block', minWidth: 120 }}>{label}</span>
      {control}
      {hint ? <Typography.Text type="secondary">{hint}</Typography.Text> : null}
    </Space>
  )

  const histCols = [
    { title: t('storage.histTime'), dataIndex: 'ran_at' },
    { title: t('storage.histTrigger'), dataIndex: 'trigger', render: (v: string) => t(v === 'schedule' ? 'storage.triggerSchedule' : 'storage.triggerManual') },
    {
      title: t('storage.histResult'),
      key: 'result',
      render: (_: unknown, r: CleanupRun) => t('storage.resultLine', { batch: r.batch_deleted, tokens: r.tokens_deleted, reports: r.reports_deleted }),
    },
    {
      title: t('storage.histStatus'),
      dataIndex: 'ok',
      render: (ok: boolean, r: CleanupRun) => (ok ? <Tag color="green">{t('storage.statusOk')}</Tag> : <Tag color="red">{r.error || t('storage.statusFailed')}</Tag>),
    },
  ]

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <Card title={t('storage.usageTitle')}>
        <Space direction="vertical" size={14} style={{ width: '100%' }}>
          {/* Total + proportion bar: which category takes the space, at a glance. */}
          <Space align="center" size={8}>
            <DatabaseOutlined style={{ fontSize: 18, color: token.colorTextSecondary }} />
            <Typography.Text strong>{fmtBytes(usage?.db_bytes ?? 0)}</Typography.Text>
            <Typography.Text type="secondary">
              {t('storage.dbTotal')} · {t('storage.approxNote')}
            </Typography.Text>
          </Space>
          <div style={{ display: 'flex', gap: 2, height: 12, width: '100%' }}>
            {totalBytes > 0 ? (
              cats
                .filter((c) => c.bytes > 0)
                .map((c) => (
                  <div
                    key={c.key}
                    title={`${t(`storage.cat.${c.key}`)} ${fmtBytes(c.bytes)}`}
                    style={{ flex: `${Math.max(2, (c.bytes / totalBytes) * 100)} 0 0`, background: catColor(c.key), borderRadius: 3, minWidth: 6 }}
                  />
                ))
            ) : (
              <div style={{ flex: 1, background: token.colorFillSecondary, borderRadius: 3 }} />
            )}
          </div>
          <Space wrap size={16}>
            {cats.map((c) => (
              <span key={c.key} style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                <span style={{ width: 10, height: 10, borderRadius: 3, background: catColor(c.key), display: 'inline-block' }} />
                <Typography.Text>{t(`storage.cat.${c.key}`)}</Typography.Text>
                <Typography.Text type="secondary">{fmtBytes(c.bytes)}</Typography.Text>
              </span>
            ))}
          </Space>

          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))', gap: 12 }}>{cats.map(renderCard)}</div>
        </Space>
      </Card>

      <Card title={t('storage.title')}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12, width: '100%' }}>
          <Divider style={{ margin: '4px 0' }} orientation="left" orientationMargin={0} plain>
            {t('storage.scheduleTitle')}
          </Divider>
          <Typography.Text type="secondary">{t('storage.scheduleHint')}</Typography.Text>
          {row(t('storage.freq'), <Select style={{ width: 160 }} value={freq} onChange={(v) => setFreq(v)} options={freqOptions} />)}
          {freq !== 'off' && (
            <Space wrap>
              <span style={{ display: 'inline-block', minWidth: 120 }}>{t('storage.time')}</span>
              <input type="time" value={time} onChange={(e) => setTime(e.target.value || '03:00')} aria-label={t('storage.time')} style={{ height: 32, padding: '0 8px' }} />
            </Space>
          )}
          {freq === 'weekly' && row(t('storage.weekday'), <Select style={{ width: 160 }} value={weekday} onChange={setWeekday} options={weekdayOptions} />)}
          {freq === 'monthly' && row(t('storage.monthday'), <InputNumber min={1} max={31} value={monthday} onChange={(v) => setMonthday(v ?? 1)} />)}

          <Divider style={{ margin: '4px 0' }} orientation="left" orientationMargin={0} plain>
            {t('storage.targetsTitle')}
          </Divider>
          {row(
            t('storage.batchTarget'),
            <Space>
              <Switch checked={batchEnabled} onChange={setBatchEnabled} />
              <InputNumber min={batchFloor} value={batchDays} onChange={(v) => setBatchDays(v ?? batchFloor)} addonAfter={t('batch.admin.days')} />
            </Space>,
            t('storage.batchHint'),
          )}
          {row(
            t('storage.tokensTarget'),
            <Space>
              <Switch checked={tokensEnabled} onChange={setTokensEnabled} />
              <InputNumber min={0} value={tokensGraceDays} onChange={(v) => setTokensGraceDays(v ?? 0)} addonAfter={t('batch.admin.days')} />
            </Space>,
            t('storage.tokensHint'),
          )}

          <Divider style={{ margin: '4px 0' }} orientation="left" orientationMargin={0} plain>
            {t('storage.reportsTarget')}
          </Divider>
          <Alert type="warning" showIcon message={t('storage.reportsDanger')} description={t('storage.reportsWarn')} />
          {row(
            t('storage.enable'),
            <Space>
              <Switch checked={reportsEnabled} onChange={onToggleReports} />
              <InputNumber min={reportsFloor} value={reportsDays} onChange={(v) => setReportsDays(v ?? reportsFloor)} addonAfter={t('batch.admin.days')} />
            </Space>,
            t('storage.floorHint', { n: reportsFloor }),
          )}

          <StickyActionBar>
            <Button type="primary" onClick={save}>
              {t('common.save')}
            </Button>
          </StickyActionBar>
        </div>
      </Card>

      <Card title={t('storage.historyTitle')}>
        <div style={{ overflowX: 'auto' }}>
          <Table rowKey="id" size="small" pagination={false} dataSource={history} columns={histCols} locale={{ emptyText: t('storage.never') }} />
        </div>
      </Card>
    </div>
  )
}
