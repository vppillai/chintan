// Notes management functionality
class NotesManager {
    constructor() {
        this.currentNoteId = null;
        this.notes = [];
        this.hasUnsavedChanges = false;
        this.viewingArchive = false;
        this.currentNoteArchived = false;
        
        this.setupEventListeners();
    }

    setupEventListeners() {
        // Navigation buttons
        document.getElementById('view-all-notes').addEventListener('click', () => {
            this.showNotesScreen();
        });

        document.getElementById('back-to-home').addEventListener('click', () => {
            ui.showContentScreen('home-screen');
            this.loadRecentNotes();
        });

        document.getElementById('back-to-notes').addEventListener('click', () => {
            this.handleBackToNotes();
        });

        document.getElementById('create-note-btn').addEventListener('click', () => {
            this.createNewNote();
        });

        document.getElementById('notes-tab-active').addEventListener('click', () => {
            this.showNotesScreen();
        });

        document.getElementById('notes-tab-archive').addEventListener('click', () => {
            this.showArchiveScreen();
        });

        document.getElementById('delete-note-btn').addEventListener('click', () => {
            this.archiveCurrentNote();
        });

        document.getElementById('restore-note-btn').addEventListener('click', () => {
            this.restoreCurrentNote();
        });

        document.getElementById('purge-note-btn').addEventListener('click', () => {
            this.purgeCurrentNote();
        });

        // Note editing
        document.getElementById('save-note-btn').addEventListener('click', () => {
            this.saveNote();
        });

        // Track changes
        const titleInput = document.getElementById('note-title-input');
        const aliasesInput = document.getElementById('note-aliases-input');
        const bodyInput = document.getElementById('note-body-input');

        [titleInput, aliasesInput, bodyInput].forEach(input => {
            input.addEventListener('input', () => {
                this.markUnsaved();
            });
        });

        // Auto-save with debounce
        const autoSave = ui.debounce(() => {
            if (this.hasUnsavedChanges && this.currentNoteId && !this.currentNoteArchived) {
                this.saveNote(true); // silent save
            }
        }, 3000);

        [titleInput, aliasesInput, bodyInput].forEach(input => {
            input.addEventListener('input', autoSave);
        });
    }

    async loadRecentNotes() {
        const container = document.getElementById('recent-notes');
        ui.showLoading(container, 'Loading recent notes...');

        try {
            this.notes = await api.getNotes();
            this.displayNotes(container, this.notes.slice(0, 5), true); // Show 5 most recent
        } catch (error) {
            console.error('Load notes error:', error);
            container.innerHTML = `<div class="error">Failed to load notes: ${error.message}</div>`;
        }
    }

    async showNotesScreen() {
        this.viewingArchive = false;
        this.setNotesTab('active');
        ui.showContentScreen('notes-screen');
        document.getElementById('create-note-btn').classList.remove('hidden');

        const container = document.getElementById('all-notes');
        ui.showLoading(container, 'Loading all notes...');

        try {
            this.notes = await api.getNotes();
            this.displayNotes(container, this.notes, false);
        } catch (error) {
            console.error('Load notes error:', error);
            container.innerHTML = `<div class="error">Failed to load notes: ${error.message}</div>`;
        }
    }

    async showArchiveScreen() {
        this.viewingArchive = true;
        this.setNotesTab('archive');
        ui.showContentScreen('notes-screen');
        document.getElementById('create-note-btn').classList.add('hidden');

        const container = document.getElementById('all-notes');
        ui.showLoading(container, 'Loading archive...');

        try {
            this.notes = await api.getNotes({ archived: true });
            this.displayNotes(container, this.notes, false, true);
        } catch (error) {
            console.error('Load archive error:', error);
            container.innerHTML = `<div class="error">Failed to load archive: ${error.message}</div>`;
        }
    }

    setNotesTab(which) {
        document.getElementById('notes-tab-active').classList.toggle('active', which === 'active');
        document.getElementById('notes-tab-archive').classList.toggle('active', which === 'archive');
    }

    daysUntilPurge(purgeAfter) {
        const ms = new Date(purgeAfter) - Date.now();
        return Math.max(0, Math.ceil(ms / 86400000));
    }

    displayNotes(container, notes, isPreview = false, isArchive = false) {
        if (notes.length === 0) {
            container.innerHTML = `
                <div class="empty-state" style="text-align: center; padding: 2rem; color: var(--color-text-light);">
                    <p>${isArchive ? 'Archive is empty.' : 'No notes yet. Create your first note to get started!'}</p>
                </div>
            `;
            return;
        }

        container.innerHTML = notes.map(note => `
            <div class="note-item" data-note-id="${note.id}">
                <div class="note-item-title">${ui.escapeHtml(note.title)}</div>
                ${note.snippet ? `<div class="note-item-snippet">${ui.escapeHtml(ui.truncateText(note.snippet, 120))}</div>` : ''}
                <div class="note-item-meta">
                    ${isArchive
                        ? `<span>Deleted ${ui.formatDate(note.deleted_at)}</span> • <span>Deletes in ${this.daysUntilPurge(note.purge_after)} days</span>`
                        : `${note.aliases && note.aliases.length > 0 ? `<span>Aliases: ${ui.escapeHtml(note.aliases.join(', '))}</span> • ` : ''}<span>Updated ${ui.formatDate(note.updated_at)}</span>`}
                </div>
            </div>
        `).join('');

        // Add click handlers
        container.querySelectorAll('.note-item').forEach(item => {
            item.addEventListener('click', () => {
                const noteId = item.dataset.noteId;
                this.showNoteDetail(noteId);
            });
        });
    }

    async showNoteDetail(noteId) {
        if (this.hasUnsavedChanges) {
            const shouldContinue = await ui.confirm(
                'You have unsaved changes. Continue without saving?',
                'Unsaved Changes'
            );
            if (!shouldContinue) return;
        }

        ui.showContentScreen('note-detail-screen');
        this.currentNoteId = noteId;

        try {
            const note = await api.getNote(noteId);
            this.displayNoteDetail(note);
            await this.loadCapturesForNote(noteId);
        } catch (error) {
            console.error('Load note error:', error);
            ui.showToast('Failed to load note: ' + error.message, 'error');
        }
    }

    displayNoteDetail(note) {
        document.getElementById('note-detail-title').textContent = note.title;
        document.getElementById('note-title-input').value = note.title || '';
        document.getElementById('note-aliases-input').value = (note.aliases || []).join(', ');
        document.getElementById('note-body-input').value = note.body || '';

        this.currentNoteArchived = Boolean(note.deleted_at);
        const titleInput = document.getElementById('note-title-input');
        const aliasesInput = document.getElementById('note-aliases-input');
        const bodyInput = document.getElementById('note-body-input');
        const readOnly = this.currentNoteArchived;
        titleInput.readOnly = readOnly;
        aliasesInput.readOnly = readOnly;
        bodyInput.readOnly = readOnly;

        document.getElementById('delete-note-btn').classList.toggle('hidden', this.currentNoteArchived);
        document.getElementById('restore-note-btn').classList.toggle('hidden', !this.currentNoteArchived);
        document.getElementById('purge-note-btn').classList.toggle('hidden', !this.currentNoteArchived);

        this.hasUnsavedChanges = false;
        this.updateSaveButton();
    }

    async archiveCurrentNote() {
        if (!this.currentNoteId || this.currentNoteArchived) return;
        const ok = await ui.confirm(
            'Move to Archive? You can restore for 30 days.',
            'Delete note'
        );
        if (!ok) return;

        try {
            await api.deleteNote(this.currentNoteId);
            ui.showToast('Moved to Archive', 'success');
            this.hasUnsavedChanges = false;
            this.currentNoteId = null;
            await this.showNotesScreen();
        } catch (error) {
            ui.showToast('Failed to archive note: ' + error.message, 'error');
        }
    }

    async restoreCurrentNote() {
        if (!this.currentNoteId) return;
        try {
            await api.restoreNote(this.currentNoteId);
            ui.showToast('Note restored', 'success');
            this.viewingArchive = false;
            await this.showNoteDetail(this.currentNoteId);
        } catch (error) {
            ui.showToast('Failed to restore note: ' + error.message, 'error');
        }
    }

    async purgeCurrentNote() {
        if (!this.currentNoteId || !this.currentNoteArchived) return;
        const ok = await ui.confirm(
            'Delete forever? This cannot be undone. Recordings for this note will also be removed.',
            'Delete forever'
        );
        if (!ok) return;

        try {
            await api.permanentlyDeleteNote(this.currentNoteId);
            ui.showToast('Note permanently deleted', 'success');
            this.hasUnsavedChanges = false;
            this.currentNoteId = null;
            await this.showArchiveScreen();
        } catch (error) {
            ui.showToast('Failed to delete note: ' + error.message, 'error');
        }
    }

    captureStatusLabel(status) {
        const labels = {
            uploaded: 'Uploading',
            transcribed: 'Transcribed',
            cleaned: 'Tidying up',
            appended: 'Saved to note',
            needs_target: 'Waiting for you',
            failed: 'Failed'
        };
        return labels[status] || status;
    }

    async loadCapturesForNote(noteId) {
        const container = document.getElementById('captures-list');
        ui.showLoading(container, 'Loading captures...');

        try {
            const captures = await api.listCaptures(noteId);
            if (!captures || captures.length === 0) {
                container.innerHTML = `
                    <div style="text-align: center; color: var(--color-text-light);">
                        <p>No captures yet for this note.</p>
                    </div>
                `;
                return;
            }

            container.innerHTML = captures.map(c => `
                <div class="capture-item">
                    <div class="capture-meta">
                        <span class="capture-status ${c.status}">${this.captureStatusLabel(c.status)}</span>
                        <span class="capture-time">${ui.formatDate(c.created_at)}</span>
                    </div>
                    <div class="capture-downloads">
                        ${c.audio_key ? `<button class="btn btn-ghost btn-small" onclick="notes.downloadCapture('${c.id}', 'audio')">Audio</button>` : ''}
                        ${c.raw_key ? `<button class="btn btn-ghost btn-small" onclick="notes.downloadCapture('${c.id}', 'raw')">Raw STT</button>` : ''}
                        ${c.clean_key ? `<button class="btn btn-ghost btn-small" onclick="notes.downloadCapture('${c.id}', 'clean')">Clean</button>` : ''}
                    </div>
                    ${c.error ? `<div class="error">${c.error}</div>` : ''}
                </div>
            `).join('');
        } catch (error) {
            console.error('Load captures error:', error);
            container.innerHTML = `<div class="error">Failed to load captures: ${error.message}</div>`;
        }
    }

    async createNewNote() {
        if (this.hasUnsavedChanges) {
            const shouldContinue = await ui.confirm(
                'You have unsaved changes. Continue without saving?',
                'Unsaved Changes'
            );
            if (!shouldContinue) return;
        }

        const title = prompt('Enter note title:');
        if (!title || !title.trim()) return;

        const createBtn = document.getElementById('create-note-btn');
        ui.setButtonLoading(createBtn, true);

        try {
            const note = await api.createNote(title.trim());
            ui.showToast('Note created successfully', 'success');
            
            // Show the new note
            this.showNoteDetail(note.id);
            
            // Refresh notes list if we're on that screen
            const notesScreen = document.getElementById('notes-screen');
            if (!notesScreen.classList.contains('hidden')) {
                this.showNotesScreen();
            }
        } catch (error) {
            console.error('Create note error:', error);
            ui.showToast('Failed to create note: ' + error.message, 'error');
        } finally {
            ui.setButtonLoading(createBtn, false);
        }
    }

    async saveNote(silent = false) {
        if (!this.currentNoteId) return;

        const titleInput = document.getElementById('note-title-input');
        const aliasesInput = document.getElementById('note-aliases-input');
        const bodyInput = document.getElementById('note-body-input');

        // Validate title
        if (!ui.validateRequired(titleInput, 'Note title is required')) {
            return;
        }

        const saveBtn = document.getElementById('save-note-btn');
        if (!silent) {
            ui.setButtonLoading(saveBtn, true);
        }

        try {
            const updates = {
                title: titleInput.value.trim(),
                aliases: aliasesInput.value.split(',').map(a => a.trim()).filter(a => a),
                body: bodyInput.value
            };

            await api.updateNote(this.currentNoteId, updates);
            
            this.hasUnsavedChanges = false;
            this.updateSaveButton();
            
            if (!silent) {
                ui.showToast('Note saved successfully', 'success');
            }

            // Update page title
            document.getElementById('note-detail-title').textContent = updates.title;

        } catch (error) {
            console.error('Save note error:', error);
            if (!silent) {
                ui.showToast('Failed to save note: ' + error.message, 'error');
            }
        } finally {
            if (!silent) {
                ui.setButtonLoading(saveBtn, false);
            }
        }
    }

    async handleBackToNotes() {
        if (this.hasUnsavedChanges && !this.currentNoteArchived) {
            const shouldSave = await ui.confirm(
                'You have unsaved changes. Do you want to save before leaving?',
                'Unsaved Changes'
            );
            if (shouldSave) {
                await this.saveNote();
            }
        }

        this.currentNoteId = null;
        this.hasUnsavedChanges = false;
        this.updateSaveButton();
        if (this.viewingArchive) {
            await this.showArchiveScreen();
        } else {
            await this.showNotesScreen();
        }
    }

    markUnsaved() {
        if (this.currentNoteArchived) return;
        this.hasUnsavedChanges = true;
        this.updateSaveButton();
    }

    updateSaveButton() {
        const saveBtn = document.getElementById('save-note-btn');
        if (this.hasUnsavedChanges && !this.currentNoteArchived) {
            saveBtn.classList.remove('hidden');
        } else {
            saveBtn.classList.add('hidden');
        }
    }

    // Download functionality for captures (placeholder)
    async downloadCapture(captureId, kind) {
        try {
            const response = await api.getDownloadUrl(captureId, kind);
            if (response.url) {
                ui.downloadFile(response.url, `capture_${captureId}_${kind}`);
            }
        } catch (error) {
            console.error('Download error:', error);
            ui.showToast('Failed to download: ' + error.message, 'error');
        }
    }
}

// Create and export notes manager instance
const notes = new NotesManager();
window.notes = notes;