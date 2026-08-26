import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import ErrorBoundary from './ErrorBoundary'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

// React unmounts the whole tree when a render throws with nothing above it to catch. The app does
// not show an error then — it shows nothing, which is indistinguishable from a page that never
// loaded and leaves no way forward. These tests are about there always being something on screen.

function Boom({ when }: { when: boolean }): React.ReactElement {
  if (when) throw new Error('the sky fell')
  return <div>content</div>
}

let errorSpy: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  // The boundary logs on purpose (it is the only artefact of a swallowed error); React also logs
  // the caught error itself. Neither is a test failure.
  errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => errorSpy.mockRestore())

describe('ErrorBoundary', () => {
  it('is invisible while nothing is wrong', () => {
    render(
      <ErrorBoundary>
        <Boom when={false} />
      </ErrorBoundary>,
    )
    expect(screen.getByText('content')).toBeTruthy()
    expect(screen.queryByText('common.crashTitle')).toBeNull()
  })

  it('shows a page instead of a blank one when a child throws', () => {
    render(
      <ErrorBoundary>
        <Boom when={true} />
      </ErrorBoundary>,
    )
    expect(screen.getByText('common.crashTitle')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'common.crashReload' })).toBeTruthy()
  })

  it('carries the message, for the person who has to report it', () => {
    render(
      <ErrorBoundary>
        <Boom when={true} />
      </ErrorBoundary>,
    )
    expect(screen.getByText('the sky fell')).toBeTruthy()
  })

  it('can be retried without a reload once the cause has passed', async () => {
    const user = userEvent.setup()
    function Flaky() {
      const [broken, setBroken] = useState(true)
      return (
        <>
          <button onClick={() => setBroken(false)}>fix it</button>
          <ErrorBoundary>
            <Boom when={broken} />
          </ErrorBoundary>
        </>
      )
    }
    render(<Flaky />)
    expect(screen.getByText('common.crashTitle')).toBeTruthy()

    await user.click(screen.getByText('fix it'))
    await user.click(screen.getByRole('button', { name: 'common.crashRetry' }))
    expect(screen.getByText('content')).toBeTruthy()
    expect(screen.queryByText('common.crashTitle')).toBeNull()
  })
})
