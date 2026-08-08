// Authentication module using Cognito Hosted UI with PKCE
class CognitoAuth {
    constructor() {
        this.userPoolId = window.CHINTAN_USER_POOL_ID;
        this.clientId = window.CHINTAN_CLIENT_ID;
        this.instance = window.CHINTAN_INSTANCE;
        this.cognitoDomain = window.CHINTAN_COGNITO_DOMAIN;
        this.redirectUri = window.location.origin + window.location.pathname;
        this.tokenKey = `chintan_tokens_${this.instance}`;
        
        // Build Cognito URLs
        this.region = this.userPoolId.split('_')[0];
        this.cognitoHostedUrl = `https://${this.cognitoDomain}.auth.${this.region}.amazoncognito.com`;
    }

    // Generate PKCE challenge
    generateCodeChallenge() {
        const codeVerifier = this.generateCodeVerifier();
        sessionStorage.setItem('codeVerifier', codeVerifier);
        return this.sha256Base64Url(codeVerifier);
    }

    generateCodeVerifier() {
        const array = new Uint8Array(32);
        crypto.getRandomValues(array);
        return this.base64UrlEncode(array);
    }

    base64UrlEncode(array) {
        return btoa(String.fromCharCode.apply(null, array))
            .replace(/\+/g, '-')
            .replace(/\//g, '_')
            .replace(/=/g, '');
    }

    async sha256Base64Url(plain) {
        const encoder = new TextEncoder();
        const data = encoder.encode(plain);
        const digest = await crypto.subtle.digest('SHA-256', data);
        const array = new Uint8Array(digest);
        return this.base64UrlEncode(array);
    }

    // Check if user is authenticated
    isAuthenticated() {
        const tokens = this.getStoredTokens();
        if (!tokens || !tokens.id_token) return false;

        try {
            const payload = JSON.parse(atob(tokens.id_token.split('.')[1]));
            const expirationTime = payload.exp * 1000;
            return Date.now() < expirationTime;
        } catch (error) {
            console.error('Error parsing token:', error);
            return false;
        }
    }

    // Get stored tokens
    getStoredTokens() {
        try {
            const stored = localStorage.getItem(this.tokenKey);
            return stored ? JSON.parse(stored) : null;
        } catch (error) {
            console.error('Error reading stored tokens:', error);
            return null;
        }
    }

    // Get ID token for API calls
    getIdToken() {
        const tokens = this.getStoredTokens();
        return tokens ? tokens.id_token : null;
    }

    // Store tokens
    storeTokens(tokens) {
        try {
            localStorage.setItem(this.tokenKey, JSON.stringify(tokens));
        } catch (error) {
            console.error('Error storing tokens:', error);
        }
    }

    // Clear tokens
    clearTokens() {
        localStorage.removeItem(this.tokenKey);
        sessionStorage.removeItem('codeVerifier');
    }

    // Initiate login flow
    async login() {
        try {
            const codeChallenge = await this.generateCodeChallenge();
            const state = Math.random().toString(36).substring(2, 15);
            sessionStorage.setItem('authState', state);

            const params = new URLSearchParams({
                response_type: 'code',
                client_id: this.clientId,
                redirect_uri: this.redirectUri,
                scope: 'openid email profile',
                code_challenge: codeChallenge,
                code_challenge_method: 'S256',
                state: state
            });

            window.location.href = `${this.cognitoHostedUrl}/oauth2/authorize?${params}`;
        } catch (error) {
            console.error('Login error:', error);
            throw new Error('Failed to initiate login');
        }
    }

    // Handle login callback
    async handleCallback() {
        const urlParams = new URLSearchParams(window.location.search);
        const code = urlParams.get('code');
        const state = urlParams.get('state');
        const error = urlParams.get('error');

        if (error) {
            throw new Error(`Authentication error: ${urlParams.get('error_description') || error}`);
        }

        if (!code) {
            return false; // No callback in progress
        }

        // Verify state
        const storedState = sessionStorage.getItem('authState');
        if (state !== storedState) {
            throw new Error('Invalid state parameter');
        }

        try {
            await this.exchangeCodeForTokens(code);
            
            // Clean up URL
            const cleanUrl = window.location.origin + window.location.pathname;
            window.history.replaceState({}, document.title, cleanUrl);
            
            return true;
        } catch (error) {
            console.error('Token exchange error:', error);
            throw new Error('Failed to complete authentication');
        }
    }

    // Exchange authorization code for tokens
    async exchangeCodeForTokens(code) {
        const codeVerifier = sessionStorage.getItem('codeVerifier');
        if (!codeVerifier) {
            throw new Error('Code verifier not found');
        }

        const params = new URLSearchParams({
            grant_type: 'authorization_code',
            client_id: this.clientId,
            code: code,
            redirect_uri: this.redirectUri,
            code_verifier: codeVerifier
        });

        const response = await fetch(`${this.cognitoHostedUrl}/oauth2/token`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/x-www-form-urlencoded',
            },
            body: params
        });

        if (!response.ok) {
            const error = await response.text();
            throw new Error(`Token exchange failed: ${error}`);
        }

        const tokens = await response.json();
        this.storeTokens(tokens);
        
        // Clean up session storage
        sessionStorage.removeItem('codeVerifier');
        sessionStorage.removeItem('authState');
    }

    // Refresh tokens if needed
    async refreshTokensIfNeeded() {
        const tokens = this.getStoredTokens();
        if (!tokens || !tokens.refresh_token) return false;

        try {
            // Check if access token is expired (with 5 minute buffer)
            const accessTokenPayload = JSON.parse(atob(tokens.access_token.split('.')[1]));
            const bufferTime = 5 * 60 * 1000; // 5 minutes
            const expirationTime = accessTokenPayload.exp * 1000;
            
            if (Date.now() + bufferTime < expirationTime) {
                return true; // Token still valid
            }

            // Refresh tokens
            const params = new URLSearchParams({
                grant_type: 'refresh_token',
                client_id: this.clientId,
                refresh_token: tokens.refresh_token
            });

            const response = await fetch(`${this.cognitoHostedUrl}/oauth2/token`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/x-www-form-urlencoded',
                },
                body: params
            });

            if (!response.ok) {
                throw new Error('Token refresh failed');
            }

            const newTokens = await response.json();
            
            // Preserve refresh token if not returned
            if (!newTokens.refresh_token) {
                newTokens.refresh_token = tokens.refresh_token;
            }
            
            this.storeTokens(newTokens);
            return true;
        } catch (error) {
            console.error('Token refresh error:', error);
            this.clearTokens();
            return false;
        }
    }

    // Logout
    async logout() {
        this.clearTokens();
        
        const params = new URLSearchParams({
            client_id: this.clientId,
            logout_uri: this.redirectUri
        });

        window.location.href = `${this.cognitoHostedUrl}/logout?${params}`;
    }

    // Get user info from ID token
    getUserInfo() {
        const idToken = this.getIdToken();
        if (!idToken) return null;

        try {
            const payload = JSON.parse(atob(idToken.split('.')[1]));
            return {
                sub: payload.sub,
                email: payload.email,
                name: payload.name || payload.email,
                given_name: payload.given_name,
                family_name: payload.family_name
            };
        } catch (error) {
            console.error('Error parsing ID token:', error);
            return null;
        }
    }
}

// Create and export auth instance
const auth = new CognitoAuth();
window.auth = auth;