// Settings management
class SettingsManager {
    constructor() {
        this.settings = {
            cleanup_mode: 'faithful',
            retention_days: 0
        };
        this.loaded = false;
        this.setupEventListeners();
    }

    setupEventListeners() {
        // Settings button in header
        document.getElementById('settings-btn').addEventListener('click', () => {
            this.showSettings();
        });

        // Back button
        document.getElementById('back-from-settings').addEventListener('click', () => {
            ui.showContentScreen('home-screen');
        });

        // Save settings button
        document.getElementById('save-settings-btn').addEventListener('click', () => {
            this.saveSettings();
        });

        // Form change detection
        document.getElementById('cleanup-mode').addEventListener('change', () => {
            this.markChanged();
        });

        document.getElementById('retention-days').addEventListener('input', () => {
            this.markChanged();
        });

        const bioBtn = document.getElementById('biometric-toggle-btn');
        if (bioBtn) {
            bioBtn.addEventListener('click', () => this.toggleBiometric());
        }
    }

    async refreshBiometricToggle() {
        const btn = document.getElementById('biometric-toggle-btn');
        if (!btn || !window.ChintanWebAuthn) return;
        const available = await ChintanWebAuthn.isPlatformAuthenticatorAvailable();
        if (!available) {
            btn.disabled = true;
            btn.textContent = 'Not available on this device';
            return;
        }
        btn.disabled = false;
        try {
            const status = await api.webauthnStatus();
            this.biometricEnrolled = !!status.enrolled;
            ChintanWebAuthn.markEnrolled(this.biometricEnrolled);
            btn.textContent = this.biometricEnrolled ? 'Disable biometric unlock' : 'Enable biometric unlock';
        } catch (error) {
            btn.textContent = 'Enable biometric unlock';
            this.biometricEnrolled = false;
        }
    }

    async toggleBiometric() {
        const btn = document.getElementById('biometric-toggle-btn');
        ui.setButtonLoading(btn, true);
        try {
            if (this.biometricEnrolled) {
                await api.webauthnDisable();
                ChintanWebAuthn.markEnrolled(false);
                this.biometricEnrolled = false;
                ui.showToast('Biometric unlock disabled', 'success');
            } else {
                const tokens = auth.getStoredTokens();
                if (!tokens || !tokens.refresh_token) {
                    throw new Error('Sign in again to enroll biometrics (missing refresh token)');
                }
                await ChintanWebAuthn.register(tokens.refresh_token);
                ChintanWebAuthn.markEnrolled(true);
                this.biometricEnrolled = true;
                ui.showToast('Biometric unlock enabled', 'success');
            }
            await this.refreshBiometricToggle();
        } catch (error) {
            console.error('Biometric toggle error:', error);
            ui.showToast(error.message || 'Biometric update failed', 'error');
        } finally {
            ui.setButtonLoading(btn, false);
        }
    }

    async showSettings() {
        try {
            await this.loadSettings();
            this.populateForm();
            await this.refreshBiometricToggle();
            ui.showContentScreen('settings-screen');
        } catch (error) {
            console.error('Failed to load settings:', error);
            ui.showToast('Failed to load settings: ' + error.message, 'error');
        }
    }

    async loadSettings() {
        try {
            this.settings = await api.getSettings();
            this.loaded = true;
            console.log('Loaded settings:', this.settings);
        } catch (error) {
            console.error('Failed to load settings:', error);
            // Use defaults if loading fails
            this.settings = {
                cleanup_mode: 'faithful',
                retention_days: 0
            };
            throw error;
        }
    }

    populateForm() {
        document.getElementById('cleanup-mode').value = this.settings.cleanup_mode || 'faithful';
        document.getElementById('retention-days').value = this.settings.retention_days || 0;
        
        this.clearChanged();
    }

    getFormData() {
        return {
            cleanup_mode: document.getElementById('cleanup-mode').value,
            retention_days: parseInt(document.getElementById('retention-days').value) || 0
        };
    }

    async saveSettings() {
        const saveButton = document.getElementById('save-settings-btn');
        ui.setButtonLoading(saveButton, true);

        try {
            const formData = this.getFormData();
            
            // Validate retention days
            if (formData.retention_days < 0) {
                throw new Error('Retention days must be 0 or greater');
            }

            await api.updateSettings(formData);
            this.settings = formData;
            
            ui.showToast('Settings saved successfully', 'success');
            this.clearChanged();
            
        } catch (error) {
            console.error('Failed to save settings:', error);
            ui.showToast('Failed to save settings: ' + error.message, 'error');
        } finally {
            ui.setButtonLoading(saveButton, false);
        }
    }

    markChanged() {
        const saveButton = document.getElementById('save-settings-btn');
        if (saveButton) {
            saveButton.textContent = 'Save Changes';
            saveButton.classList.add('btn-warning');
        }
    }

    clearChanged() {
        const saveButton = document.getElementById('save-settings-btn');
        if (saveButton) {
            saveButton.textContent = 'Save';
            saveButton.classList.remove('btn-warning');
        }
    }

    // Get current settings (load if not already loaded)
    async getCurrentSettings() {
        if (!this.loaded) {
            await this.loadSettings();
        }
        return this.settings;
    }

    // Get cleanup mode for capture operations
    async getCleanupMode() {
        const settings = await this.getCurrentSettings();
        return settings.cleanup_mode || 'faithful';
    }
}

// Create and export settings manager instance
const settings = new SettingsManager();
window.settings = settings;