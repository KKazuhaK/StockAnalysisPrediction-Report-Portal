import { useCallback, useEffect, useState } from 'react'
import { Alert, App, Button, Card, Empty, Input, Popconfirm, Select, Space, Tag, Typography } from 'antd'
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'

// Report versions (ADR 0024). A report can be published in several written forms; each is produced
// by its own run. This page registers the forms and decides, per form, who may read it and whose
// reports they see.
//
// Two settings, deliberately separate, because conflating them is what made the previous design
// unable to express a read-only client:
//   - grants   — WHICH principals (groups or accounts) may read this version at all
//   - visibility — WHOSE reports of it they see: only their own requests, their group's, or all
// Neither has anything to do with who may RUN a workflow.

interface VersionRow {
  name: string
  label: string
  ord: number
  visibility: 'owner' | 'group' | 'all'
  grants: string[]
  is_default: boolean
  reports: number
}
interface Principal {
  principal: string
  name: string
  restricted?: boolean
  display?: string
}

export default function VersionsPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [versions, setVersions] = useState<VersionRow[]>([])
  const [groups, setGroups] = useState<Principal[]>([])
  const [users, setUsers] = useState<Principal[]>([])
  const [loading, setLoading] = useState(true)
  const [adding, setAdding] = useState('')

  const load = useCallback(() => {
    setLoading(true)
    api
      .get<{ versions: VersionRow[]; groups: Principal[]; users: Principal[] }>('/api/admin/versions')
      .then((r) => {
        setVersions(r.versions ?? [])
        setGroups(r.groups ?? [])
        setUsers(r.users ?? [])
      })
      .catch((e) => message.error(errText(e, t)))
      .finally(() => setLoading(false))
  }, [])
  useEffect(load, [load])

  const save = async (v: VersionRow) => {
    try {
      await api.post('/api/admin/versions', {
        name: v.name,
        label: v.label,
        ord: v.ord,
        visibility: v.visibility,
        grants: v.grants,
      })
      message.success(t('versions.saved'))
      load()
    } catch (e) {
      message.error(errText(e, t))
    }
  }

  const patch = (name: string, changes: Partial<VersionRow>) =>
    setVersions((list) => list.map((v) => (v.name === name ? { ...v, ...changes } : v)))

  const principalOptions = [
    {
      label: t('versions.principalGroups'),
      options: groups.map((g) => ({
        value: g.principal,
        label: g.restricted ? `${g.name} · ${t('versions.restrictedTag')}` : g.name,
      })),
    },
    {
      label: t('versions.principalUsers'),
      options: users.map((u) => ({
        value: u.principal,
        label: u.display && u.display !== u.name ? `${u.display} (${u.name})` : u.name,
      })),
    },
  ]

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <div>
        <Typography.Title level={4} style={{ marginBottom: 4 }}>
          {t('versions.title')}
        </Typography.Title>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          {t('versions.desc')}
        </Typography.Paragraph>
      </div>

      {!loading && versions.length === 0 && <Empty />}

      {versions.map((v) => (
        <Card
          key={v.name}
          title={
            <Space>
              <Typography.Text code>{v.name}</Typography.Text>
              {v.is_default && <Tag>{t('versions.default')}</Tag>}
              <Typography.Text type="secondary" style={{ fontWeight: 400 }}>
                {t('versions.reports', { count: v.reports })}
              </Typography.Text>
            </Space>
          }
          extra={
            !v.is_default && (
              <Popconfirm
                title={t('versions.deleteConfirm')}
                onConfirm={async () => {
                  try {
                    await api.del(`/api/admin/versions/${encodeURIComponent(v.name)}`)
                    load()
                  } catch (e) {
                    message.error(errText(e, t))
                  }
                }}
              >
                <Button type="text" danger size="small" icon={<DeleteOutlined />} />
              </Popconfirm>
            )
          }
        >
          <Space direction="vertical" size="middle" style={{ width: '100%' }}>
            {v.is_default && <Alert type="info" showIcon message={t('versions.defaultHint')} />}
            <div>
              <Typography.Text strong>{t('versions.label')}</Typography.Text>
              <Input
                value={v.label}
                placeholder={v.name}
                onChange={(e) => patch(v.name, { label: e.target.value })}
                onBlur={() => save(v)}
                style={{ marginTop: 6 }}
              />
            </div>
            <div>
              <Typography.Text strong>{t('versions.visibility')}</Typography.Text>
              <Select
                value={v.visibility}
                style={{ width: '100%', marginTop: 6 }}
                onChange={(vis) => {
                  patch(v.name, { visibility: vis })
                  save({ ...v, visibility: vis })
                }}
                options={[
                  { value: 'owner', label: t('versions.visOwner') },
                  { value: 'group', label: t('versions.visGroup') },
                  { value: 'all', label: t('versions.visAll') },
                ]}
              />
            </div>
            <div>
              <Typography.Text strong>{t('versions.grants')}</Typography.Text>
              <Select
                mode="multiple"
                allowClear
                value={v.grants}
                style={{ width: '100%', marginTop: 6 }}
                placeholder={t('versions.grantsPlaceholder')}
                onChange={(g) => patch(v.name, { grants: g })}
                onBlur={() => save(v)}
                options={principalOptions}
                optionFilterProp="label"
              />
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t('versions.grantsHint')}
              </Typography.Text>
            </div>
          </Space>
        </Card>
      ))}

      <Space.Compact style={{ width: '100%', maxWidth: 420 }}>
        <Input
          value={adding}
          placeholder={t('versions.nameHint')}
          onChange={(e) => setAdding(e.target.value)}
          onPressEnter={() => adding.trim() && save({ name: adding.trim(), label: '', ord: versions.length, visibility: 'owner', grants: [], is_default: false, reports: 0 }).then(() => setAdding(''))}
        />
        <Button
          type="primary"
          icon={<PlusOutlined />}
          disabled={!adding.trim()}
          onClick={() =>
            save({ name: adding.trim(), label: '', ord: versions.length, visibility: 'owner', grants: [], is_default: false, reports: 0 }).then(() => setAdding(''))
          }
        >
          {t('versions.add')}
        </Button>
      </Space.Compact>
    </Space>
  )
}
