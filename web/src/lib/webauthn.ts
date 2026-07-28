// WebAuthn browser plumbing (ADR 0023).
//
// The server speaks the JSON encoding from the spec, in which every binary field is base64url. The
// browser API takes and returns ArrayBuffers. These two functions are that translation and nothing
// else — kept out of the page so the ceremony code reads as the ceremony.

function b64urlToBytes(s: string): Uint8Array {
  const pad = s.replace(/-/g, '+').replace(/_/g, '/')
  const raw = atob(pad + '='.repeat((4 - (pad.length % 4)) % 4))
  const out = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

function bytesToB64url(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf)
  let s = ''
  for (const b of bytes) s += String.fromCharCode(b)
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

export function passkeySupported(): boolean {
  return typeof window !== 'undefined' && !!window.PublicKeyCredential && !!navigator.credentials
}

// createCredential runs a registration ceremony and returns the JSON the server expects back.
export async function createCredential(options: any): Promise<any> {
  const publicKey: PublicKeyCredentialCreationOptions = {
    ...options,
    challenge: b64urlToBytes(options.challenge) as unknown as BufferSource,
    user: { ...options.user, id: b64urlToBytes(options.user.id) as unknown as BufferSource },
    excludeCredentials: (options.excludeCredentials ?? []).map((c: any) => ({
      ...c,
      id: b64urlToBytes(c.id) as unknown as BufferSource,
    })),
  }
  const cred = (await navigator.credentials.create({ publicKey })) as PublicKeyCredential | null
  if (!cred) throw new Error('no credential was created')
  const att = cred.response as AuthenticatorAttestationResponse
  return {
    id: cred.id,
    rawId: bytesToB64url(cred.rawId),
    type: cred.type,
    response: {
      clientDataJSON: bytesToB64url(att.clientDataJSON),
      attestationObject: bytesToB64url(att.attestationObject),
    },
  }
}

// getCredential runs an assertion (login) ceremony.
export async function getCredential(options: any): Promise<any> {
  const publicKey: PublicKeyCredentialRequestOptions = {
    ...options,
    challenge: b64urlToBytes(options.challenge) as unknown as BufferSource,
    allowCredentials: (options.allowCredentials ?? []).map((c: any) => ({
      ...c,
      id: b64urlToBytes(c.id) as unknown as BufferSource,
    })),
  }
  const cred = (await navigator.credentials.get({ publicKey })) as PublicKeyCredential | null
  if (!cred) throw new Error('no credential was returned')
  const asr = cred.response as AuthenticatorAssertionResponse
  return {
    id: cred.id,
    rawId: bytesToB64url(cred.rawId),
    type: cred.type,
    response: {
      clientDataJSON: bytesToB64url(asr.clientDataJSON),
      authenticatorData: bytesToB64url(asr.authenticatorData),
      signature: bytesToB64url(asr.signature),
      userHandle: asr.userHandle ? bytesToB64url(asr.userHandle) : null,
    },
  }
}
