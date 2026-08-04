import { useCallback, useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { Alert, App, Button, Divider, Input, InputNumber, Select, Space, Switch, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import type { GeoStatus, GeoUpdateState } from '../../api/types'

/**
 * A source is described by what it needs, not by what it is called: the form asks for an edition
 * and a credential only where they mean something. DB-IP publishes one free database and takes no
 * account, so showing it a disabled "edition" box and a key box is three controls of noise.
 */
const GEO_SOURCES = [
  { value: 'maxmind', edition: 'GeoLite2-City', key: true, edit: true, label: 'audit.geoSrcMaxmind', keyLabel: 'audit.geoKeyLabelMaxmind' },
  { value: 'dbip', edition: '', key: false, edit: false, label: 'audit.geoSrcDbip', keyLabel: '' },
  { value: 'ipinfo', edition: 'ipinfo_lite', key: true, edit: true, label: 'audit.geoSrcIpinfo', keyLabel: 'audit.geoKeyLabelIpinfo' },
  { value: 'custom', edition: '', key: false, edit: false, label: 'audit.geoSrcCustom', keyLabel: '' },
] as const

type TFn = (key: string, vars?: Record<string, unknown>) => string

/**
 * What one installed database is, as the single line that labels its option in the picker.
 *
 * Exported so it can be tested directly: the option list is rendered by antd's virtual list, which
 * jsdom never lays out, and the formatting is the whole point of folding the old bullet list into
 * the picker.
 */
export function describeDatabase(f: { file: string; ok: boolean; info?: { granularity?: string; build_epoch?: number } }, t: TFn): string {
  if (!f.ok) return t('audit.geoUnreadable', { file: f.file })
  const g = f.info?.granularity
  const gran = g === 'city' ? t('audit.geoGranCity') : g === 'country' ? t('audit.geoGranCountry') : (g ?? '—')
  const built = f.info?.build_epoch
    ? ' · ' + t('audit.geoBuilt', { at: new Date(f.info.build_epoch * 1000).toISOString().slice(0, 10) })
    : ''
  return `${f.file} · ${gran}${built}`
}

/** A labelled control. Every box in this section says what it is above itself, because "a text box
 *  between a dropdown and a password field" is not a question anybody can answer. */
function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {label}
      </Typography.Text>
      {children}
      {hint ? (
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {hint}
        </Typography.Text>
      ) : null}
    </div>
  )
}

/**
 * The IP database: whether to resolve at all, which file to use, and where to get a new one.
 *
 * The credential is write-only. It is sent only when the admin types a new one, which is what lets
 * the field say "saved, unchanged" rather than either echoing the key back or wiping it on every
 * save — the rule the SMTP password and the SSO client secrets already follow.
 */
export default function GeoSection() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [geo, setGeo] = useState<GeoStatus | null>(null)
  const [upd, setUpd] = useState<GeoUpdateState | null>(null)
  const [busy, setBusy] = useState(false)
  const [d, setD] = useState({
    enabled: true,
    file: '',
    auto: false,
    auto_hours: 12,
    source: 'maxmind',
    edition: 'GeoLite2-City',
    url: '',
  })
  const [key, setKey] = useState<string | null>(null) // null = untouched, so it is not sent

  const poll = useCallback(() => {
    api
      .get<{ status: GeoStatus; update: GeoUpdateState }>('/api/admin/geoip')
      .then((r) => {
        // Defensive about the shape: this section renders inside a page that fetches other things,
        // and a response missing `status` must leave the section absent rather than throw inside a
        // promise and take the rest of the page's effects with it.
        if (!r?.status) return
        setUpd(r.update)
        setGeo(r.status)
        setD((p) => ({
          ...p,
          enabled: r.status.enabled,
          // "." is what an older build stored for "automatic"; show it as the automatic choice.
          file: r.status.pick && r.status.pick !== '.' ? r.status.pick : '',
          auto: r.update.auto,
          auto_hours: r.update.auto_hours || 12,
          source: r.update.source || 'maxmind',
          edition: r.update.edition ?? p.edition,
          url: r.update.url ?? '',
        }))
      })
      .catch(() => {})
  }, [])
  useEffect(poll, [poll])

  // Only while a download runs, and it stops on its own: a page left open must not poll for ever.
  useEffect(() => {
    if (!upd?.updating) return
    const id = setInterval(() => {
      api
        .get<{ status: GeoStatus; update: GeoUpdateState }>('/api/admin/geoip')
        .then((r) => {
          setUpd(r.update)
          setGeo(r.status)
        })
        .catch(() => {})
    }, 3000)
    return () => clearInterval(id)
  }, [upd?.updating])

  const body = () => ({ ...d, ...(key === null ? {} : { token: key }) })

  const save = async () => {
    setBusy(true)
    try {
      await api.post('/api/admin/geoip', body())
      setKey(null)
      message.success(t('common.saved'))
      poll()
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setBusy(false)
    }
  }

  // Save first, then download — otherwise "Update now" after editing the key would fetch with the
  // old one and report a failure the admin has already fixed on screen.
  const update = async () => {
    setBusy(true)
    try {
      await api.post('/api/admin/geoip', body())
      setKey(null)
      await api.post('/api/admin/geoip/update', {})
      poll()
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setBusy(false)
    }
  }

  const src = GEO_SOURCES.find((x) => x.value === d.source)

  if (!geo) return null
  return (
    <>
      <Divider titlePlacement="left">{t('audit.geoTitle')}</Divider>
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Space align="start">
          <Switch checked={d.enabled} onChange={(v) => setD({ ...d, enabled: v })} />
          <div>
            <Typography.Text strong style={{ display: 'block' }}>
              {t('audit.geoShow')}
            </Typography.Text>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t('audit.geoOfflineHint', { dir: geo.dir })}
            </Typography.Text>
          </div>
        </Space>

        {/* One list, not two: what each installed database is belongs in the option that selects it,
            not in a bullet list repeating the same filenames underneath. */}
        <Field label={t('audit.geoActive')} hint={t('audit.geoActiveHint')}>
          <Select
            style={{ width: 420, maxWidth: '100%' }}
            value={d.file}
            onChange={(v) => setD({ ...d, file: v })}
            options={[
              { value: '', label: t('audit.geoAuto') },
              ...(geo.files ?? []).map((f) => ({ value: f.file, label: describeDatabase(f, t) })),
            ]}
          />
        </Field>

        <Space align="start">
          <Switch checked={d.auto} onChange={(v) => setD({ ...d, auto: v })} />
          <div>
            <Typography.Text strong style={{ display: 'block' }}>
              {t('audit.geoAuto2')}
            </Typography.Text>
            <Space size={6} style={{ marginTop: 4 }}>
              <InputNumber
                min={1}
                max={720}
                style={{ width: 88 }}
                value={d.auto_hours}
                onChange={(v) => setD({ ...d, auto_hours: v ?? 12 })}
                disabled={!d.auto}
              />
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t('audit.geoIntervalHint')}
              </Typography.Text>
            </Space>
          </div>
        </Space>

        {/* Outside the auto-update switch on purpose: the source and the credential are what
            "Download now" uses too, so an admin who never turns automatic updates on still needs
            them. */}
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 16, alignItems: 'flex-start' }}>
          <Field label={t('audit.geoSourceLabel')}>
            <Select
              style={{ width: 300 }}
              value={d.source}
              onChange={(v) => {
                // The edition follows the source: a leftover "GeoLite2-City" would send IPinfo
                // looking for a database it does not publish.
                const n = GEO_SOURCES.find((x) => x.value === v)
                setD({ ...d, source: v, edition: n?.edition ?? '' })
              }}
              options={GEO_SOURCES.map((x) => ({ value: x.value, label: t(x.label) }))}
            />
          </Field>

          {d.source === 'custom' && (
            <Field label={t('audit.geoUrlLabel')} hint={t('audit.geoUrlFieldHint')}>
              <Input
                style={{ width: 380, maxWidth: '100%' }}
                placeholder={t('audit.geoUrlPlaceholder')}
                value={d.url}
                onChange={(e) => setD({ ...d, url: e.target.value })}
              />
            </Field>
          )}

          {src?.edit && (
            <Field
              label={t('audit.geoEditionLabel')}
              hint={t('audit.geoEditionHint', { def: src.edition })}
            >
              <Input
                style={{ width: 220 }}
                placeholder={src.edition}
                value={d.edition}
                onChange={(e) => setD({ ...d, edition: e.target.value })}
              />
            </Field>
          )}

          {src?.key && (
            <Field label={t(src.keyLabel)} hint={t('audit.geoKeyHint')}>
              <Input.Password
                style={{ width: 280 }}
                placeholder={upd?.has_key && key === null ? t('audit.geoKeySaved') : t('audit.geoKey')}
                value={key ?? ''}
                onChange={(e) => setKey(e.target.value)}
                autoComplete="new-password"
              />
            </Field>
          )}
        </div>

        <Space>
          <Button onClick={save} loading={busy}>
            {t('common.save')}
          </Button>
          <Button type="primary" onClick={update} loading={busy || !!upd?.updating}>
            {t('audit.geoUpdate')}
          </Button>
        </Space>

        {upd?.last_error ? (
          <Alert type="error" showIcon message={t('audit.geoUpdateFailed')} description={upd.last_error} />
        ) : upd?.last_file ? (
          <Alert type="success" showIcon message={t('audit.geoLastUpdate', { file: upd.last_file })} />
        ) : null}

        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {d.source === 'maxmind' ? t('audit.geoEulaHint') : t('audit.geoDownloadHint')}
          {' '}
          {t('audit.geoAttribution')}
        </Typography.Text>
      </Space>
    </>
  )
}
