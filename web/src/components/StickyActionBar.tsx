import type { ReactNode } from 'react'
import { theme } from 'antd'

// A settings-page action bar pinned to the bottom of its scroll section so the primary Save stays
// reachable on a long form without scrolling to the very end. Place it as the LAST child of a tall
// block-level container (a Form, a Card body, or a plain div) — never directly inside an antd
// <Space>, whose per-item wrapper is only as tall as the bar and so leaves it nothing to stick to.
// It pins to the viewport bottom while that container is in view, then comes to rest at its end.
// The container background matches the page (colorBgContainer) so form rows scroll cleanly behind it.
export default function StickyActionBar({ children }: { children: ReactNode }) {
  const { token } = theme.useToken()
  return (
    <div
      style={{
        position: 'sticky',
        bottom: 0,
        zIndex: 3,
        display: 'flex',
        justifyContent: 'flex-end',
        gap: token.marginXS,
        marginTop: token.marginXS,
        paddingBlock: token.paddingSM,
        background: token.colorBgContainer,
        borderTop: `1px solid ${token.colorBorderSecondary}`,
      }}
    >
      {children}
    </div>
  )
}
