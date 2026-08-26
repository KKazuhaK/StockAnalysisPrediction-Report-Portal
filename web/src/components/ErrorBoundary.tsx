import { Component, type ErrorInfo, type ReactNode } from 'react'
import { Button, Card, Result, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'

// The last thing between a thrown render error and a blank page.
//
// React unmounts the whole tree when a render throws with nothing above it to catch — the app does
// not show an error, it shows nothing at all, which is indistinguishable from a page that never
// loaded and leaves no way forward but a manual reload. That is what a session ending underneath a
// page used to look like: every call 401s, something reads a field off the data that never arrived,
// and the app disappears.
//
// The session case is handled where it belongs now (the client notices, the route gate sends the
// user to the login form). This is the backstop for everything else: whatever the cause, say that
// something broke and offer the two ways out. It is deliberately plain — no data, no fetches, no
// router — because it has to work in a tree that has just proved it can fail.
interface State {
  error: Error | null
}

export default class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // Kept for whoever opens the console: the boundary swallows the error, so without this the one
    // artefact of the failure is a screenshot of the card below.
    console.error('unhandled render error', error, info.componentStack)
  }

  render() {
    if (!this.state.error) return this.props.children
    return <CrashCard error={this.state.error} onRetry={() => this.setState({ error: null })} />
  }
}

function CrashCard({ error, onRetry }: { error: Error; onRetry: () => void }) {
  const { t } = useTranslation()
  return (
    <div style={{ display: 'flex', justifyContent: 'center', padding: 24 }}>
      <Card style={{ width: '100%', maxWidth: 560 }}>
        <Result
          status="warning"
          title={t('common.crashTitle')}
          subTitle={t('common.crashHint')}
          extra={
            <Space wrap>
              {/* Re-rendering is worth one try — a transient failure clears — and a reload is the
                  answer when it does not. */}
              <Button onClick={onRetry}>{t('common.crashRetry')}</Button>
              <Button type="primary" onClick={() => window.location.reload()}>
                {t('common.crashReload')}
              </Button>
            </Space>
          }
        />
        {/* The message itself, small and last: it is for the person reporting the fault, not for
            the person who just hit it. */}
        <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 0, textAlign: 'center' }}>
          {error.message}
        </Typography.Paragraph>
      </Card>
    </div>
  )
}
