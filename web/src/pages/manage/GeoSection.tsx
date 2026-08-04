import { useCallback, useEffect, useState } from 'react'
import { App, Button, Divider, Input, InputNumber, Select, Space, Switch, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import type { GeoStatus, GeoUpdateState } from '../../api/types'

const GEO_SOURCES = [
  { value: 'maxmind', label: 'MaxMind GeoLite2', edition: 'GeoLite2-City', needsKey: true },
  { value: 'dbip', label: 'DB-IP Lite', edition: '', needsKey: false },
  { value: 'ipinfo', label: 'IPinfo Lite', edition: 'ipinfo_lite', needsKey: true },
  { value: 'custom', label: 'Custom URL', edition: '', needsKey: false },
]

/**
 * The IP database: whether to resolve at all, which file to use, and where to get a new one.
 *
 * It lives on this page because this is the only screen that shows an IP address — somebody
 * wondering why a row has no location is looking at the row.
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
          file: r.status.pick ?? '',
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
      <Space direction="vertical" size={10} style={{ width: '100%' }}>
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

        <Space wrap>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {t('audit.geoActive')}
          </Typography.Text>
          <Select
            size="small"
            style={{ minWidth: 260 }}
            value={d.file}
            onChange={(v) => setD({ ...d, file: v })}
            options={[
              { value: '', label: t('audit.geoAuto') },
              ...(geo.files ?? []).map((f) => ({
                value: f.file,
                label: f.ok ? `${f.file} · ${f.info?.granularity ?? ''}` : t('audit.geoUnreadable', { file: f.file }),
              })),
            ]}
          />
        </Space>
        {(geo.files ?? []).map((f) => (
          <Typography.Text key={f.file} type="secondary" style={{ fontSize: 12 }}>
            {f.ok
              ? `• ${f.file} — ${f.info?.granularity ?? '—'} · ${f.info?.type ?? '—'}${
                  f.info?.build_epoch
                    ? ' · ' + t('audit.geoBuilt', { at: new Date(f.info.build_epoch * 1000).toISOString().slice(0, 10) })
                    : ''
                }`
              : `• ${t('audit.geoUnreadable', { file: f.file })}`}
          </Typography.Text>
        ))}

        <Space align="start">
          <Switch checked={d.auto} onChange={(v) => setD({ ...d, auto: v })} />
          <div>
            <Typography.Text strong style={{ display: 'block' }}>
              {t('audit.geoAuto2')}
            </Typography.Text>
            <Space size={6}>
              <InputNumber
                size="small"
                min={1}
                max={720}
                value={d.auto_hours}
                onChange={(v) => setD({ ...d, auto_hours: v ?? 12 })}
              />
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t('audit.geoIntervalHint')}
              </Typography.Text>
            </Space>
          </div>
        </Space>

        <Space wrap>
          <Select
            size="small"
            style={{ width: 220 }}
            value={d.source}
            onChange={(v) => {
              const n = GEO_SOURCES.find((x) => x.value === v)
              setD({ ...d, source: v, edition: n?.edition ?? '' })
            }}
            options={GEO_SOURCES.map((x) => ({ value: x.value, label: x.label }))}
          />
          {d.source === 'custom' ? (
            <Input
              size="small"
              style={{ width: 340 }}
              placeholder={t('audit.geoUrlPlaceholder')}
              value={d.url}
              onChange={(e) => setD({ ...d, url: e.target.value })}
            />
          ) : (
            <Input
              size="small"
              style={{ width: 220 }}
              placeholder={t('audit.geoEdition')}
              value={d.edition}
              onChange={(e) => setD({ ...d, edition: e.target.value })}
              disabled={d.source === 'dbip'}
            />
          )}
          {src?.needsKey && (
            <Input.Password
              size="small"
              style={{ width: 260 }}
              placeholder={upd?.has_key && key === null ? t('audit.geoKeySaved') : t('audit.geoKey')}
              value={key ?? ''}
              onChange={(e) => setKey(e.target.value)}
              autoComplete="new-password"
            />
          )}
        </Space>

        <Space>
          <Button onClick={save} loading={busy}>
            {t('common.save')}
          </Button>
          <Button type="primary" onClick={update} loading={busy || !!upd?.updating}>
            {t('audit.geoUpdate')}
          </Button>
        </Space>

        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {t('audit.geoEulaHint')}
        </Typography.Text>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {t('audit.geoAttribution')}
        </Typography.Text>
        {upd?.last_error && (
          <Typography.Text type="danger" style={{ fontSize: 12 }}>
            {upd.last_error}
          </Typography.Text>
        )}
        {!upd?.last_error && upd?.last_file && (
          <Typography.Text type="success" style={{ fontSize: 12 }}>
            {t('audit.geoLastUpdate', { file: upd.last_file })}
          </Typography.Text>
        )}
      </Space>
    </>
  )
}
