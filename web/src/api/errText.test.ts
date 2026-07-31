import { describe, it, expect } from 'vitest'
import { ApiError, errText } from './client'

// A server message cannot be localized. The portal speaks three languages and the response is
// written before the browser's choice is known, so an English login page was printing
// "用户名或密码错误" and a Chinese one "captcha is required or incorrect" — the same bug in both
// directions. The server now sends a stable code and the UI renders that through t().

// Stands in for i18next, which echoes the key back when it has no string for it.
const dict: Record<string, string> = {
  'err.bad_credentials': 'Incorrect username or password.',
  'login.error': 'Sign-in failed.',
  'common.error': 'Something went wrong.',
}
const t = (k: string) => dict[k] ?? k

describe('errText', () => {
  it('translates a known server code, ignoring the server wording entirely', () => {
    const e = new ApiError(401, '用户名或密码错误', 'bad_credentials')
    expect(errText(e, t)).toBe('Incorrect username or password.')
  })

  it('falls back to the server message for a code the UI has no string for', () => {
    // A newer server, an older bundle: showing the raw key would be worse than showing the message.
    const e = new ApiError(400, 'that email domain is not accepted here', 'unknown_future_code')
    expect(errText(e, t)).toBe('that email domain is not accepted here')
  })

  it('falls back to the server message when there is no code at all', () => {
    expect(errText(new ApiError(500, 'boom'), t)).toBe('boom')
  })

  it('uses the caller\'s own fallback for a non-Api error, and a generic one by default', () => {
    expect(errText(new Error('network down'), t, 'login.error')).toBe('Sign-in failed.')
    expect(errText('not even an error', t)).toBe('Something went wrong.')
  })

  it('never renders a bare translation key at the user', () => {
    const e = new ApiError(401, '', 'bad_credentials')
    expect(errText(e, t)).not.toMatch(/^err\./)
    // No code, no message: still not a key.
    expect(errText(new ApiError(401, ''), t, 'login.error')).toBe('Sign-in failed.')
  })
})
