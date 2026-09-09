import { HolderOutlined } from '@ant-design/icons'
import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from '@dnd-kit/core'
import {
  SortableContext,
  arrayMove,
  rectSortingStrategy,
  sortableKeyboardCoordinates,
  useSortable,
} from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import { App, Button, Card, Col, Empty, Pagination, Result, Row, Space, Spin, Tag, Typography, theme } from 'antd'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { errText } from '../api/client'
import type { FavoriteItem } from '../api/types'
import { fenToYuan, signedFenToYuan } from '../lib/money'
import { useHomeQuotes, type CardQuote } from '../lib/useHomeQuotes'
import { clickable } from '../lib/clickable'
import { FavoriteButton } from './FavoriteButton'

const PAGE_SIZE = 20

export function moveFavoriteItems(items: FavoriteItem[], activeKey: string, overKey: string): FavoriteItem[] {
  const from = items.findIndex((item) => item.key === activeKey)
  const to = items.findIndex((item) => item.key === overKey)
  if (from < 0 || to < 0 || from === to) return items
  return arrayMove(items, from, to).map((item, ord) => ({ ...item, ord }))
}

function FavoriteCard({ item, quote, disabled }: { item: FavoriteItem; quote?: CardQuote; disabled: boolean }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { token } = theme.useToken()
  const sortable = useSortable({ id: item.key, disabled })
  const style = {
    height: '100%',
    opacity: sortable.isDragging ? 0.55 : 1,
    transform: CSS.Transform.toString(sortable.transform),
    transition: sortable.transition,
    zIndex: sortable.isDragging ? 1 : undefined,
  }
  const name = quote?.name || item.report?.name || item.symbol
  const destination = item.report
    ? `/stock/${encodeURIComponent(item.symbol)}?date=${encodeURIComponent(item.report.date)}`
    : `/apps/quotes?symbol=${encodeURIComponent(`${item.market}:${item.symbol}`)}`
  const direction = quote ? Math.sign(Math.trunc(quote.change)) : 0
  const moveColor = direction > 0 ? token.colorError : direction < 0 ? token.colorSuccess : token.colorText

  return (
    <div ref={sortable.setNodeRef} style={style}>
      <Card
        hoverable
        size="small"
        style={{ height: '100%' }}
        styles={{ body: { padding: 16 } }}
        {...clickable(() => navigate(destination), name)}
      >
        <Space direction="vertical" size={10} style={{ width: '100%' }}>
          <Space align="start" style={{ justifyContent: 'space-between', width: '100%' }}>
            <div style={{ minWidth: 0, flex: 1 }}>
              <Typography.Paragraph strong ellipsis={{ rows: 1, tooltip: name }} style={{ fontSize: 16, marginBottom: 4 }}>
                {name}
              </Typography.Paragraph>
              <Space size={6} wrap>
                <Tag style={{ marginInlineEnd: 0 }}>{t(`quote.market.${item.market}`)}</Tag>
                {name !== item.symbol && (
                  <Typography.Text type="secondary" style={{ fontVariantNumeric: 'tabular-nums' }}>
                    {item.symbol}
                  </Typography.Text>
                )}
              </Space>
            </div>
            <Space size={0}>
              <FavoriteButton market={item.market} symbol={item.symbol} />
              <Button
                type="text"
                shape="circle"
                icon={<HolderOutlined />}
                aria-label={t('favorite.reorder')}
                title={t('favorite.reorder')}
                disabled={disabled}
                onClick={(event) => {
                  event.preventDefault()
                  event.stopPropagation()
                }}
                {...sortable.attributes}
                {...sortable.listeners}
              />
            </Space>
          </Space>

          <div style={{ minHeight: 22, display: 'flex', alignItems: 'baseline', gap: 8 }}>
            {quote && (
              <>
                <Typography.Text strong style={{ color: moveColor, fontSize: 17, fontVariantNumeric: 'tabular-nums' }}>
                  {fenToYuan(quote.last)}
                </Typography.Text>
                <Typography.Text style={{ color: moveColor, fontVariantNumeric: 'tabular-nums' }}>
                  {signedFenToYuan(quote.change)} {quote.changePct}%
                </Typography.Text>
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {t(`quote.currency.${quote.currency}`)}
                </Typography.Text>
              </>
            )}
          </div>

          {item.report ? (
            <div style={{ minHeight: 42 }}>
              <Typography.Paragraph ellipsis={{ rows: 1, tooltip: item.report.title }} style={{ marginBottom: 2 }}>
                {item.report.title}
              </Typography.Paragraph>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t('favorite.latestReport')} · {item.report.date}
              </Typography.Text>
            </div>
          ) : (
            <Typography.Text type="secondary" style={{ minHeight: 42 }}>
              {t('favorite.noReport')}
            </Typography.Text>
          )}
        </Space>
      </Card>
    </div>
  )
}

export default function FavoritesGrid({
  items,
  loading = false,
  error = null,
  reordering = false,
  onRetry,
  onReorder,
}: {
  items: FavoriteItem[]
  loading?: boolean
  error?: unknown
  reordering?: boolean
  onRetry?: () => void
  onReorder: (items: FavoriteItem[]) => void | Promise<void>
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [page, setPage] = useState(1)
  const maxPage = Math.max(1, Math.ceil(items.length / PAGE_SIZE))
  useEffect(() => setPage((current) => Math.min(current, maxPage)), [maxPage])
  const pageItems = useMemo(() => items.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE), [items, page])
  const quotes = useHomeQuotes(pageItems.map((item) => `${item.market}:${item.symbol}`))
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  )

  const dragEnd = (event: DragEndEvent) => {
    if (!event.over || event.active.id === event.over.id) return
    const next = moveFavoriteItems(items, String(event.active.id), String(event.over.id))
    void Promise.resolve(onReorder(next)).catch((cause) => message.error(errText(cause, t)))
  }

  if (error) {
    return (
      <Result
        status="warning"
        title={t('favorite.loadFailed')}
        subTitle={errText(error, t)}
        extra={onRetry && <Button onClick={onRetry}>{t('common.retry')}</Button>}
      />
    )
  }

  return (
    <Spin spinning={loading || reordering}>
      {items.length === 0 && !loading ? (
        <Empty description={t('favorite.empty')} style={{ padding: '60px 0' }} />
      ) : (
        <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={dragEnd}>
          <SortableContext items={pageItems.map((item) => item.key)} strategy={rectSortingStrategy}>
            <Row gutter={[16, 16]}>
              {pageItems.map((item) => (
                <Col key={item.key} xs={24} sm={12} lg={8} xl={6}>
                  <FavoriteCard item={item} quote={quotes.get(item.key)} disabled={reordering} />
                </Col>
              ))}
            </Row>
          </SortableContext>
        </DndContext>
      )}
      {items.length > PAGE_SIZE && (
        <div style={{ display: 'flex', justifyContent: 'center', paddingTop: 24 }}>
          <Pagination current={page} pageSize={PAGE_SIZE} total={items.length} showSizeChanger={false} onChange={setPage} />
        </div>
      )}
    </Spin>
  )
}
