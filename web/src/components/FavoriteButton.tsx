import { StarFilled, StarOutlined } from '@ant-design/icons'
import { App, Button, theme } from 'antd'
import type { MouseEvent } from 'react'
import { useTranslation } from 'react-i18next'
import type { QuoteResp } from '../api/types'
import { errText } from '../api/client'
import { useFavorites } from '../favorites'

export function FavoriteButton({
  market,
  symbol,
  className,
  showLabel = false,
  size,
}: {
  market: QuoteResp['market']
  symbol: string
  className?: string
  showLabel?: boolean
  size?: 'small' | 'middle' | 'large'
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { token } = theme.useToken()
  const favorites = useFavorites()
  const active = favorites.isFavorite(market, symbol)
  const busy = favorites.isBusy(market, symbol)
  const label = t(active ? 'favorite.remove' : 'favorite.add')

  const click = (event: MouseEvent<HTMLElement>) => {
    event.preventDefault()
    event.stopPropagation()
    const action = favorites.error
      ? favorites.ensureLoaded(true).then(() => favorites.toggle(market, symbol))
      : favorites.toggle(market, symbol)
    void action.catch((cause) => message.error(errText(cause, t)))
  }

  return (
    <Button
      type="text"
      shape={showLabel ? undefined : 'circle'}
      size={size}
      className={className}
      aria-label={label}
      aria-pressed={active}
      title={label}
      disabled={!favorites.loaded || favorites.loading || favorites.reordering}
      loading={busy || (!favorites.loaded && favorites.loading)}
      icon={active ? <StarFilled style={{ color: token.colorWarning }} /> : <StarOutlined />}
      onClick={click}
    >
      {showLabel ? label : null}
    </Button>
  )
}
