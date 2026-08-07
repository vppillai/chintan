// Main application controller
class ChintanApp {
    constructor() {
        this.initialized = false;
        this.init();
    }

    async init() {
        try {
            // Wait for DOM to be ready
            if (document.readyState === 'loading') {
                document.addEventListener('DOMContentLoaded', () => this.start());
            } else {
                this.start();
            }
        } catch (error) {
            console.error('App initialization error:', error);
            ui.showToast('Failed to initialize app: ' + error.message, 'error');
        }
    }

    async start() {
        if (this.initialized) return;
        
        try {
            console.log('Starting Chintan app...');
            
            // Register service worker
            await this.registerServiceWorker();
            
            // Setup global event listeners
            this.setupGlobalEventListeners();
            
            // Handle authentication
            await this.handleAuthentication();
            
            this.initialized = true;
            console.log('Chintan app initialized successfully');
            
        } catch (error) {
            console.error('App start error:', error);
            ui.showToast('Failed to start app: ' + error.message, 'error');
        }
    }

    async registerServiceWorker() {
        if ('serviceWorker' in navigator) {
            try {
                const registration = await navigator.serviceWorker.register('./sw.js');
                console.log('Service Worker registered successfully:', registration.scope);
                
                // Handle updates
                registration.addEventListener('updatefound', () => {
                    const newWorker = registration.installing;
                    newWorker.addEventListener('statechange', () => {
                        if (newWorker.state === 'installed' && navigator.serviceWorker.controller) {
                            ui.showToast('App updated. Refresh to use the latest version.', 'info', 10000);
                        }
                    });
                });
                
            } catch (error) {
                console.log('Service Worker registration failed:', error);
            }
        }
    }

    setupGlobalEventListeners() {
        // Logout button
        document.getElementById('logout-btn').addEventListener('click', async () => {
            try {
                await auth.logout();
            } catch (error) {
                console.error('Logout error:', error);
                ui.showToast('Logout failed: ' + error.message, 'error');
            }
        });

        // Login button
        document.getElementById('login-btn').addEventListener('click', async () => {
            const loginBtn = document.getElementById('login-btn');
            ui.setButtonLoading(loginBtn, true);
            
            try {
                await auth.login();
            } catch (error) {
                console.error('Login error:', error);
                ui.showToast('Login failed: ' + error.message, 'error');
                ui.setButtonLoading(loginBtn, false);
            }
        });

        document.getElementById('biometric-login-btn').addEventListener('click', async () => {
            const btn = document.getElementById('biometric-login-btn');
            ui.setButtonLoading(btn, true);
            try {
                const tokens = await ChintanWebAuthn.login();
                auth.storeTokens(tokens);
                ChintanWebAuthn.markEnrolled(true);
                await this.showMainApp();
            } catch (error) {
                console.error('Biometric login error:', error);
                ui.showToast('Biometric unlock failed: ' + error.message, 'error');
                ChintanWebAuthn.markEnrolled(false);
            } finally {
                ui.setButtonLoading(btn, false);
            }
        });

        // Handle browser back/forward
        window.addEventListener('popstate', () => {
            this.handleAuthentication();
        });

        // Handle visibility change (refresh tokens when app comes back to foreground)
        document.addEventListener('visibilitychange', () => {
            if (!document.hidden && auth.isAuthenticated()) {
                this.refreshAuthenticationIfNeeded();
            }
        });

        // Handle online/offline status
        window.addEventListener('online', () => {
            ui.showToast('Connection restored', 'success');
        });

        window.addEventListener('offline', () => {
            ui.showToast('You are offline. Some features may not work.', 'warning');
        });
    }

    async handleAuthentication() {
        try {
            // First, handle any callback from Cognito
            const callbackHandled = await auth.handleCallback();
            
            if (callbackHandled) {
                console.log('Authentication callback handled');
            }

            // Check if user is authenticated
            if (auth.isAuthenticated()) {
                await this.showMainApp();
            } else {
                this.showLoginScreen();
            }
            
        } catch (error) {
            console.error('Authentication error:', error);
            ui.showToast('Authentication error: ' + error.message, 'error');
            this.showLoginScreen();
        }
    }

    async refreshAuthenticationIfNeeded() {
        try {
            const refreshed = await auth.refreshTokensIfNeeded();
            if (!refreshed) {
                // Tokens couldn't be refreshed, need to login again
                this.showLoginScreen();
                ui.showToast('Session expired. Please sign in again.', 'warning');
            }
        } catch (error) {
            console.error('Token refresh error:', error);
            this.showLoginScreen();
        }
    }

    showLoginScreen() {
        ui.showScreen('login-screen');
        this.refreshBiometricButton();
    }

    async refreshBiometricButton() {
        const btn = document.getElementById('biometric-login-btn');
        if (!btn || !window.ChintanWebAuthn) return;
        const available = await ChintanWebAuthn.isPlatformAuthenticatorAvailable();
        if (available && ChintanWebAuthn.isMarkedEnrolled()) {
            btn.classList.remove('hidden');
        } else {
            btn.classList.add('hidden');
        }
    }

    async showMainApp() {
        try {
            // Test API connectivity
            await this.testApiConnection();
            
            // Show main app
            ui.showScreen('main-app');
            ui.showContentScreen('home-screen');
            
            // Load initial data
            await this.loadInitialData();
            
        } catch (error) {
            console.error('Failed to load main app:', error);
            ui.showToast('Failed to connect to server: ' + error.message, 'error');
            
            // Still show the app but with limited functionality
            ui.showScreen('main-app');
            ui.showContentScreen('home-screen');
        }
    }

    async testApiConnection() {
        try {
            await api.health();
            console.log('API connection successful');
        } catch (error) {
            console.log('API health check failed:', error);
            // Don't throw here - we'll still show the app
        }
    }

    async loadInitialData() {
        try {
            // Load user info
            const userInfo = auth.getUserInfo();
            if (userInfo) {
                console.log('Loaded user:', userInfo.email);
            }
            
            // Load recent notes for home screen
            await notes.loadRecentNotes();
            
        } catch (error) {
            console.error('Failed to load initial data:', error);
            // Don't show error toast here - individual components will handle their own errors
        }
    }

    // Handle app install prompt
    setupInstallPrompt() {
        let deferredPrompt;

        window.addEventListener('beforeinstallprompt', (e) => {
            // Prevent Chrome 67 and earlier from automatically showing the prompt
            e.preventDefault();
            deferredPrompt = e;
            
            // Show custom install button/prompt
            ui.showToast('Chintan can be installed on your device for easier access', 'info', 10000);
        });

        // Handle successful install
        window.addEventListener('appinstalled', () => {
            console.log('PWA was installed');
            ui.showToast('Chintan has been installed successfully!', 'success');
            deferredPrompt = null;
        });

        return deferredPrompt;
    }

    // Utility method to check if app is running in PWA mode
    isPWA() {
        return window.matchMedia('(display-mode: standalone)').matches ||
               window.navigator.standalone === true;
    }
}

// Initialize app when script loads
const app = new ChintanApp();
window.app = app;