import { useEffect, useMemo, useState } from 'react'
import {
  App,
  Button,
  Card,
  Col,
  DatePicker,
  Form,
  Input,
  Popconfirm,
  Radio,
  Row,
  Segmented,
  Select,
  Space,
  Spin,
  Tag,
  Typography,
  Upload,
} from 'antd'
import {
  ArrowLeftOutlined,
  DeleteOutlined,
  SaveOutlined,
  UploadOutlined,
} from '@ant-design/icons'
import dayjs from 'dayjs'
import { useNavigate, useParams, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { ApiError, api, errText } from '../api/client'
import type { Principal } from '../api/types'
import Markdown from '../components/Markdown'
import { principalOptions } from '../components/principals'

// The report editor (ADR 0026).
//
// One page serves three entrances, because on the server they are two operations and not three:
//
//   /report/new              write one from nothing
//   /report/new?from=<id>    write one starting from a machine report's text — a NEW report, at the
//                            manual version; the machine report is not modified and cannot be
//   /report/:id/edit         edit a hand-written one in place
//
// The middle case is the one worth being careful about in the UI, because from the author's seat it
// looks like editing something that already exists. The header says which of the two is happening,
// and if the report already HAS a hand-written form the server hands back its id and we go there
// instead of quietly creating a second one.

interface EditorForm {
  from?: number
  id?: number
  manual?: boolean
  manualId?: number
  symbol?: string
  name?: string
  date?: string
  subtype?: string
  title?: string
  source?: string
  body_md?: string
  updated_at?: string
  audience: 'all' | 'grant'
  viewers: string[]
  subtypes: string[]
  groups: Principal[]
  users: Principal[]
  usersTruncated: boolean
  today: string
}

const DATE_FMT = 'YYYY-MM-DD'

export default function ReportEditorPage() {
  const { t } = useTranslation()
  const { message, modal } = App.useApp()
  const navigate = useNavigate()
  const { id } = useParams()
  const [sp] = useSearchParams()
  const from = sp.get('from') ?? undefined

  const [form] = Form.useForm()
  const [data, setData] = useState<EditorForm | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [pane, setPane] = useState<'write' | 'preview'>('write')
  // Held in state as well as in the form, because the preview has to re-render as you type and
  // Form.useWatch on a long textarea is the same subscription with more indirection.
  const [body, setBody] = useState('')
  // The concurrency token (sent_at). Replaced by each successful save, so an author can keep
  // editing after saving without reloading the page — the alternative is a second save that
  // always reports a conflict against itself.
  const [token, setToken] = useState('')

  const editingId = id ? Number(id) : undefined

  useEffect(() => {
    let live = true
    setLoading(true)
    const seed = editingId ?? from
    api
      .get<EditorForm>(`/api/reports/editor${seed ? `?from=${encodeURIComponent(String(seed))}` : ''}`)
      .then((r) => {
        if (!live) return
        // ?from=<id> means "open the editor for this report", and only the server knows which of
        // the three that is. Both redirects below turn it into an EDIT rather than a create:
        // seeding a new report from one that is already hand-written, or from one whose
        // hand-written form exists, would collide on the identity index at save time — and two
        // hand-written forms of one report is a state nothing else in the product can express.
        if (!editingId && r.manual && r.id) {
          navigate(`/report/${r.id}/edit`, { replace: true })
          return
        }
        if (!editingId && r.manualId) {
          navigate(`/report/${r.manualId}/edit`, { replace: true })
          return
        }
        setData(r)
        setBody(r.body_md ?? '')
        setToken(r.updated_at ?? '')
        form.setFieldsValue({
          symbol: r.symbol ?? '',
          name: r.name ?? '',
          date: dayjs(r.date || r.today, DATE_FMT),
          subtype: r.subtype ?? undefined,
          title: r.title ?? '',
          source: r.source ?? '',
          audience: r.audience ?? 'grant',
          viewers: r.viewers ?? [],
        })
      })
      .catch((e) => live && message.error(errText(e, t)))
      .finally(() => live && setLoading(false))
    return () => {
      live = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editingId, from])

  const audienceOptions = useMemo(
    () =>
      principalOptions(data?.groups ?? [], data?.users ?? [], {
        groups: t('announcementAdmin.principalGroups'),
        users: t('announcementAdmin.principalUsers'),
        restricted: t('announcementAdmin.restrictedTag'),
      }),
    [data, t],
  )

  // Importing a .md file fills the body rather than uploading anything: the report is the text, and
  // a file that lands on the server before the author has looked at it is a file nobody checked.
  const importMarkdown = (file: File) => {
    const reader = new FileReader()
    reader.onload = () => {
      const text = String(reader.result ?? '')
      setBody(text)
      // A file named 2026-09-02 深度分析.md is not metadata, but an untouched title field is worth
      // filling from its name — retyping what you just chose in a file dialog is busywork.
      if (!form.getFieldValue('title')) {
        form.setFieldValue('title', file.name.replace(/\.(md|markdown|txt)$/i, ''))
      }
      setPane('write')
    }
    reader.onerror = () => message.error(t('reportEditor.importFailed'))
    reader.readAsText(file)
    return false // never let antd upload it
  }

  const save = async () => {
    const v = await form.validateFields()
    if (!body.trim()) {
      message.error(t('reportEditor.bodyRequired'))
      setPane('write')
      return
    }
    const payload = {
      symbol: (v.symbol ?? '').trim(),
      name: (v.name ?? '').trim(),
      date: v.date ? v.date.format(DATE_FMT) : '',
      subtype: v.subtype ?? '',
      title: (v.title ?? '').trim(),
      source: (v.source ?? '').trim(),
      body_md: body,
      audience: v.audience,
      viewers: v.audience === 'grant' ? (v.viewers ?? []) : [],
      updated_at: token,
    }
    setSaving(true)
    try {
      if (editingId) {
        const r = await api.put<{ updated_at: string }>(`/api/reports/${editingId}`, payload)
        setToken(r.updated_at ?? '')
        message.success(t('reportEditor.saved'))
      } else {
        const r = await api.post<{ id: number }>('/api/reports', payload)
        message.success(t('reportEditor.created'))
        navigate(`/report/${r.id}/edit`, { replace: true })
      }
    } catch (e) {
      // A collision hands back the row it collided with, and opening that one is almost always what
      // the author wants — they are writing the report that already exists.
      const other = e instanceof ApiError && e.code === 'report_exists'
        ? (e.data as { id?: number } | undefined)?.id
        : undefined
      if (other) {
        modal.confirm({
          title: t('reportEditor.existsTitle'),
          content: t('reportEditor.existsBody'),
          okText: t('reportEditor.existsOpen'),
          cancelText: t('common.cancel'),
          onOk: () => navigate(`/report/${other}/edit`),
        })
      } else {
        message.error(errText(e, t))
      }
    } finally {
      setSaving(false)
    }
  }

  const remove = async () => {
    if (!editingId) return
    try {
      await api.del(`/api/reports/${editingId}`)
      message.success(t('reportEditor.deleted'))
      navigate('/')
    } catch (e) {
      message.error(errText(e, t))
    }
  }

  if (loading) {
    return (
      <div style={{ padding: 80, textAlign: 'center' }}>
        <Spin size="large" />
      </div>
    )
  }
  if (!data) return null

  // Three headings for three entrances. "另存为人工版" is not decoration: an author who opened this
  // from a machine report is about to create a second report, not to change the one they were
  // reading, and the moment to say so is before they type rather than after they save.
  const heading = editingId
    ? t('reportEditor.titleEdit')
    : data.from
      ? t('reportEditor.titleFork')
      : t('reportEditor.titleNew')

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Space size={12} wrap>
        <Button icon={<ArrowLeftOutlined />} onClick={() => navigate(-1)}>
          {t('common.back')}
        </Button>
        <Typography.Title level={4} style={{ margin: 0 }}>
          {heading}
        </Typography.Title>
        <Tag color="blue">{t('reportEditor.manualTag')}</Tag>
      </Space>

      {data.from && !editingId && (
        <Typography.Text type="secondary">{t('reportEditor.forkNote')}</Typography.Text>
      )}

      <Form form={form} layout="vertical" initialValues={{ audience: 'grant', viewers: [] }}>
        <Row gutter={16}>
          <Col xs={24} lg={14}>
            <Card
              size="small"
              title={t('reportEditor.body')}
              extra={
                <Space size={8}>
                  <Upload accept=".md,.markdown,.txt" showUploadList={false} beforeUpload={importMarkdown}>
                    <Button size="small" icon={<UploadOutlined />}>
                      {t('reportEditor.import')}
                    </Button>
                  </Upload>
                  <Segmented
                    size="small"
                    value={pane}
                    onChange={(v) => setPane(v as 'write' | 'preview')}
                    options={[
                      { value: 'write', label: t('reportEditor.write') },
                      { value: 'preview', label: t('reportEditor.preview') },
                    ]}
                  />
                </Space>
              }
            >
              {pane === 'write' ? (
                <Input.TextArea
                  value={body}
                  onChange={(e) => setBody(e.target.value)}
                  autoSize={{ minRows: 20, maxRows: 40 }}
                  placeholder={t('reportEditor.bodyPlaceholder')}
                  aria-label={t('reportEditor.body')}
                  style={{ fontFamily: 'var(--rp-mono, ui-monospace, SFMono-Regular, Menlo, monospace)' }}
                />
              ) : (
                // The reader's own renderer, not a second one: mermaid, KaTeX and the chart fences
                // all behave here exactly as they will on the report page, which is the only way a
                // preview is worth having.
                <div style={{ minHeight: 320 }}>
                  <Markdown md={body} />
                </div>
              )}
            </Card>
          </Col>

          <Col xs={24} lg={10}>
            <Space direction="vertical" size={16} style={{ width: '100%' }}>
              <Card size="small" title={t('reportEditor.identity')}>
                <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
                  {t('reportEditor.identityNote')}
                </Typography.Paragraph>
                <Form.Item name="symbol" label={t('reportEditor.symbol')}>
                  <Input placeholder={t('reportEditor.symbolPlaceholder')} allowClear />
                </Form.Item>
                <Form.Item
                  name="title"
                  label={t('reportEditor.reportTitle')}
                  // Either a code or a title identifies a report; a thematic one has no code at all.
                  // The server enforces the same rule, so this only saves a round trip.
                  rules={[
                    ({ getFieldValue }) => ({
                      validator: (_, value) =>
                        (value ?? '').trim() || (getFieldValue('symbol') ?? '').trim()
                          ? Promise.resolve()
                          : Promise.reject(new Error(t('reportEditor.subjectRequired'))),
                    }),
                  ]}
                >
                  <Input placeholder={t('reportEditor.titlePlaceholder')} />
                </Form.Item>
                <Form.Item name="date" label={t('reportEditor.date')} rules={[{ required: true }]}>
                  <DatePicker style={{ width: '100%' }} format={DATE_FMT} allowClear={false} />
                </Form.Item>
                <Form.Item name="subtype" label={t('reportEditor.subtype')} rules={[{ required: true }]}>
                  <Select
                    showSearch
                    // Free entry as well as the registry: a hand-written report is often the first
                    // of a type nobody has run yet, and the server registers an unknown subtype on
                    // sight exactly as the ingest path does.
                    mode="tags"
                    maxCount={1}
                    options={(data.subtypes ?? []).map((s) => ({ value: s, label: s }))}
                    placeholder={t('reportEditor.subtypePlaceholder')}
                  />
                </Form.Item>
                <Form.Item name="name" label={t('reportEditor.company')}>
                  <Input placeholder={t('reportEditor.companyPlaceholder')} allowClear />
                </Form.Item>
                <Form.Item name="source" label={t('reportEditor.source')}>
                  <Input placeholder={t('reportEditor.sourcePlaceholder')} allowClear />
                </Form.Item>
              </Card>

              <Card size="small" title={t('reportEditor.audience')}>
                <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
                  {t('reportEditor.audienceNote')}
                </Typography.Paragraph>
                <Form.Item name="audience" style={{ marginBottom: 12 }}>
                  <Radio.Group
                    optionType="button"
                    options={[
                      { value: 'all', label: t('reportEditor.audienceAll') },
                      { value: 'grant', label: t('reportEditor.audienceChosen') },
                    ]}
                  />
                </Form.Item>
                <Form.Item shouldUpdate={(a, b) => a.audience !== b.audience} noStyle>
                  {({ getFieldValue }) =>
                    getFieldValue('audience') === 'grant' ? (
                      <Form.Item
                        name="viewers"
                        extra={
                          data.usersTruncated
                            ? t('announcementAdmin.grantsTruncated', { count: data.users.length })
                            : undefined
                        }
                        rules={[{ required: true, message: t('reportEditor.audienceRequired') }]}
                      >
                        <Select
                          mode="multiple"
                          allowClear
                          options={audienceOptions}
                          optionFilterProp="label"
                          placeholder={t('reportEditor.audiencePlaceholder')}
                        />
                      </Form.Item>
                    ) : null
                  }
                </Form.Item>
              </Card>

              <Space size={8} wrap>
                <Button type="primary" icon={<SaveOutlined />} loading={saving} onClick={save}>
                  {t('common.save')}
                </Button>
                {editingId && (
                  <Popconfirm title={t('reportEditor.deleteConfirm')} onConfirm={remove}>
                    <Button danger icon={<DeleteOutlined />}>
                      {t('common.delete')}
                    </Button>
                  </Popconfirm>
                )}
              </Space>
            </Space>
          </Col>
        </Row>
      </Form>
    </Space>
  )
}
