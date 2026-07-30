// A full browser navigation, as opposed to a client-side route change.
//
// The SSO handshake has to leave the SPA: the redirect chain to the identity provider and back runs
// through top-level document navigations and Set-Cookie responses, none of which a fetch or a
// React Router push can perform.
//
// It exists as a function mainly so tests have something to replace. Asserting on the real
// `window.location` means overwriting a jsdom global, and doing that destabilised unrelated test
// files — replacing one small module is both safer and clearer about what is being asserted.
export function hardNavigate(url: string): void {
  window.location.href = url
}
