import { useEffect, useMemo, useState } from 'react'
import {
  Button,
  Col,
  Collapse,
  DatePicker,
  Empty,
  Form,
  Input,
  Pagination,
  Popover,
  Row,
  Select,
  Space,
  Spin,
  theme,
  Typography,
} from 'antd'
import { EllipsisOutlined } from '@ant-design/icons'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import dayjs from 'dayjs'
import { api, qs } from '../api/client'
import type { HomeResp, LinkItem } from '../api/types'
import { SiteLogo, useSite } from '../site'
import { useAuth } from '../auth'
import Omnibox from '../components/Omnibox'
import ReportCard from '../components/ReportCard'
import { linkIconComponent } from '../components/linkIcons'
import { shortcutOfUrl, triggerShortcut } from '../lib/shortcuts'

const { RangePicker } = DatePicker

export default function HomePage() {
  const { t } = useTranslation()
  const { title } = useSite()
  const { token } = theme.useToken()
  const navigate = useNavigate()
  const { can } = useAuth()
  const canRun = can('run_batch')
  const [sp, setSp] = useSearchParams()
  const [data, setData] = useState<HomeResp | null>(null)
  const [loading, setLoading] = useState(true)
  const [form] = Form.useForm()

  const params = useMemo(
    () => ({
      q: sp.get('q') || '',
      kind: sp.get('kind') || '',
      rtype: sp.get('rtype') || '',
      date_from: sp.get('date_from') || '',
      date_to: sp.get('date_to') || '',
      sort: sp.get('sort') || 'date_desc',
      size: sp.get('size') || '30',
      page: sp.get('page') || '1',
    }),
    [sp],
  )

  useEffect(() => {
    setLoading(true)
    api
      .get<HomeResp>(`/api/home${qs(params)}`)
      .then(setData)
      .finally(() => setLoading(false))
  }, [params])

  // Auto-refresh: silently refetch the current view (no spinner) when the tab regains
  // focus, and on a gentle interval while visible — new reports appear without a manual
  // reload, and switching back to the tab shows fresh data.
  useEffect(() => {
    const refetch = () => {
      if (document.visibilityState !== 'visible') return
      api.get<HomeResp>(`/api/home${qs(params)}`).then(setData).catch(() => {})
    }
    window.addEventListener('focus', refetch)
    document.addEventListener('visibilitychange', refetch)
    const id = setInterval(refetch, 60000)
    return () => {
      window.removeEventListener('focus', refetch)
      document.removeEventListener('visibilitychange', refetch)
      clearInterval(id)
    }
  }, [params])

  // Keep the form's initial values in sync with the URL
  useEffect(() => {
    form.setFieldsValue({
      q: params.q,
      kind: params.kind || undefined,
      rtype: params.rtype || undefined,
      range: params.date_from && params.date_to ? [dayjs(params.date_from), dayjs(params.date_to)] : undefined,
      sort: params.sort,
    })
  }, [params, form])

  const applyFilters = () => {
    const v = form.getFieldsValue()
    const next: Record<string, string> = { size: params.size, page: '1' }
    if (v.q) next.q = v.q
    if (v.kind) next.kind = v.kind
    if (v.rtype) next.rtype = v.rtype
    if (v.range?.[0]) next.date_from = v.range[0].format('YYYY-MM-DD')
    if (v.range?.[1]) next.date_to = v.range[1].format('YYYY-MM-DD')
    if (v.sort && v.sort !== 'date_desc') next.sort = v.sort
    setSp(next)
  }

  const reset = () => {
    form.resetFields()
    setSp({})
  }

  const changePage = (page: number, size: number) => {
    setSp({ ...Object.fromEntries(sp), page: String(page), size: String(size) })
  }

  const kindOptions = (data?.kinds || []).map((x) => ({ value: x, label: x }))
  const typeOptions = (data?.types || []).map((x) => ({ value: x, label: x }))

  // Render one entry button. A shortcut link (url = "rp:<action>[:<target>]") triggers an
  // internal action, optionally pre-selected on a specific target; a shortcut the user can't
  // run (e.g. run-analysis without run_batch) is hidden (returns null).
  const renderLink = (l: LinkItem) => {
    const Icon = linkIconComponent(l.icon)
    const res = shortcutOfUrl(l.url)
    if (res) {
      if (res.shortcut.requiresRun && !canRun) return null
      return (
        <Button key={l.id} icon={<Icon />} onClick={() => triggerShortcut(res.shortcut, navigate, res.param)}>
          {l.label}
        </Button>
      )
    }
    const newTab = l.newTab !== false // default: open in a new tab
    return (
      <Button key={l.id} icon={<Icon />} href={l.url} target={newTab ? '_blank' : undefined} rel={newTab ? 'noreferrer' : undefined}>
        {l.label}
      </Button>
    )
  }
  const allLinks = data?.links || []
  const inlineLinks = allLinks.filter((l) => !l.collapsed)
  // Collapsed links fold into a "More" popover; drop any hidden by run-batch gating so the
  // popover (and its trigger) never render empty.
  const moreButtons = allLinks.filter((l) => l.collapsed).map(renderLink).filter(Boolean)

  return (
    <Space direction="vertical" size={24} style={{ width: '100%' }}>
      {/* Hero: main search */}
      <div style={{ textAlign: 'center', paddingTop: 24 }}>
        <Typography.Title
          level={3}
          style={{ marginBottom: 20, display: 'inline-flex', alignItems: 'center', gap: 10 }}
        >
          <SiteLogo size={28} color={token.colorPrimary} />
          {title}
        </Typography.Title>
        <div style={{ maxWidth: 640, margin: '0 auto' }}>
          <Omnibox initial={params.q} />
        </div>
      </div>

      {/* Quick links: inline buttons, with less-used ones folded into a "More" popover */}
      {!!allLinks.length && (
        <div style={{ textAlign: 'center' }}>
          <Space size={[8, 8]} wrap>
            {inlineLinks.map(renderLink)}
            {moreButtons.length > 0 && (
              <Popover
                trigger="click"
                content={
                  <Space size={[8, 8]} wrap style={{ maxWidth: 320 }}>
                    {moreButtons}
                  </Space>
                }
              >
                <Button icon={<EllipsisOutlined />}>{t('home.more')}</Button>
              </Popover>
            )}
          </Space>
        </div>
      )}

      {/* Advanced search (collapsible) */}
      <Collapse
        items={[
          {
            key: 'adv',
            label: t('home.advanced'),
            children: (
              <Form form={form} layout="vertical" onFinish={applyFilters}>
                <Row gutter={16}>
                  <Col xs={24} md={8}>
                    <Form.Item name="q" label={t('home.keyword')}>
                      <Input allowClear placeholder={t('home.keyword')} onPressEnter={applyFilters} />
                    </Form.Item>
                  </Col>
                  <Col xs={24} md={8}>
                    <Form.Item name="kind" label={t('home.category')}>
                      <Select allowClear showSearch options={kindOptions} placeholder={t('home.category')} />
                    </Form.Item>
                  </Col>
                  <Col xs={24} md={8}>
                    <Form.Item name="rtype" label={t('home.type')}>
                      <Select allowClear showSearch options={typeOptions} placeholder={t('home.type')} />
                    </Form.Item>
                  </Col>
                  <Col xs={24} md={8}>
                    <Form.Item name="range" label={t('home.dateRange')}>
                      <RangePicker style={{ width: '100%' }} />
                    </Form.Item>
                  </Col>
                  <Col xs={24} md={8}>
                    <Form.Item name="sort" label={t('home.sort')}>
                      <Select
                        options={[
                          { value: 'date_desc', label: t('sort.dateDesc') },
                          { value: 'date_asc', label: t('sort.dateAsc') },
                        ]}
                      />
                    </Form.Item>
                  </Col>
                  <Col xs={24} md={8} style={{ display: 'flex', alignItems: 'flex-end' }}>
                    <Space style={{ marginBottom: 24 }}>
                      <Button type="primary" onClick={applyFilters}>
                        {t('home.search')}
                      </Button>
                      <Button onClick={reset}>{t('home.reset')}</Button>
                    </Space>
                  </Col>
                </Row>
              </Form>
            ),
          },
        ]}
      />

      {/* Card list */}
      <Spin spinning={loading}>
        {data && data.groups.length === 0 ? (
          <Empty description={t('home.empty')} style={{ padding: '60px 0' }} />
        ) : (
          <Row gutter={[16, 16]}>
            {data?.groups.map((g) => (
              <Col key={g.key} xs={24} sm={12} lg={8} xl={6}>
                <ReportCard g={g} kindColors={data.kindColors} />
              </Col>
            ))}
          </Row>
        )}
      </Spin>

      {/* Pagination — flex-centered (textAlign doesn't center antd's flex Pagination) */}
      {!!data && data.totalRuns > 0 && (
        <div style={{ display: 'flex', justifyContent: 'center', paddingTop: 8 }}>
          <Pagination
            current={data.page}
            pageSize={Number(params.size)}
            total={data.totalRuns}
            showSizeChanger
            pageSizeOptions={['15', '30', '50']}
            onChange={changePage}
            showTotal={(total) => `${total} ${t('home.reports')}`}
          />
        </div>
      )}
    </Space>
  )
}
