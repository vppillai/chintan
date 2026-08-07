// Audio capture and recording functionality
class CaptureManager {
    constructor() {
        this.mediaRecorder = null;
        this.audioChunks = [];
        this.isRecording = false;
        this.selectedNoteId = null;
        this.matchedNotes = [];
        this.currentCaptureId = null;
        // Quick Record path: audio is captured with no note, then a note is created automatically.
        this.quickMode = false;
        this.quickNoteId = null;
        
        this.setupEventListeners();
    }

    setupEventListeners() {
        // Match notes button
        document.getElementById('match-btn').addEventListener('click', () => {
            this.handleMatchNotes();
        });

        // New note button
        document.getElementById('new-note-btn').addEventListener('click', () => {
            this.handleNewNote();
        });

        // Quick record: capture first, choose the note afterwards
        document.getElementById('quick-record-btn').addEventListener('click', () => {
            this.startQuickRecording();
        });

        // Recording controls
        document.getElementById('record-btn').addEventListener('click', () => {
            this.startRecording();
        });

        document.getElementById('stop-btn').addEventListener('click', () => {
            this.stopRecording();
        });

        // Enter key in query input
        document.getElementById('query-input').addEventListener('keypress', (e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                this.handleMatchNotes();
            }
        });
    }

    async handleMatchNotes() {
        const queryInput = document.getElementById('query-input');
        const query = queryInput.value.trim();
        
        if (!query) {
            ui.showToast('Please enter a description', 'warning');
            return;
        }

        const matchBtn = document.getElementById('match-btn');
        ui.setButtonLoading(matchBtn, true);

        try {
            const result = await api.matchNotes(query);
            this.displayMatchResults(result);
        } catch (error) {
            console.error('Match error:', error);
            ui.showToast('Failed to find matching notes: ' + error.message, 'error');
        } finally {
            ui.setButtonLoading(matchBtn, false);
        }
    }

    // /v1/notes/match returns note_id, /v1/notes returns id
    noteIdOf(note) {
        return note.note_id || note.id;
    }

    displayMatchResults(result) {
        this.matchedNotes = result.candidates || [];

        if (result.auto_select_id) {
            const selectedNote = this.matchedNotes.find(n => this.noteIdOf(n) === result.auto_select_id);
            this.chooseTarget(result.auto_select_id, selectedNote ? selectedNote.title : 'Selected Note');
            return;
        }

        this.renderNoteChoices(this.matchedNotes, 'Choose a note to append to:');
    }

    renderNoteChoices(notes, title) {
        const resultsContainer = document.getElementById('match-results');
        const candidatesContainer = document.getElementById('match-candidates');

        document.getElementById('match-results-title').textContent = title;
        candidatesContainer.innerHTML = '';

        notes.forEach(note => {
            const candidate = document.createElement('div');
            candidate.className = 'match-candidate';
            candidate.dataset.noteId = this.noteIdOf(note);

            candidate.innerHTML = `
                <div>
                    <div class="candidate-title">${ui.escapeHtml(note.title)}</div>
                    ${note.snippet ? `<div class="candidate-snippet">${ui.escapeHtml(ui.truncateText(note.snippet, 80))}</div>` : ''}
                </div>
            `;

            candidate.addEventListener('click', () => {
                this.selectCandidate(candidate, note);
            });

            candidatesContainer.appendChild(candidate);
        });

        resultsContainer.classList.remove('hidden');
    }

    selectCandidate(candidateElement, note) {
        // Clear previous selections
        document.querySelectorAll('.match-candidate').forEach(c => {
            c.classList.remove('selected');
        });
        
        // Select this candidate
        candidateElement.classList.add('selected');

        this.chooseTarget(this.noteIdOf(note), note.title);
    }

    // Advanced "append to existing note" path: reveal the recorder for the chosen note.
    chooseTarget(noteId, noteTitle) {
        this.selectedNoteId = noteId;
        document.getElementById('match-results').classList.add('hidden');
        this.showRecordingSection(noteTitle);
    }

    async handleNewNote() {
        const queryInput = document.getElementById('query-input');
        const query = queryInput.value.trim();

        if (!query) {
            ui.showToast('Please enter a description for the new note', 'warning');
            return;
        }

        const newNoteBtn = document.getElementById('new-note-btn');
        ui.setButtonLoading(newNoteBtn, true);

        try {
            const note = await api.createNote(query);
            ui.showToast('Created new note: ' + note.title, 'success');
            this.chooseTarget(note.id, note.title);
        } catch (error) {
            console.error('Create note error:', error);
            ui.showToast('Failed to create note: ' + error.message, 'error');
        } finally {
            ui.setButtonLoading(newNoteBtn, false);
        }
    }

    defaultNoteTitle() {
        return 'Voice note ' + new Date().toLocaleString();
    }

    showRecordingSection(noteTitle) {
        const titleEl = document.getElementById('recording-title');

        if (noteTitle) {
            titleEl.innerHTML = 'Recording for: <span id="target-note-name"></span>';
            document.getElementById('target-note-name').textContent = noteTitle;
        } else {
            titleEl.textContent = 'Recording… tap Stop and your note saves automatically';
        }

        document.getElementById('recording-section').classList.remove('hidden');
        
        // Hide match results
        document.getElementById('match-results').classList.add('hidden');
    }

    async startQuickRecording() {
        this.quickMode = true;
        this.pendingBlob = null;
        this.selectedNoteId = null;

        this.showRecordingSection(null);
        // The in-section record button is redundant while quick recording runs.
        document.getElementById('record-btn').classList.add('hidden');

        await this.startRecording({ requireNote: false });

        if (!this.isRecording) {
            this.quickMode = false;
            document.getElementById('recording-section').classList.add('hidden');
            document.getElementById('record-btn').classList.remove('hidden');
        }
    }

    async startRecording(options = {}) {
        if (options.requireNote !== false && !this.selectedNoteId) {
            ui.showToast('No note selected', 'error');
            return;
        }

        try {
            // Request microphone permission
            const stream = await navigator.mediaDevices.getUserMedia({ 
                audio: {
                    echoCancellation: true,
                    noiseSuppression: true,
                    sampleRate: 44100
                } 
            });

            // Create media recorder
            const mimeType = this.getSupportedMimeType();
            this.mediaRecorder = new MediaRecorder(stream, { mimeType });
            this.audioChunks = [];

            this.mediaRecorder.ondataavailable = (event) => {
                if (event.data.size > 0) {
                    this.audioChunks.push(event.data);
                }
            };

            this.mediaRecorder.onstop = () => {
                this.handleRecordingStop();
            };

            // Start recording
            this.mediaRecorder.start(100); // Collect data every 100ms
            this.isRecording = true;

            // Update UI
            this.updateRecordingUI();
            ui.showToast('Recording started', 'success');

        } catch (error) {
            console.error('Recording error:', error);
            ui.showToast('Failed to start recording: ' + error.message, 'error');
        }
    }

    stopRecording() {
        if (this.mediaRecorder && this.isRecording) {
            this.mediaRecorder.stop();
            this.isRecording = false;
            
            // Stop all tracks
            this.mediaRecorder.stream.getTracks().forEach(track => track.stop());
            
            this.updateRecordingUI();
        }
    }

    getSupportedMimeType() {
        const types = [
            'audio/webm;codecs=opus',
            'audio/webm',
            'audio/mp4',
            'audio/ogg;codecs=opus',
            'audio/ogg'
        ];
        
        for (const type of types) {
            if (MediaRecorder.isTypeSupported(type)) {
                return type;
            }
        }
        
        return 'audio/webm'; // Fallback
    }

    updateRecordingUI() {
        const recordBtn = document.getElementById('record-btn');
        const stopBtn = document.getElementById('stop-btn');
        const statusSection = document.getElementById('recording-status');
        const statusText = document.getElementById('status-text');
        const progress = document.getElementById('progress');

        if (this.isRecording) {
            recordBtn.classList.add('hidden');
            stopBtn.classList.remove('hidden');
            statusSection.classList.remove('hidden');
            statusText.textContent = 'Recording... Click stop when finished';
            progress.style.width = '100%';
            progress.classList.add('pulse');
        } else {
            // In quick mode the note is still unknown, so re-recording would have no target.
            if (!this.quickMode || this.selectedNoteId) {
                recordBtn.classList.remove('hidden');
            }
            stopBtn.classList.add('hidden');
            statusText.textContent = 'Processing recording...';
            progress.classList.remove('pulse');
        }
    }

    async handleRecordingStop() {
        try {
            // Create audio blob
            const audioBlob = new Blob(this.audioChunks, { type: this.getSupportedMimeType() });
            
            if (audioBlob.size === 0) {
                throw new Error('No audio data recorded');
            }

            // Voice-first default: no note was chosen, so create one automatically.
            if (!this.selectedNoteId) {
                await this.quickFinalize(audioBlob);
                return;
            }

            await this.uploadBlob(audioBlob);
        } catch (error) {
            console.error('Upload error:', error);
            ui.showToast('Failed to save recording: ' + error.message, 'error');

            // Reset UI but keep the recording
            document.getElementById('recording-status').classList.add('hidden');
            document.getElementById('record-btn').classList.remove('hidden');
            document.getElementById('stop-btn').classList.add('hidden');
        }
    }

    // Open → talk → stop → note. Creates a note, uploads, transcribes, and titles it, no prompts.
    async quickFinalize(audioBlob) {
        const statusEl = document.getElementById('recording-status');
        const statusText = document.getElementById('status-text');
        const progress = document.getElementById('progress');
        statusEl.classList.remove('hidden');
        statusText.textContent = 'Saving your note…';
        progress.style.width = '10%';

        const note = await api.createNote(this.defaultNoteTitle());
        this.selectedNoteId = note.id;
        this.quickNoteId = note.id;

        statusText.textContent = 'Uploading recording…';
        progress.style.width = '30%';
        const captureResponse = await api.createCapture(note.id, audioBlob.type);
        this.currentCaptureId = captureResponse.capture_id;
        await api.uploadAudio(captureResponse.upload_url, audioBlob);

        statusText.textContent = 'Transcribing…';
        progress.style.width = '55%';
        const completedCapture = await api.completeCapture(this.currentCaptureId);

        this.showProcessingStatus(completedCapture);

        if (completedCapture.status === 'failed') {
            ui.showToast('Could not transcribe audio: ' + (completedCapture.error || 'unknown error'), 'error');
            this.quickNoteId = null;
            this.resetCaptureForm(false);
        }
    }

    // Derive a readable title from the transcript, then open the note.
    async autoTitleAndOpen(noteId) {
        try {
            const note = await api.getNote(noteId);
            const body = (note.body || '').trim();
            if (body) {
                let title = body.split(/\s+/).slice(0, 8).join(' ');
                if (title.length > 60) title = title.slice(0, 57) + '…';
                title = title.replace(/[\s.,;:!?-]+$/, '');
                if (title && title !== note.title) {
                    await api.updateNote(noteId, { title });
                }
            }
        } catch (error) {
            console.warn('Auto-title failed:', error);
        }

        if (window.notes && typeof notes.showNoteDetail === 'function') {
            try {
                await notes.showNoteDetail(noteId);
            } catch (error) {
                console.warn('Open note failed:', error);
            }
        }
    }

    async uploadBlob(audioBlob) {
        try {
            document.getElementById('recording-section').classList.remove('hidden');
            document.getElementById('recording-status').classList.remove('hidden');

            // Update status
            document.getElementById('status-text').textContent = 'Uploading recording...';

            // Create capture record
            const captureResponse = await api.createCapture(this.selectedNoteId, audioBlob.type);
            this.currentCaptureId = captureResponse.capture_id;

            // Upload audio
            await api.uploadAudio(captureResponse.upload_url, audioBlob);

            // Complete capture (start processing)
            document.getElementById('status-text').textContent = 'Processing audio...';
            document.getElementById('recording-status').classList.remove('hidden');
            const completedCapture = await api.completeCapture(this.currentCaptureId);

            ui.showToast('Recording uploaded successfully!', 'success');

            // Keep capture id until processing UI finishes; then reset form fields.
            this.showProcessingStatus(completedCapture);
            if (completedCapture.status === 'appended' || completedCapture.status === 'failed') {
                this.resetCaptureForm(false);
            }
            
        } catch (error) {
            console.error('Upload error:', error);
            ui.showToast('Failed to upload recording: ' + error.message, 'error');
            
            // Reset UI but keep the recording
            document.getElementById('recording-status').classList.add('hidden');
            document.getElementById('record-btn').classList.remove('hidden');
            document.getElementById('stop-btn').classList.add('hidden');
        }
    }

    resetCaptureForm(hideStatus = true) {
        // Clear form
        document.getElementById('query-input').value = '';
        document.getElementById('match-results').classList.add('hidden');
        document.getElementById('recording-section').classList.add('hidden');
        if (hideStatus) {
            document.getElementById('recording-status').classList.add('hidden');
        }
        
        // Reset state
        this.selectedNoteId = null;
        this.matchedNotes = [];
        this.audioChunks = [];
        this.quickMode = false;
        document.getElementById('record-btn').classList.remove('hidden');
        document.getElementById('stop-btn').classList.add('hidden');
        if (hideStatus) {
            this.currentCaptureId = null;
        }
    }

    showProcessingStatus(capture) {
        const statusEl = document.getElementById('recording-status');
        statusEl.classList.remove('hidden');
        const statusText = document.getElementById('status-text');
        const progress = document.getElementById('progress');
        
        // Show status based on capture state
        switch (capture.status) {
            case 'uploaded':
                statusText.textContent = 'Audio uploaded, starting transcription...';
                progress.style.width = '25%';
                break;
            case 'transcribed':
                statusText.textContent = 'Transcription complete, cleaning up...';
                progress.style.width = '50%';
                break;
            case 'cleaned':
                statusText.textContent = 'Cleanup complete, adding to note...';
                progress.style.width = '75%';
                break;
            case 'appended':
                statusText.textContent = 'Note saved!';
                progress.style.width = '100%';
                this.currentCaptureId = null;
                // Quick Record: title the note from its transcript and open it.
                if (this.quickNoteId) {
                    const quickNoteId = this.quickNoteId;
                    this.quickNoteId = null;
                    this.quickMode = false;
                    this.autoTitleAndOpen(quickNoteId).catch(() => {});
                }
                setTimeout(() => {
                    statusEl.classList.add('hidden');
                    notes.loadRecentNotes().catch(() => {});
                }, 2000);
                break;
            case 'failed':
                statusText.textContent = 'Processing failed: ' + (capture.error || 'Unknown error');
                progress.style.width = '0%';
                this.currentCaptureId = null;
                this.quickNoteId = null;
                this.quickMode = false;
                ui.showToast('Recording processing failed', 'error');
                break;
        }

        // If still processing, poll status endpoint (idempotent complete as fallback)
        if (['uploaded', 'transcribed', 'cleaned'].includes(capture.status)) {
            setTimeout(() => {
                this.checkCaptureStatus();
            }, 2000);
        }
    }

    async checkCaptureStatus() {
        if (!this.currentCaptureId) return;

        try {
            let capture;
            if (typeof api.getCapture === 'function') {
                capture = await api.getCapture(this.currentCaptureId);
            } else {
                capture = await api.completeCapture(this.currentCaptureId);
            }
            this.showProcessingStatus(capture);
        } catch (error) {
            console.error('Status check error:', error);
            document.getElementById('recording-status').classList.add('hidden');
            this.currentCaptureId = null;
        }
    }
}

// Create and export capture manager instance
const capture = new CaptureManager();
window.capture = capture;