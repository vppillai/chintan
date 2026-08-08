// WebAuthn helpers for Chintan biometric unlock (passbook-style).
(function () {
    function isWebAuthnSupported() {
        return typeof window !== 'undefined' &&
            typeof window.PublicKeyCredential === 'function' &&
            !!(navigator.credentials && navigator.credentials.create && navigator.credentials.get);
    }

    async function isPlatformAuthenticatorAvailable() {
        if (!isWebAuthnSupported() ||
            typeof window.PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable !== 'function') {
            return false;
        }
        try {
            return await window.PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable();
        } catch {
            return false;
        }
    }

    function base64urlToBuffer(value) {
        const base64 = value.replace(/-/g, '+').replace(/_/g, '/');
        const padded = base64.padEnd(base64.length + ((4 - (base64.length % 4)) % 4), '=');
        const binary = atob(padded);
        const bytes = new Uint8Array(binary.length);
        for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
        return bytes.buffer;
    }

    function bufferToBase64url(buffer) {
        const bytes = buffer instanceof ArrayBuffer ? new Uint8Array(buffer) : new Uint8Array(buffer.buffer);
        let binary = '';
        for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i]);
        return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    }

    function decodeCreationOptions(publicKey) {
        const out = { ...publicKey };
        out.challenge = base64urlToBuffer(publicKey.challenge);
        out.user = { ...publicKey.user, id: base64urlToBuffer(publicKey.user.id) };
        if (Array.isArray(publicKey.excludeCredentials)) {
            out.excludeCredentials = publicKey.excludeCredentials.map(c => ({
                ...c,
                id: base64urlToBuffer(c.id),
            }));
        }
        return out;
    }

    function decodeRequestOptions(publicKey) {
        const out = { ...publicKey };
        out.challenge = base64urlToBuffer(publicKey.challenge);
        if (Array.isArray(publicKey.allowCredentials)) {
            out.allowCredentials = publicKey.allowCredentials.map(c => ({
                ...c,
                id: base64urlToBuffer(c.id),
            }));
        }
        return out;
    }

    function encodeRegistrationCredential(cred) {
        const r = cred.response;
        const json = {
            id: cred.id,
            rawId: bufferToBase64url(cred.rawId),
            type: cred.type,
            clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {},
            response: {
                clientDataJSON: bufferToBase64url(r.clientDataJSON),
                attestationObject: bufferToBase64url(r.attestationObject),
            },
        };
        if (typeof r.getTransports === 'function') {
            json.response.transports = r.getTransports();
        }
        if (cred.authenticatorAttachment) json.authenticatorAttachment = cred.authenticatorAttachment;
        return json;
    }

    function encodeAssertionCredential(cred) {
        const r = cred.response;
        const json = {
            id: cred.id,
            rawId: bufferToBase64url(cred.rawId),
            type: cred.type,
            clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {},
            response: {
                clientDataJSON: bufferToBase64url(r.clientDataJSON),
                authenticatorData: bufferToBase64url(r.authenticatorData),
                signature: bufferToBase64url(r.signature),
                userHandle: r.userHandle ? bufferToBase64url(r.userHandle) : null,
            },
        };
        if (cred.authenticatorAttachment) json.authenticatorAttachment = cred.authenticatorAttachment;
        return json;
    }

    function unwrapPublicKey(options) {
        // Server may return { publicKey: {...} } or the publicKey object itself.
        if (options && options.publicKey) return options.publicKey;
        return options;
    }

    async function register(refreshToken) {
        const start = await api.webauthnRegisterOptions();
        const publicKey = decodeCreationOptions(unwrapPublicKey(JSON.parse(typeof start.options === 'string' ? start.options : JSON.stringify(start.options))));
        const cred = await navigator.credentials.create({ publicKey });
        await api.webauthnRegister({
            challenge_id: start.challenge_id,
            credential: encodeRegistrationCredential(cred),
            refresh_token: refreshToken,
        });
    }

    async function login() {
        const start = await api.webauthnLoginOptions();
        const publicKey = decodeRequestOptions(unwrapPublicKey(JSON.parse(typeof start.options === 'string' ? start.options : JSON.stringify(start.options))));
        const cred = await navigator.credentials.get({ publicKey });
        return api.webauthnLogin({
            challenge_id: start.challenge_id,
            credential: encodeAssertionCredential(cred),
        });
    }

    function biometricKey() {
        const instance = window.CHINTAN_INSTANCE || 'dev';
        return `chintan_biometric_${instance}`;
    }

    function markEnrolled(enrolled) {
        if (enrolled) localStorage.setItem(biometricKey(), '1');
        else localStorage.removeItem(biometricKey());
    }

    function isMarkedEnrolled() {
        return localStorage.getItem(biometricKey()) === '1';
    }

    window.ChintanWebAuthn = {
        isWebAuthnSupported,
        isPlatformAuthenticatorAvailable,
        register,
        login,
        markEnrolled,
        isMarkedEnrolled,
    };
})();
