// API client for Chintan backend
class ApiClient {
    constructor() {
        this.baseURL = window.CHINTAN_API_URL;
        this.defaultHeaders = {
            'Content-Type': 'application/json'
        };
    }

    // Get authorization headers
    getAuthHeaders() {
        const idToken = window.auth?.getIdToken();
        if (!idToken) {
            throw new Error('No authentication token available');
        }

        return {
            ...this.defaultHeaders,
            'Authorization': `Bearer ${idToken}`
        };
    }

    // Generic request method
    async request(endpoint, options = {}) {
        const url = `${this.baseURL}${endpoint}`;
        const config = {
            headers: options.useAuth !== false ? this.getAuthHeaders() : this.defaultHeaders,
            ...options
        };

        // Remove our custom options
        delete config.useAuth;

        try {
            const response = await fetch(url, config);

            // Handle 401 - session expired (except biometric login failures)
            if (response.status === 401) {
                const isWebAuthnLogin = typeof endpoint === 'string' &&
                    endpoint.indexOf('/v1/auth/webauthn/login') === 0;
                if (!isWebAuthnLogin) {
                    window.dispatchEvent(new CustomEvent('session-expired'));
                    throw new Error('Session expired');
                }
                let msg = 'Biometric verification failed';
                try {
                    const errorData = await response.json();
                    msg = errorData.error || errorData.message || msg;
                } catch (e) { /* ignore */ }
                throw new Error(msg);
            }

            // Handle other HTTP errors
            if (!response.ok) {
                let errorMessage = `HTTP ${response.status}`;
                try {
                    const errorData = await response.json();
                    errorMessage = errorData.message || errorData.error || errorMessage;
                } catch (e) {
                    // If response isn't JSON, use status text
                    errorMessage = response.statusText || errorMessage;
                }
                throw new Error(errorMessage);
            }

            // Return JSON response or null for empty responses
            const contentType = response.headers.get('content-type');
            if (contentType && contentType.includes('application/json')) {
                return await response.json();
            }
            
            return null;
        } catch (error) {
            // Re-throw our custom errors
            if (error.message === 'Session expired' || error.message.includes('HTTP')) {
                throw error;
            }
            
            // Handle network errors
            console.error('API request failed:', error);
            throw new Error('Network request failed. Please check your connection.');
        }
    }

    // Health check (no auth required)
    async health() {
        return this.request('/v1/health', { useAuth: false });
    }

    // Notes API
    async getNotes() {
        return this.request('/v1/notes');
    }

    async getNote(noteId) {
        return this.request(`/v1/notes/${noteId}`);
    }

    async listCaptures(noteId) {
        return this.request(`/v1/captures?note_id=${encodeURIComponent(noteId)}`);
    }

    async getCapture(captureId) {
        return this.request(`/v1/captures/${captureId}`);
    }

    async createNote(title, aliases = []) {
        return this.request('/v1/notes', {
            method: 'POST',
            body: JSON.stringify({ title, aliases })
        });
    }

    async updateNote(noteId, updates) {
        return this.request(`/v1/notes/${noteId}`, {
            method: 'PATCH',
            body: JSON.stringify(updates)
        });
    }

    async deleteNote(noteId) {
        return this.request(`/v1/notes/${noteId}`, {
            method: 'DELETE'
        });
    }

    async matchNotes(query) {
        return this.request('/v1/notes/match', {
            method: 'POST',
            body: JSON.stringify({ query })
        });
    }

    // Settings API
    async getSettings() {
        return this.request('/v1/settings');
    }

    async updateSettings(settings) {
        return this.request('/v1/settings', {
            method: 'PUT',
            body: JSON.stringify(settings)
        });
    }

    // Captures API
    // Pass a null noteId to let the backend decide the destination from the recording.
    async createCapture(noteId, contentType) {
        return this.request('/v1/captures', {
            method: 'POST',
            body: JSON.stringify({
                note_id: noteId || '',
                content_type: contentType
            })
        });
    }

    // target is { note_id } or { new_note_title }.
    async setCaptureTarget(captureId, target) {
        return this.request(`/v1/captures/${captureId}/target`, {
            method: 'POST',
            body: JSON.stringify(target)
        });
    }

    async completeCapture(captureId) {
        return this.request(`/v1/captures/${captureId}/complete`, {
            method: 'POST'
        });
    }

    async retryCapture(captureId) {
        return this.request(`/v1/captures/${captureId}/retry`, {
            method: 'POST'
        });
    }

    async getDownloadUrl(captureId, kind) {
        return this.request(`/v1/captures/${captureId}/download?kind=${kind}`);
    }

    async webauthnRegisterOptions() {
        return this.request('/v1/auth/webauthn/register/options', { method: 'POST', body: '{}' });
    }

    async webauthnRegister(body) {
        return this.request('/v1/auth/webauthn/register', {
            method: 'POST',
            body: JSON.stringify(body),
        });
    }

    async webauthnLoginOptions() {
        return this.request('/v1/auth/webauthn/login/options', {
            method: 'POST',
            body: '{}',
            useAuth: false,
        });
    }

    async webauthnLogin(body) {
        return this.request('/v1/auth/webauthn/login', {
            method: 'POST',
            body: JSON.stringify(body),
            useAuth: false,
        });
    }

    async webauthnStatus() {
        return this.request('/v1/auth/webauthn/status');
    }

    async webauthnDisable() {
        return this.request('/v1/auth/webauthn', { method: 'DELETE' });
    }

    // Upload audio file to pre-signed URL
    async uploadAudio(uploadUrl, audioBlob) {
        try {
            const response = await fetch(uploadUrl, {
                method: 'PUT',
                body: audioBlob,
                headers: {
                    'Content-Type': audioBlob.type || 'audio/webm'
                }
            });

            if (!response.ok) {
                throw new Error(`Upload failed: ${response.status} ${response.statusText}`);
            }

            return true;
        } catch (error) {
            console.error('Upload error:', error);
            throw new Error('Failed to upload audio file');
        }
    }
}

// Create and export API client instance
const api = new ApiClient();
window.api = api;