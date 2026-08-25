import { useEffect, useMemo, useState } from 'react'
import { App, Button, Card, InputNumber, Select, Space, Switch, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { api, errText } from '../../api/client'
import { visibleOn } from '../../lib/batchUi'
import { presetSummary } from '../../lib/runSchedule'
import type { BatchConfig, BatchTarget, RunMode, RunPreset, RunPresetsResp } from '../../api/types'
import LoadGate from '../../components/LoadGate'
import StickyActionBar from '../../components/StickyActionBar'
import { GAP_FIELD } from './tokens'

// The run form's defaults, on their own page (docs/adr/0014 §4): what 运行分析 opens on before the
// user touches anything — which workflow, which mode button, which preset window, whether 队列空闲
// is pre-checked, how many failure retries, and whether the done-email is pre-ticked.
//
// These started as two rows on the run/queue settings page, next to the queue's own concurrency
// and priority knobs. They are a different kind of setting: nothing here governs how the queue
// behaves, it only decides what the dialog is already holding when it opens, and every one of them
// is a suggestion the user can override in the form. Hence a page of their own — and hence the
// "no default" option on every picker, which is what an unconfigured portal does and stays a
// first-class choice rather than an unset field.
export default function RunDefaultsPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [targetId, setTargetId] = useState(0) // 0 = no default workflow
  const [mode, setMode] = useState<RunMode>('now')
  const [presetId, setPresetId] = useState(0) // 0 = no default window
  const [idle, setIdle] = useState(false)
  const [retries, setRetries] = useState(0)
  const [notify, setNotify] = useState(false)
  const [showRule, setShowRule] = useState(false) // print a window's whole rule in the run form
  const [targets, setTargets] = useState<BatchTarget[]>([])
  const [presets, setPresets] = useState<RunPreset[]>([])
  // Same rule as the other settings pages: until the server has answered, the numbers above are
  // this file's opinion and not the portal's configuration, so nothing renders behind the gate.
  // The two lists are part of the gate too — a workflow picker that opens empty and fills in a
  // moment later would show "no default" for a portal that has one.
  const [loading, setLoading] = useState(true)
  const [loadErr, setLoadErr] = useState('')

  const load = () => {
    setLoading(true)
    setLoadErr('')
    return Promise.all([
      api.get<BatchConfig>('/api/admin/batch/config'),
      api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets'),
      api.get<RunPresetsResp>('/api/admin/batch/presets'),
    ])
      .then(([cfg, tg, ps]) => {
        setTargetId(cfg.run_default_target_id ?? 0)
        setMode(cfg.run_default_mode ?? 'now')
        setPresetId(cfg.run_default_preset_id ?? 0)
        setIdle(!!cfg.run_default_idle)
        setRetries(cfg.run_default_retries ?? 0)
        setNotify(!!cfg.run_default_notify)
        setShowRule(!!cfg.run_show_preset_rule)
        setTargets(tg.targets || [])
        setPresets(ps.presets || [])
      })
      .catch((e) => setLoadErr(errText(e, t)))
      .finally(() => setLoading(false))
  }
  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const save = async () => {
    await api.post('/api/admin/batch/config', {
      run_default_target_id: targetId,
      run_default_mode: mode,
      run_default_preset_id: presetId,
      run_default_idle: idle,
      run_default_retries: retries,
      run_default_notify: notify,
      run_show_preset_rule: showRule,
    })
    message.success(t('common.saved'))
    load()
  }

  // Only a workflow 运行分析 can actually offer is worth defaulting to. A saved default that has
  // since been hidden from that surface is still listed — marked as hidden — rather than silently
  // dropped from its own picker, so an admin sees the choice they made and can keep or clear it.
  const runnable = useMemo(() => visibleOn(targets, 'run'), [targets])
  const targetOptions = useMemo(() => {
    const opts = runnable.map((tg) => ({ value: tg.id, label: tg.name }))
    const saved = targetId && !runnable.some((tg) => tg.id === targetId) ? targets.find((tg) => tg.id === targetId) : undefined
    if (saved) opts.push({ value: saved.id, label: `${saved.name}（${t('runDefaults.hiddenOnRun')}）` })
    return [{ value: 0, label: t('runDefaults.none') }, ...opts]
  }, [runnable, targets, targetId, t])

  // Disabled windows stay in the list, marked: the run form won't offer one, but an admin who has
  // switched a window off for now should still see it waiting here instead of losing the setting.
  const presetOptions = useMemo(
    () => [
      { value: 0, label: t('runDefaults.none') },
      ...presets.map((p) => ({
        value: p.id,
        label: `${p.label} · ${presetSummary(p, t)}${p.enabled ? '' : `（${t('runDefaults.presetOff')}）`}`,
      })),
    ],
    [presets, t],
  )

  const row = (label: string, hint: React.ReactNode, control: React.ReactNode) => (
    <Space wrap>
      <span style={{ display: 'inline-block', minWidth: 120 }}>{label}</span>
      {control}
      <Typography.Text type="secondary">{hint}</Typography.Text>
    </Space>
  )

  return (
    <Card title={t('runDefaults.title')}>
      <LoadGate loading={loading} error={loadErr} onRetry={load}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: GAP_FIELD, width: '100%' }}>
          <Typography.Text type="secondary">{t('runDefaults.hint')}</Typography.Text>

          {row(
            t('runDefaults.workflow'),
            t('runDefaults.workflowHint'),
            <Select style={{ minWidth: 260 }} value={targetId} onChange={setTargetId} options={targetOptions} />,
          )}
          {row(
            t('runDefaults.mode'),
            t('runDefaults.modeHint'),
            <Select
              value={mode}
              onChange={(v) => setMode(v as RunMode)}
              style={{ width: 140 }}
              options={[
                { value: 'now', label: t('run.now') },
                { value: 'preset', label: t('run.preset') },
                { value: 'scheduled', label: t('run.scheduled') },
              ]}
            />,
          )}
          {row(
            t('runDefaults.preset'),
            // A portal with no windows configured yet gets the way to make one instead of a hint
            // about a picker that can only say "no default".
            presets.length === 0 ? (
              <>
                {t('runDefaults.noPresets')} <Link to="/manage/runqueue">{t('runDefaults.managePresets')}</Link>
              </>
            ) : (
              t('runDefaults.presetHint')
            ),
            <Select
              style={{ minWidth: 260 }}
              value={presetId}
              onChange={setPresetId}
              disabled={presets.length === 0}
              options={presetOptions}
            />,
          )}
          {row(
            t('runDefaults.showRule'),
            t('runDefaults.showRuleHint'),
            <Switch checked={showRule} onChange={setShowRule} disabled={presets.length === 0} />,
          )}
          {row(t('runDefaults.idle'), t('runDefaults.idleHint'), <Switch checked={idle} onChange={setIdle} />)}
          {row(
            t('runDefaults.retries'),
            t('runDefaults.retriesHint'),
            <InputNumber min={0} max={5} value={retries} onChange={(v) => setRetries(v ?? 0)} />,
          )}
          {row(t('runDefaults.notify'), t('runDefaults.notifyHint'), <Switch checked={notify} onChange={setNotify} />)}

          <StickyActionBar>
            <Button type="primary" onClick={save}>
              {t('common.save')}
            </Button>
          </StickyActionBar>
        </div>
      </LoadGate>
    </Card>
  )
}
