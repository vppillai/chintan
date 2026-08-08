// UI utility functions and toast notifications
class UIManager {
    constructor() {
        this.toastContainer = document.getElementById('toast-container');
        this.setupEventListeners();
    }

    setupEventListeners() {
        // Session expired handler
        window.addEventListener('session-expired', () => {
            this.showToast('Your session has expired. Please sign in again.', 'error');
            setTimeout(() => {
                window.location.reload();
            }, 2000);
        });
    }

    // Show/hide screens
    showScreen(screenId) {
        // Hide all screens
        document.querySelectorAll('.screen').forEach(screen => {
            screen.classList.add('hidden');
        });

        // Hide all content screens
        document.querySelectorAll('.content-screen').forEach(screen => {
            screen.classList.add('hidden');
        });

        // Show requested screen
        const screen = document.getElementById(screenId);
        if (screen) {
            screen.classList.remove('hidden');
        }
    }

    // Show content screen within main app
    showContentScreen(screenId) {
        // Hide all content screens
        document.querySelectorAll('.content-screen').forEach(screen => {
            screen.classList.add('hidden');
        });

        // Show requested screen
        const screen = document.getElementById(screenId);
        if (screen) {
            screen.classList.remove('hidden');
        }
    }

    // Toast notifications
    showToast(message, type = 'info', duration = 5000) {
        const toast = document.createElement('div');
        toast.className = `toast ${type}`;
        toast.textContent = message;

        this.toastContainer.appendChild(toast);

        // Auto remove after duration
        setTimeout(() => {
            if (toast.parentNode) {
                this.removeToast(toast);
            }
        }, duration);

        // Allow manual removal by clicking
        toast.addEventListener('click', () => {
            this.removeToast(toast);
        });

        return toast;
    }

    removeToast(toast) {
        toast.style.animation = 'slideOut 0.3s ease forwards';
        setTimeout(() => {
            if (toast.parentNode) {
                toast.parentNode.removeChild(toast);
            }
        }, 300);
    }

    // Loading states
    showLoading(container, message = 'Loading...') {
        container.innerHTML = `<div class="loading">${message}</div>`;
    }

    hideLoading(container) {
        const loading = container.querySelector('.loading');
        if (loading) {
            loading.remove();
        }
    }

    // Button states
    setButtonLoading(button, isLoading, originalText = null) {
        if (isLoading) {
            if (!button.dataset.originalText) {
                button.dataset.originalText = originalText || button.textContent;
            }
            button.textContent = 'Loading...';
            button.disabled = true;
        } else {
            button.textContent = button.dataset.originalText || originalText || button.textContent;
            button.disabled = false;
            delete button.dataset.originalText;
        }
    }

    // Form validation helpers
    validateRequired(element, message = 'This field is required') {
        const value = element.value.trim();
        if (!value) {
            this.showFieldError(element, message);
            return false;
        }
        this.clearFieldError(element);
        return true;
    }

    showFieldError(element, message) {
        this.clearFieldError(element);
        
        const errorDiv = document.createElement('div');
        errorDiv.className = 'field-error';
        errorDiv.style.cssText = `
            color: var(--color-danger);
            font-size: 0.875rem;
            margin-top: 0.25rem;
        `;
        errorDiv.textContent = message;
        
        element.parentNode.insertBefore(errorDiv, element.nextSibling);
        element.style.borderColor = 'var(--color-danger)';
    }

    clearFieldError(element) {
        const error = element.parentNode.querySelector('.field-error');
        if (error) {
            error.remove();
        }
        element.style.borderColor = '';
    }

    // Format dates
    formatDate(dateString) {
        try {
            const date = new Date(dateString);
            return date.toLocaleDateString(undefined, {
                year: 'numeric',
                month: 'short',
                day: 'numeric',
                hour: '2-digit',
                minute: '2-digit'
            });
        } catch (error) {
            return dateString;
        }
    }

    // Format time duration
    formatDuration(seconds) {
        const mins = Math.floor(seconds / 60);
        const secs = seconds % 60;
        return `${mins}:${secs.toString().padStart(2, '0')}`;
    }

    // Truncate text
    truncateText(text, maxLength = 100) {
        if (text.length <= maxLength) return text;
        return text.substring(0, maxLength - 3) + '...';
    }

    // Safe HTML escaping
    escapeHtml(text) {
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
    }

    // Debounce function
    debounce(func, wait, immediate) {
        let timeout;
        return function executedFunction(...args) {
            const later = () => {
                timeout = null;
                if (!immediate) func.apply(this, args);
            };
            const callNow = immediate && !timeout;
            clearTimeout(timeout);
            timeout = setTimeout(later, wait);
            if (callNow) func.apply(this, args);
        };
    }

    // Copy to clipboard
    async copyToClipboard(text) {
        try {
            await navigator.clipboard.writeText(text);
            this.showToast('Copied to clipboard', 'success');
            return true;
        } catch (error) {
            console.error('Copy failed:', error);
            this.showToast('Failed to copy to clipboard', 'error');
            return false;
        }
    }

    // Download file
    downloadFile(url, filename) {
        const a = document.createElement('a');
        a.href = url;
        a.download = filename;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
    }

    // Confirm dialog
    async confirm(message, title = 'Confirm') {
        return new Promise((resolve) => {
            const dialog = document.createElement('div');
            dialog.style.cssText = `
                position: fixed;
                top: 0;
                left: 0;
                right: 0;
                bottom: 0;
                background: rgba(0,0,0,0.5);
                display: flex;
                align-items: center;
                justify-content: center;
                z-index: 2000;
            `;

            const content = document.createElement('div');
            content.style.cssText = `
                background: white;
                padding: 2rem;
                border-radius: 0.5rem;
                max-width: 400px;
                margin: 1rem;
                text-align: center;
            `;

            content.innerHTML = `
                <h3 style="margin-bottom: 1rem; color: var(--color-forest);">${this.escapeHtml(title)}</h3>
                <p style="margin-bottom: 2rem;">${this.escapeHtml(message)}</p>
                <button class="btn btn-primary" style="margin-right: 1rem;">Confirm</button>
                <button class="btn btn-secondary">Cancel</button>
            `;

            const confirmBtn = content.querySelector('.btn-primary');
            const cancelBtn = content.querySelector('.btn-secondary');

            confirmBtn.addEventListener('click', () => {
                document.body.removeChild(dialog);
                resolve(true);
            });

            cancelBtn.addEventListener('click', () => {
                document.body.removeChild(dialog);
                resolve(false);
            });

            dialog.addEventListener('click', (e) => {
                if (e.target === dialog) {
                    document.body.removeChild(dialog);
                    resolve(false);
                }
            });

            dialog.appendChild(content);
            document.body.appendChild(dialog);
        });
    }
}

// Create and export UI manager instance
const ui = new UIManager();
window.ui = ui;

// Add slideOut animation
const style = document.createElement('style');
style.textContent = `
    @keyframes slideOut {
        from {
            transform: translateX(0);
            opacity: 1;
        }
        to {
            transform: translateX(100%);
            opacity: 0;
        }
    }
`;
document.head.appendChild(style);