import { useEffect, useState } from 'react'
import { App, Button, Card, InputNumber, Select, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'

// Standalone 运行/队列 settings (docs/adr/0007): the queue budget, reserved slots,
// 加急 ticket period, max concurrency, and the no-group default priority. These
// govern the whole run system (home 单次运行 + CSV 批量), so they live apart from
// the 批量任务 tab (which is just targets + CSV).
export default function RunQueueSettingsPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [maxConcurrency, setMaxConcurrency] = useState(10)
  const [maxJobs, setMaxJobs] = useState(1)
  const [reservedSlots, setReservedSlots] = useState(1)
  const [ticketPeriod, setTicketPeriod] = useState(7)
  const [defaultPriority, setDefaultPriority] = useState('normal')

  const load = () =>
    api
      .get<{
        max_concurrency: number
        max_jobs: number
        reserved_slots: number
        ticket_period_days: number
        default_priority: string
      }>('/api/admin/batch/config')
      .then((r) => {
        setMaxConcurrency(r.max_concurrency)
        setMaxJobs(r.max_jobs)
        setReservedSlots(r.reserved_slots)
        setTicketPeriod(r.ticket_period_days)
        setDefaultPriority(r.default_priority || 'normal')
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
      default_priority: defaultPriority,
    })
    message.success(t('common.saved'))
    load()
  }

  return (
    <Card title={t('batch.admin.settings')}>
      <Space direction="vertical" size={12} style={{ width: '100%' }}>
        <Space wrap>
          <span>{t('batch.admin.maxJobs')}</span>
          <InputNumber min={1} max={50} value={maxJobs} onChange={(v) => setMaxJobs(v || 1)} />
          <Typography.Text type="secondary">{t('batch.admin.maxJobsHint')}</Typography.Text>
        </Space>
        <Space wrap>
          <span>{t('batch.admin.reservedSlots')}</span>
          <InputNumber min={0} max={Math.max(0, maxJobs - 1)} value={reservedSlots} onChange={(v) => setReservedSlots(v ?? 0)} />
          <Typography.Text type="secondary">{t('batch.admin.reservedSlotsHint')}</Typography.Text>
        </Space>
        <Space wrap>
          <span>{t('batch.admin.defaultPriority')}</span>
          <Select
            value={defaultPriority}
            onChange={setDefaultPriority}
            style={{ width: 140 }}
            options={[
              { value: 'normal', label: t('batch.priority.normal') },
              { value: 'other', label: t('batch.priority.other') },
            ]}
          />
          <Typography.Text type="secondary">{t('batch.admin.defaultPriorityHint')}</Typography.Text>
        </Space>
        <Space wrap>
          <span>{t('batch.admin.ticketPeriod')}</span>
          <InputNumber min={1} max={365} value={ticketPeriod} onChange={(v) => setTicketPeriod(v || 7)} addonAfter={t('batch.admin.days')} />
          <Typography.Text type="secondary">{t('batch.admin.ticketPeriodHint')}</Typography.Text>
        </Space>
        <Space wrap>
          <span>{t('batch.admin.maxConcurrency')}</span>
          <InputNumber min={1} max={100} value={maxConcurrency} onChange={(v) => setMaxConcurrency(v || 1)} />
          <Typography.Text type="secondary">{t('batch.admin.maxConcurrencyHint')}</Typography.Text>
        </Space>
        <Button type="primary" onClick={save}>
          {t('common.save')}
        </Button>
      </Space>
    </Card>
  )
}
