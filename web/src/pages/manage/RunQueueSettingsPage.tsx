import { useEffect, useState } from 'react'
import { App, Button, Card, Divider, Input, InputNumber, Space, Switch, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { BatchConfig } from '../../api/types'

// Standalone 运行/队列 settings (docs/adr/0007 + 0008): the queue budget, reserved
// slots, 加急 ticket period, max concurrency, the no-group default base priority, and
// the Slurm-style multifactor priority weights. These govern the whole run system
// (home 单次运行 + CSV 批量), so they live apart from the 批量任务 tab (targets + CSV).
export default function RunQueueSettingsPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [maxConcurrency, setMaxConcurrency] = useState(10)
  const [maxJobs, setMaxJobs] = useState(1)
  const [reservedSlots, setReservedSlots] = useState(1)
  const [ticketPeriod, setTicketPeriod] = useState(7)
  const [urgentEnabled, setUrgentEnabled] = useState(false)
  const [difyEndUser, setDifyEndUser] = useState('')
  const [defaultPriority, setDefaultPriority] = useState(50)
  const [wBase, setWBase] = useState(1000)
  const [wAge, setWAge] = useState(1000)
  const [wFair, setWFair] = useState(1000)
  const [ageHours, setAgeHours] = useState(24)
  const [fairHalflife, setFairHalflife] = useState(168)

  const load = () =>
    api
      .get<BatchConfig>('/api/admin/batch/config')
      .then((r) => {
        setMaxConcurrency(r.max_concurrency)
        setMaxJobs(r.max_jobs)
        setReservedSlots(r.reserved_slots)
        setTicketPeriod(r.ticket_period_days)
        setUrgentEnabled(!!r.urgent_enabled)
        setDifyEndUser(r.dify_end_user ?? '')
        setDefaultPriority(r.default_priority ?? 50)
        setWBase(r.prio_w_base)
        setWAge(r.prio_w_age)
        setWFair(r.prio_w_fair)
        setAgeHours(r.prio_age_hours)
        setFairHalflife(r.prio_fair_halflife_hours)
      })
  useEffect(() => {
    load()
  }, [])

  const save = async () => {
    await api.post('/api/admin/batch/config', {
      max_concurrency: maxConcurrency,
      max_jobs: maxJobs,
      reserved_slots: reservedSlots,
      ticket_period_days: ticketPeriod,
      urgent_enabled: urgentEnabled,
      dify_end_user: difyEndUser,
      default_priority: String(defaultPriority),
      prio_w_base: wBase,
      prio_w_age: wAge,
      prio_w_fair: wFair,
      prio_age_hours: ageHours,
      prio_fair_halflife_hours: fairHalflife,
    })
    message.success(t('common.saved'))
    load()
  }

  const row = (label: string, hint: string, control: React.ReactNode) => (
    <Space wrap>
      <span style={{ display: 'inline-block', minWidth: 96 }}>{label}</span>
      {control}
      <Typography.Text type="secondary">{hint}</Typography.Text>
    </Space>
  )

  return (
    <Card title={t('batch.admin.settings')}>
      <Space direction="vertical" size={12} style={{ width: '100%' }}>
        {row(
          t('batch.admin.maxJobs'),
          t('batch.admin.maxJobsHint'),
          <InputNumber min={1} max={50} value={maxJobs} onChange={(v) => setMaxJobs(v || 1)} />,
        )}
        {row(
          t('batch.admin.reservedSlots'),
          t('batch.admin.reservedSlotsHint'),
          <InputNumber min={0} max={Math.max(0, maxJobs - 1)} value={reservedSlots} onChange={(v) => setReservedSlots(v ?? 0)} />,
        )}
        {row(
          t('batch.admin.defaultPriority'),
          t('batch.admin.defaultPriorityHint'),
          <InputNumber min={0} max={100} value={defaultPriority} onChange={(v) => setDefaultPriority(v ?? 50)} />,
        )}
        {row(
          t('batch.admin.ticketPeriod'),
          t('batch.admin.ticketPeriodHint'),
          <InputNumber min={1} max={365} value={ticketPeriod} onChange={(v) => setTicketPeriod(v || 7)} addonAfter={t('batch.admin.days')} />,
        )}
        {row(
          t('batch.admin.maxConcurrency'),
          t('batch.admin.maxConcurrencyHint'),
          <InputNumber min={1} max={100} value={maxConcurrency} onChange={(v) => setMaxConcurrency(v || 1)} />,
        )}
        {row(
          t('batch.admin.urgentEnabled'),
          t('batch.admin.urgentEnabledHint'),
          <Switch checked={urgentEnabled} onChange={setUrgentEnabled} />,
        )}

        <Divider style={{ margin: '4px 0' }} orientation="left" plain>
          {t('batch.admin.difyTitle')}
        </Divider>
        {row(
          t('batch.admin.difyEndUser'),
          t('batch.admin.difyEndUserHint'),
          <Input
            style={{ width: 280 }}
            value={difyEndUser}
            placeholder="report-portal"
            onChange={(e) => setDifyEndUser(e.target.value)}
          />,
        )}

        <Divider style={{ margin: '4px 0' }} orientation="left" plain>
          {t('batch.admin.prioWeightsTitle')}
        </Divider>
        <Typography.Text type="secondary">{t('batch.admin.prioWeightsHint')}</Typography.Text>
        {row(
          t('batch.admin.wBase'),
          t('batch.admin.wBaseHint'),
          <InputNumber min={0} step={100} value={wBase} onChange={(v) => setWBase(v ?? 0)} />,
        )}
        {row(
          t('batch.admin.wAge'),
          t('batch.admin.wAgeHint'),
          <InputNumber min={0} step={100} value={wAge} onChange={(v) => setWAge(v ?? 0)} />,
        )}
        {row(
          t('batch.admin.wFair'),
          t('batch.admin.wFairHint'),
          <InputNumber min={0} step={100} value={wFair} onChange={(v) => setWFair(v ?? 0)} />,
        )}
        {row(
          t('batch.admin.ageHours'),
          t('batch.admin.ageHoursHint'),
          <InputNumber min={1} max={8760} value={ageHours} onChange={(v) => setAgeHours(v || 24)} addonAfter={t('batch.admin.hours')} />,
        )}
        {row(
          t('batch.admin.fairHalflife'),
          t('batch.admin.fairHalflifeHint'),
          <InputNumber min={1} max={8760} value={fairHalflife} onChange={(v) => setFairHalflife(v || 168)} addonAfter={t('batch.admin.hours')} />,
        )}

        <Button type="primary" onClick={save}>
          {t('common.save')}
        </Button>
      </Space>
    </Card>
  )
}
