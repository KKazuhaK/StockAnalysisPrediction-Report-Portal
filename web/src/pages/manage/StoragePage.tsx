import { useEffect, useState } from 'react'
import { Alert, App, Button, Card, Divider, InputNumber, Popconfirm, Select, Space, Switch, Table, Tag, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { CleanupConfig, CleanupResult, CleanupRun, CleanupUsage, CleanupUsageCategory } from '../../api/types'
import StickyActionBar from '../../components/StickyActionBar'

// Storage management console (docs/adr/0017-storage-cleanup.md): a per-category usage view, manual
// (preview → confirm) cleanup, an optional daily/weekly/monthly scheduled retention pass, and the
// cleanup_runs audit history. Every target ships disabled; reports (core content) are fail-closed,
// floored, and guarded by a live-count confirmation before an admin arms or runs them.

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

export default function StoragePage() {
  const { t } = useTranslation()
  const { message, modal } = App.useApp()

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
  const [lastResult, setLastResult] = useState<CleanupResult | null>(null)
  const [usage, setUsage] = useState<CleanupUsage | null>(null)
  const [history, setHistory] = useState<CleanupRun[]>([])

  const loadConfig = () =>
    api.get<CleanupConfig>('/api/admin/cleanup/config').then((r) => {
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
      setLastResult(r.last_result)
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

  const previewLine = (r: CleanupResult) => t('storage.wouldDelete', { batch: r.batch, reports: r.reports, tokens: r.tokens })

  const doPreview = async (targets: string[]) => {
    const r = await api.post<CleanupResult>('/api/admin/cleanup/preview', { targets })
    modal.info({ title: t('storage.previewResult'), content: previewLine(r) })
  }

  const doRun = async (targets: string[]) => {
    const r = await api.post<CleanupResult>('/api/admin/cleanup/run', { targets })
    message.success(t('storage.cleaned', { batch: r.batch, reports: r.reports, tokens: r.tokens }))
    loadUsage()
    loadHistory()
    loadConfig()
  }

  // Enabling reports auto-delete (or running it manually) first previews the live count so the
  // admin sees exactly how many core-content rows are at stake before arming/executing it.
  const guardReports = async (onConfirm: () => void) => {
    const r = await api.post<CleanupResult>('/api/admin/cleanup/preview', { targets: ['reports'] })
    modal.confirm({
      title: t('storage.confirmReportsTitle'),
      content: t('storage.confirmReportsBody', { count: r.reports }),
      okText: t('common.confirm'),
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

  const usageCols = [
    { title: t('storage.colCategory'), dataIndex: 'key', render: (k: string) => t(`storage.cat.${k}`) },
    { title: t('storage.colRows'), dataIndex: 'rows', align: 'right' as const },
    { title: t('storage.colSize'), dataIndex: 'bytes', align: 'right' as const, render: (b: number) => fmtBytes(b) },
    { title: t('storage.colEligible'), dataIndex: 'eligible', align: 'right' as const, render: (n: number) => (n > 0 ? <Tag color="orange">{n}</Tag> : n) },
    { title: t('storage.colOldest'), dataIndex: 'oldest', render: (v: string) => v || '—' },
    {
      title: t('storage.colActions'),
      key: 'actions',
      render: (_: unknown, row: CleanupUsageCategory) => {
        if (row.key === 'chat') return <Typography.Text type="secondary">—</Typography.Text>
        return (
          <Space>
            <Button size="small" onClick={() => doPreview([row.key])}>
              {t('storage.preview')}
            </Button>
            {row.key === 'reports' ? (
              <Button size="small" danger onClick={() => guardReports(() => doRun(['reports']))}>
                {t('storage.cleanNow')}
              </Button>
            ) : (
              <Popconfirm title={t('storage.confirmClean')} okText={t('common.confirm')} cancelText={t('common.cancel')} onConfirm={() => doRun([row.key])}>
                <Button size="small">{t('storage.cleanNow')}</Button>
              </Popconfirm>
            )}
          </Space>
        )
      },
    },
  ]

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
        <Space direction="vertical" size={8} style={{ width: '100%' }}>
          <Typography.Text type="secondary">
            {t('storage.dbSize')}: {fmtBytes(usage?.db_bytes ?? 0)} · {t('storage.approxNote')}
          </Typography.Text>
          <div style={{ overflowX: 'auto' }}>
            <Table
              rowKey="key"
              size="small"
              pagination={false}
              dataSource={usage?.categories ?? []}
              columns={usageCols}
            />
          </div>
        </Space>
      </Card>

      <Card title={t('storage.title')}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12, width: '100%' }}>
          <Divider style={{ margin: '4px 0' }} orientation="left" orientationMargin={0} plain>
            {t('storage.scheduleTitle')}
          </Divider>
          <Typography.Text type="secondary">{t('storage.scheduleHint')}</Typography.Text>
          {row(
            t('storage.freq'),
            <Select style={{ width: 160 }} value={freq} onChange={(v) => setFreq(v)} options={freqOptions} />,
          )}
          {freq !== 'off' && (
            <Space wrap>
              <span style={{ display: 'inline-block', minWidth: 120 }}>{t('storage.time')}</span>
              <input
                type="time"
                value={time}
                onChange={(e) => setTime(e.target.value || '03:00')}
                aria-label={t('storage.time')}
                style={{ height: 32, padding: '0 8px' }}
              />
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

          {lastResult && (
            <Typography.Text type="secondary">
              {t('storage.lastRun')}: {lastResult.at} · {t('storage.resultLine', { batch: lastResult.batch, tokens: lastResult.tokens, reports: lastResult.reports })}
            </Typography.Text>
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
