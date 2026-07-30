import { useEffect, useRef, useState } from 'react'
import { Button, Grid, Popover, Segmented, Slider, Space, Switch, Typography } from 'antd'
import type { TooltipRef } from 'antd/es/tooltip'
import { FontSizeOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { FONT_DEFAULT, FONT_MAX, FONT_MIN, setReaderPrefs, useReaderPrefs } from '../reader'
import { followTrigger } from '../lib/followAlign'

// The "Aa" reading-settings popover: body font size / weight and (on desktop) a
// wide-layout toggle. State lives in the shared reader store, so changes apply
// live to the rendered report and persist across sessions.
export default function ReaderControls() {
  const { t } = useTranslation()
  const { fontSize, fontWeight, wide } = useReaderPrefs()
  const screens = Grid.useBreakpoint()
  const isDesktop = !!screens.md
  const [open, setOpen] = useState(false)
  const pop = useRef<TooltipRef>(null)

  // The wide toggle is the one control here that moves its own popover: it widens the reading
  // column, so the card header this button sits in slides sideways underneath an already-open
  // popover. antd re-positions on scroll and on window resize, neither of which a re-flow is, so
  // the popover stayed at the coordinates it opened at. forceAlign is the supported way to ask it
  // to catch up, and the widening is animated, so we follow the button until it comes to rest.
  useEffect(() => {
    const p = pop.current
    if (!open || !p?.nativeElement) return
    return followTrigger(p.nativeElement, () => p.forceAlign())
  }, [open, wide])

  const content = (
    <div style={{ width: 244 }}>
      <Space direction="vertical" size={14} style={{ width: '100%' }}>
        <div>
          <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 2 }}>
            <Typography.Text type="secondary">{t('reader.fontSize')}</Typography.Text>
            <Typography.Text style={{ fontVariantNumeric: 'tabular-nums' }}>{fontSize}px</Typography.Text>
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <span style={{ fontSize: 13, lineHeight: 1 }} aria-hidden>
              A
            </span>
            <Slider
              style={{ flex: 1, margin: 0 }}
              min={FONT_MIN}
              max={FONT_MAX}
              value={fontSize}
              onChange={(v) => setReaderPrefs({ fontSize: v })}
              tooltip={{ open: false }}
              ariaLabelForHandle={t('reader.fontSize')}
            />
            <span style={{ fontSize: 21, lineHeight: 1 }} aria-hidden>
              A
            </span>
          </div>
        </div>

        <div>
          <Typography.Text type="secondary" style={{ display: 'block', marginBottom: 6 }}>
            {t('reader.fontWeight')}
          </Typography.Text>
          <Segmented
            block
            value={fontWeight}
            onChange={(v) => setReaderPrefs({ fontWeight: Number(v) })}
            options={[
              { label: <span style={{ fontWeight: 400 }}>{t('reader.weightNormal')}</span>, value: 400 },
              { label: <span style={{ fontWeight: 500 }}>{t('reader.weightMedium')}</span>, value: 500 },
              { label: <span style={{ fontWeight: 700 }}>{t('reader.weightBold')}</span>, value: 600 },
            ]}
          />
        </div>

        {isDesktop && (
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
            <Typography.Text type="secondary">{t('reader.wide')}</Typography.Text>
            <Switch checked={wide} onChange={(v) => setReaderPrefs({ wide: v })} aria-label={t('reader.wide')} />
          </div>
        )}

        <Button
          type="link"
          size="small"
          style={{ padding: 0, height: 'auto' }}
          onClick={() => setReaderPrefs({ fontSize: FONT_DEFAULT, fontWeight: 400, wide: false })}
        >
          {t('reader.reset')}
        </Button>
      </Space>
    </div>
  )

  return (
    <Popover
      ref={pop}
      content={content}
      title={t('reader.title')}
      trigger="click"
      placement="bottomRight"
      open={open}
      onOpenChange={setOpen}
    >
      <Button icon={<FontSizeOutlined />}>{t('reader.title')}</Button>
    </Popover>
  )
}
