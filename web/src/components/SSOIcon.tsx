import { KeyOutlined, SafetyCertificateOutlined, GithubOutlined, GitlabOutlined } from '@ant-design/icons'

// The icon beside an SSO button's name.
//
// Two shapes, and the server refuses everything else: a built-in preset, or a path under
// /site-assets/ that this portal serves itself. Never a remote URL — the login page is
// unauthenticated, so loading an image from someone else's host would announce every visitor to
// them before anyone signs in.
//
// The brand marks are drawn here rather than fetched, for the same reason. They are simplified
// glyphs, not the vendors' official logos: recognisable at 18px, and nothing is downloaded.

const SIZE = 18

function Entra() {
  // The Microsoft four-square, which is what an admin recognises on the Entra button.
  return (
    <svg width={SIZE} height={SIZE} viewBox="0 0 20 20" aria-hidden focusable="false">
      <rect x="1" y="1" width="8" height="8" fill="#F25022" />
      <rect x="11" y="1" width="8" height="8" fill="#7FBA00" />
      <rect x="1" y="11" width="8" height="8" fill="#00A4EF" />
      <rect x="11" y="11" width="8" height="8" fill="#FFB900" />
    </svg>
  )
}

function Google() {
  return (
    <svg width={SIZE} height={SIZE} viewBox="0 0 48 48" aria-hidden focusable="false">
      <path fill="#4285F4" d="M45 24c0-1.6-.1-2.7-.4-4H24v7.5h12c-.2 2-1.5 5-4.4 7l6.7 5.2C42.2 36 45 30.6 45 24z" />
      <path fill="#34A853" d="M24 46c5.9 0 10.9-2 14.5-5.3l-6.9-5.4C29.7 36.6 27.1 37.5 24 37.5c-5.8 0-10.7-3.9-12.5-9.2l-7.1 5.5C8 41.3 15.4 46 24 46z" />
      <path fill="#FBBC05" d="M11.5 28.3A13.5 13.5 0 0 1 10.8 24c0-1.5.3-2.9.7-4.3l-7.1-5.5A22 22 0 0 0 2 24c0 3.6.9 6.9 2.4 9.8z" />
      <path fill="#EA4335" d="M24 10.5c4.1 0 6.9 1.8 8.5 3.3l6.2-6C34.9 4.3 29.9 2 24 2 15.4 2 8 6.7 4.4 14.2l7.1 5.5C13.3 14.4 18.2 10.5 24 10.5z" />
    </svg>
  )
}

function Okta() {
  return (
    <svg width={SIZE} height={SIZE} viewBox="0 0 20 20" aria-hidden focusable="false">
      <circle cx="10" cy="10" r="9" fill="#007DC1" />
      <circle cx="10" cy="10" r="3.6" fill="#fff" />
    </svg>
  )
}

function Keycloak() {
  return (
    <svg width={SIZE} height={SIZE} viewBox="0 0 20 20" aria-hidden focusable="false">
      <path fill="#4D4D4D" d="M5.4 2h9.2l4.4 8-4.4 8H5.4L1 10z" />
      <path fill="#008AAA" d="M7.2 6h2.3v8H7.2zm2.9 4 3.1-4h2.6l-3.2 4 3.2 4h-2.6z" />
    </svg>
  )
}

function Auth0() {
  return (
    <svg width={SIZE} height={SIZE} viewBox="0 0 20 20" aria-hidden focusable="false">
      <path fill="#EB5424" d="M10 1l2.8 5.6L19 7.4l-4.5 4.3 1.1 6.3L10 15l-5.6 3 1.1-6.3L1 7.4l6.2-.8z" />
    </svg>
  )
}

// currentColor, so these follow the button's text in both themes.
const PRESETS: Record<string, () => React.ReactElement> = {
  entra: Entra,
  google: Google,
  okta: Okta,
  keycloak: Keycloak,
  auth0: Auth0,
  github: () => <GithubOutlined style={{ fontSize: SIZE }} />,
  gitlab: () => <GitlabOutlined style={{ fontSize: SIZE }} />,
  key: () => <KeyOutlined style={{ fontSize: SIZE }} />,
  shield: () => <SafetyCertificateOutlined style={{ fontSize: SIZE }} />,
}

/** Every preset name the picker offers. The server keeps the same list as an allow-list. */
export const SSO_ICON_PRESETS = Object.keys(PRESETS)

export default function SSOIcon({ icon }: { icon?: string }) {
  if (!icon) return null
  if (icon.startsWith('preset:')) {
    const Draw = PRESETS[icon.slice('preset:'.length)]
    // An unknown preset draws nothing rather than a broken-image box. The server refuses to store
    // one, so this only happens to a row written by a newer build than the page.
    return Draw ? <Draw /> : null
  }
  // Only ever a path this portal serves; the server refuses anything that would reach another host.
  if (!icon.startsWith('/site-assets/')) return null
  return <img src={icon} alt="" width={SIZE} height={SIZE} style={{ objectFit: 'contain', display: 'block' }} />
}
