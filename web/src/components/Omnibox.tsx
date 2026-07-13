import { useRef, useState } from 'react'
import { AutoComplete, Grid, Input, Space, Tag, Typography } from 'antd'
import { SearchOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api, qs } from '../api/client'
import type { SymbolInfo } from '../api/types'

// Main search (search-engine style): type a code or name → /api/symbols suggests
// matching stocks (code + name + report count + latest date, across new AND legacy
// reports). Clicking a suggestion opens that stock's reports; pressing Enter runs a
// full search over every report. Both land on the home list (?q=…), which renders
// new and legacy reports alike.
export default function Omnibox({ size = 'large', initial = '' }: { size?: 'large' | 'middle'; initial?: string }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const mobile = !Grid.useBreakpoint().md
  const [value, setValue] = useState(initial)
  const [options, setOptions] = useState<{ value: string; label: React.ReactNode; sym: string }[]>([])
  const timer = useRef<number | undefined>(undefined)

  const fetchOptions = (q: string) => {
    window.clearTimeout(timer.current)
    if (!q.trim()) {
      setOptions([])
      return
    }
    timer.current = window.setTimeout(async () => {
      try {
        const res = await api.get<{ symbols: SymbolInfo[] }>(`/api/symbols${qs({ q, limit: 20 })}`)
        setOptions(
          (res.symbols || []).map((s) => ({
            value: `${s.symbol} ${s.name}`,
            sym: s.symbol,
            label: (
              <Space size={8} style={{ display: 'flex', justifyContent: 'space-between', width: '100%' }}>
                <span>
                  <Typography.Text strong>{s.symbol}</Typography.Text>{' '}
                  <Typography.Text>{s.name}</Typography.Text>
                </span>
                <span>
                  <Tag color="blue" style={{ marginInlineEnd: 4 }}>
                    {s.count}
                  </Tag>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {s.latest}
                  </Typography.Text>
                </span>
              </Space>
            ),
          })),
        )
      } catch {
        setOptions([])
      }
    }, 200)
  }

  // Full search: filter the home list by a query (name / code / title, new + legacy).
  const search = (raw: string) => {
    const q = raw.trim()
    navigate(q ? `/?q=${encodeURIComponent(q)}` : '/')
  }
  // Open one stock's dedicated page (its full report timeline).
  const openStock = (sym: string) => {
    if (sym) navigate(`/stock/${encodeURIComponent(sym)}`)
  }

  return (
    <AutoComplete
      value={value}
      options={options}
      onChange={setValue}
      onSearch={fetchOptions}
      // Click a suggestion → go straight into that stock's page.
      onSelect={(_v, opt) => openStock((opt as { sym: string }).sym)}
      // Don't auto-select the first option on Enter — Enter should run a full search.
      defaultActiveFirstOption={false}
      style={{ width: '100%' }}
      // A fixed 480px popup overflows a phone and pushes the names off-screen; match the
      // input width on mobile so suggestions stay fully visible.
      popupMatchSelectWidth={mobile ? true : 480}
    >
      <Input
        size={size}
        allowClear
        prefix={<SearchOutlined />}
        placeholder={t('home.searchPlaceholder')}
        onPressEnter={(e) => search((e.target as HTMLInputElement).value)}
      />
    </AutoComplete>
  )
}
