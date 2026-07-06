import { useEffect, useMemo, useState } from 'react'
import { Alert, App, Checkbox, DatePicker, Form, Input, Modal, Radio, Select, Space, Tag, Typography } from 'antd'
import { ThunderboltOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import type { Dayjs } from 'dayjs'
import { api } from '../api/client'
import type { BatchQueueSummary, BatchTarget, BatchTickets } from '../api/types'

// The home-page "运行分析" modal (docs/adr/0007-run-analysis-and-scheduling.md):
// pick a Dify workflow, fill its discovered inputs, run now or schedule, optionally
// escalate to 加急 (ticket-gated), with the live queue depth shown inline.
export default function RunAnalysisModal({
  open,
  onClose,
  onSubmitted,
}: {
  open: boolean
  onClose: () => void
  onSubmitted?: (jobId: number) => void
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [form] = Form.useForm()
  const [targets, setTargets] = useState<BatchTarget[]>([])
  const [targetId, setTargetId] = useState<number | undefined>()
  const [tickets, setTickets] = useState<BatchTickets | null>(null)
  const [queue, setQueue] = useState<BatchQueueSummary | null>(null)
  const [mode, setMode] = useState<'now' | 'scheduled'>('now')
  const [runAt, setRunAt] = useState<Dayjs | null>(null)
  const [urgent, setUrgent] = useState(false)
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    if (!open) return
    api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets').then((r) => setTargets(r.targets || [])).catch(() => {})
    api.get<BatchTickets>('/api/admin/batch/tickets').then(setTickets).catch(() => {})
    api.get<BatchQueueSummary>('/api/admin/batch/queue').then(setQueue).catch(() => {})
  }, [open])

  const target = useMemo(() => targets.find((tg) => tg.id === targetId), [targets, targetId])
  const inputs = target?.inputs || []

  const urgentEnabled = tickets?.urgent_enabled !== false
  // 加急 needs a ticket unless the caller is unlimited (admin); disable + uncheck at 0.
  const urgentDisabled = urgentEnabled && tickets != null && !tickets.unlimited && (tickets.remaining ?? 0) <= 0
  useEffect(() => {
    if (!urgentEnabled || urgentDisabled) setUrgent(false)
  }, [urgentEnabled, urgentDisabled])

  const pickTarget = (id: number) => {
    setTargetId(id)
    form.resetFields()
  }

  const reset = () => {
    setTargetId(undefined)
    setUrgent(false)
    setMode('now')
    setRunAt(null)
    form.resetFields()
  }

  const submit = async () => {
    if (!targetId) {
      message.error(t('run.selectWorkflow'))
      return
    }
    let vals: Record<string, unknown>
    try {
      vals = await form.validateFields()
    } catch {
      return
    }
    if (mode === 'scheduled' && !runAt) {
      message.error(t('run.pickTime'))
      return
    }
    setSubmitting(true)
    try {
      const row: Record<string, string> = {}
      inputs.forEach((i) => {
        row[i.key] = String(vals[i.key] ?? '').trim()
      })
      const res = await api.post<{ job_id: number; downgraded?: boolean; run_at?: string }>('/api/admin/batch/jobs', {
        target_id: targetId,
        concurrency: 1,
        max_retries: 2,
        priority: urgent ? 'urgent' : '', // "" → backend resolves group/system default
        run_at: mode === 'scheduled' && runAt ? runAt.format('YYYY-MM-DD HH:mm:ss') : '',
        rows: [row],
      })
      if (res.run_at) message.success(t('run.scheduledOk', { at: res.run_at }))
      else message.success(t('run.startedOk', { id: res.job_id }))
      if (res.downgraded) message.warning(t('batch.ticketDowngraded'))
      onSubmitted?.(res.job_id)
      reset()
      onClose()
    } catch (e) {
      message.error((e as Error).message || t('run.startFailed'))
    } finally {
      setSubmitting(false)
    }
  }

  const waiting = queue?.waiting ?? 0

  return (
    <Modal
      title={
        <Space>
          <ThunderboltOutlined />
          {t('run.title')}
        </Space>
      }
      open={open}
      onOk={submit}
      okText={mode === 'scheduled' ? t('run.schedule') : t('run.run')}
      okButtonProps={{ loading: submitting, disabled: !targetId }}
      cancelText={t('common.cancel')}
      onCancel={onClose}
      destroyOnClose
    >
      <Space direction="vertical" size={14} style={{ width: '100%' }}>
        {targets.length === 0 && <Alert type="info" showIcon message={t('run.noTargets')} />}

        <div>
          <Typography.Text type="secondary">{t('run.workflow')}</Typography.Text>
          <Select
            style={{ width: '100%', marginTop: 4 }}
            placeholder={t('run.selectWorkflow')}
            value={targetId}
            onChange={pickTarget}
            options={targets.map((tg) => ({ value: tg.id, label: tg.name }))}
          />
        </div>

        {target && (
          <Form form={form} layout="vertical" requiredMark style={{ marginBottom: -8 }}>
            {inputs.map((i) => (
              <Form.Item
                key={i.key}
                name={i.key}
                label={i.label || i.key}
                rules={i.required ? [{ required: true, message: t('run.required', { field: i.label || i.key }) }] : []}
              >
                <Input placeholder={i.label || i.key} />
              </Form.Item>
            ))}
            {inputs.length === 0 && <Typography.Text type="secondary">{t('run.noInputs')}</Typography.Text>}
          </Form>
        )}

        <div>
          <Radio.Group value={mode} onChange={(e) => setMode(e.target.value)} optionType="button" buttonStyle="solid">
            <Radio.Button value="now">{t('run.now')}</Radio.Button>
            <Radio.Button value="scheduled">{t('run.scheduled')}</Radio.Button>
          </Radio.Group>
          {mode === 'scheduled' && (
            <DatePicker
              showTime
              style={{ marginLeft: 10 }}
              value={runAt}
              onChange={setRunAt}
              format="YYYY-MM-DD HH:mm:ss"
              placeholder={t('run.pickTime')}
            />
          )}
        </div>

        {urgentEnabled && (
          <div>
            <Checkbox checked={urgent} disabled={urgentDisabled} onChange={(e) => setUrgent(e.target.checked)}>
              {t('run.urgent')}
            </Checkbox>
            {tickets && !tickets.unlimited && (
              <Tag style={{ marginLeft: 8 }} color={(tickets.remaining ?? 0) > 0 ? 'gold' : 'default'} icon={<ThunderboltOutlined />}>
                {t('batch.ticketsLeft', { n: tickets.remaining ?? 0, total: tickets.allocation ?? 0 })}
              </Tag>
            )}
            {urgentDisabled && (
              <Typography.Text type="secondary" style={{ marginLeft: 8, fontSize: 12 }}>
                {t('run.noTickets')}
              </Typography.Text>
            )}
          </div>
        )}

        <Alert
          type={waiting > 0 ? 'warning' : 'success'}
          showIcon
          message={waiting > 0 ? t('run.queueWaiting', { n: waiting }) : t('run.queueEmpty')}
        />
      </Space>
    </Modal>
  )
}
