/**
 * WebAuthn ceremony plumbing.
 *
 * The server sends creation options as JSON; the browser API wants
 * `ArrayBuffer`s, and returns `ArrayBuffer`s the server needs as base64url.
 * All of that conversion lives here so the component stays readable and the
 * encoding is done in exactly one place — getting it wrong produces a
 * credential that enrolls and then never verifies.
 */

export function isWebAuthnAvailable(): boolean {
  return (
    typeof window !== 'undefined' &&
    typeof window.PublicKeyCredential !== 'undefined' &&
    typeof navigator.credentials?.create === 'function'
  );
}

export function base64UrlToBuffer(value: string): ArrayBuffer {
  const padded = value.replace(/-/g, '+').replace(/_/g, '/');
  const binary = atob(padded.padEnd(Math.ceil(padded.length / 4) * 4, '='));
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index);
  }
  return bytes.buffer;
}

export function bufferToBase64Url(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer);
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

interface RawOptions {
  challenge?: string;
  user?: { id?: string; name?: string; displayName?: string };
  excludeCredentials?: { id?: string; type?: string; transports?: string[] }[];
  [key: string]: unknown;
}

/** Decodes the base64url fields the spec requires as BufferSource. */
export function decodeCreationOptions(
  raw: Record<string, unknown>,
): PublicKeyCredentialCreationOptions {
  const options = raw as RawOptions;
  return {
    ...(options as unknown as PublicKeyCredentialCreationOptions),
    challenge: base64UrlToBuffer(options.challenge ?? ''),
    user: {
      ...(options.user as unknown as PublicKeyCredentialUserEntity),
      id: base64UrlToBuffer(options.user?.id ?? ''),
    },
    excludeCredentials: (options.excludeCredentials ?? []).map((credential) => ({
      id: base64UrlToBuffer(credential.id ?? ''),
      type: 'public-key' as const,
      // `transports` is a hint only, and the DOM types narrow it to a closed
      // union the server has no reason to respect. Dropping it costs nothing.
      ...(credential.transports
        ? { transports: credential.transports as AuthenticatorTransport[] }
        : {}),
    })),
  };
}

/** Runs the create ceremony and re-encodes the result for the server. */
export async function performRegistration(
  rawOptions: Record<string, unknown>,
): Promise<Record<string, unknown>> {
  const credential = (await navigator.credentials.create({
    publicKey: decodeCreationOptions(rawOptions),
  })) as PublicKeyCredential | null;

  if (!credential) throw new Error('No credential was created');
  const response = credential.response as AuthenticatorAttestationResponse;

  return {
    id: credential.id,
    rawId: bufferToBase64Url(credential.rawId),
    type: credential.type,
    response: {
      clientDataJSON: bufferToBase64Url(response.clientDataJSON),
      attestationObject: bufferToBase64Url(response.attestationObject),
    },
  };
}
