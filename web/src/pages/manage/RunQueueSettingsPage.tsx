import { useEffect, useState } from 'react'
import { App, Button, Card, Divider, Input, InputNumber, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { BatchConfig } from '../../api/types'

// Standalone run-queue settings (docs/adr/0007 + 0008): the queue concurrency budget,
// the default base priority, the Dify end-user template, and the Slurm-style multifactor
// priority weights. These govern the whole run system (single run + CSV batch). The
// urgent lane / ticket settings live with the group config (Manage -> Users -> Groups),
// not here — urgent is a group/ticket concern.
export default function RunQueueSettingsPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [maxJobs, setMaxJobs] = useState(1)
  const [difyEndUser, setDifyEndUser] = useState('')
  const [difyPollSeconds, setDifyPollSeconds] = useState(0)
  const [difyRunTimeout, setDifyRunTimeout] = useState(180)
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
        setMaxJobs(r.max_jobs)
        setDifyEndUser(r.dify_end_user ?? '')
        setDifyPollSeconds(r.dify_poll_seconds ?? 0)
        setDifyRunTimeout(r.dify_run_timeout_minutes ?? 180)
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
      max_jobs: maxJobs,
      dify_end_user: difyEndUser,
      dify_poll_seconds: difyPollSeconds,
      dify_run_timeout_minutes: difyRunTimeout,
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
          t('batch.admin.defaultPriority'),
          t('batch.admin.defaultPriorityHint'),
          <InputNumber min={0} max={100} value={defaultPriority} onChange={(v) => setDefaultPriority(v ?? 50)} />,
        )}

        <Divider style={{ margin: '4px 0' }} orientation="left" orientationMargin={0} plain>
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
        {row(
          t('batch.admin.difyPoll'),
          t('batch.admin.difyPollHint'),
          <InputNumber min={0} max={600} value={difyPollSeconds} onChange={(v) => setDifyPollSeconds(v ?? 0)} addonAfter={t('batch.admin.seconds')} />,
        )}
        {row(
          t('batch.admin.difyRunTimeout'),
          t('batch.admin.difyRunTimeoutHint'),
          <InputNumber min={1} max={720} value={difyRunTimeout} onChange={(v) => setDifyRunTimeout(v || 180)} addonAfter={t('batch.admin.minutes')} />,
        )}

        <Divider style={{ margin: '4px 0' }} orientation="left" orientationMargin={0} plain>
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
