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

/**
 * Whether this browser can *assert* a credential.
 *
 * Separate from `isWebAuthnAvailable` because they gate different controls, and
 * the unlock button on the signed-out screen must not appear on a browser that
 * can only enrol. `get` is what an assertion needs; `create` is what enrolment
 * needs, and the two are independently absent in the wild — a WebView
 * frequently exposes one without the other.
 */
export function canAssertWebAuthn(): boolean {
  return (
    typeof window !== 'undefined' &&
    typeof window.PublicKeyCredential !== 'undefined' &&
    typeof navigator.credentials?.get === 'function'
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
  allowCredentials?: { id?: string; type?: string; transports?: string[] }[];
  [key: string]: unknown;
}

/** The shared decoding of a credential descriptor list. */
function decodeDescriptors(
  list: { id?: string; transports?: string[] }[] | undefined,
): PublicKeyCredentialDescriptor[] {
  return (list ?? []).map((credential) => ({
    id: base64UrlToBuffer(credential.id ?? ''),
    type: 'public-key' as const,
    // `transports` is a hint only, and the DOM types narrow it to a closed
    // union the server has no reason to respect. Dropping it costs nothing.
    ...(credential.transports
      ? { transports: credential.transports as AuthenticatorTransport[] }
      : {}),
  }));
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
    excludeCredentials: decodeDescriptors(options.excludeCredentials),
  };
}

/** The assertion half of the same decoding. */
export function decodeRequestOptions(
  raw: Record<string, unknown>,
): PublicKeyCredentialRequestOptions {
  const options = raw as RawOptions;
  return {
    ...(options as unknown as PublicKeyCredentialRequestOptions),
    challenge: base64UrlToBuffer(options.challenge ?? ''),
    allowCredentials: decodeDescriptors(options.allowCredentials),
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

/**
 * Runs the get ceremony and re-encodes the result for the server.
 *
 * This is the half that did not exist. The settings toggle performed a real
 * registration and the backend vaulted a real refresh token, but no code
 * anywhere in the client ever called `navigator.credentials.get` or
 * `/v1/auth/webauthn/login` — so enrolling produced a credential that nothing
 * could ever use, and the setting looked like it worked.
 *
 * `userHandle` is sent when the authenticator returns one. It is how the server
 * identifies the account for a discoverable credential, and omitting it makes a
 * resident-key unlock unresolvable.
 */
export async function performAssertion(
  rawOptions: Record<string, unknown>,
): Promise<Record<string, unknown>> {
  const credential = (await navigator.credentials.get({
    publicKey: decodeRequestOptions(rawOptions),
  })) as PublicKeyCredential | null;

  if (!credential) throw new Error('No credential was returned');
  const response = credential.response as AuthenticatorAssertionResponse;

  return {
    id: credential.id,
    rawId: bufferToBase64Url(credential.rawId),
    type: credential.type,
    response: {
      clientDataJSON: bufferToBase64Url(response.clientDataJSON),
      authenticatorData: bufferToBase64Url(response.authenticatorData),
      signature: bufferToBase64Url(response.signature),
      ...(response.userHandle
        ? { userHandle: bufferToBase64Url(response.userHandle) }
        : {}),
    },
  };
}
