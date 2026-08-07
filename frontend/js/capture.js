// Audio capture and recording functionality
class CaptureManager {
    constructor() {
        this.mediaRecorder = null;
        this.audioChunks = [];
        this.isRecording = false;
        this.selectedNoteId = null;
        this.matchedNotes = [];
        this.currentCaptureId = null;
        // Quick Record path: audio is captured with no note and the destination is
        // decided from the recording itself.
        this.quickMode = false;
        this.openNoteWhenDone = false;
        
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

    // Open → talk → stop → note. The backend transcribes, then decides from what was
    // said whether this belongs in an existing note or a new one.
    async quickFinalize(audioBlob) {
        const statusEl = document.getElementById('recording-status');
        const statusText = document.getElementById('status-text');
        const progress = document.getElementById('progress');
        statusEl.classList.remove('hidden');
        statusText.textContent = 'Uploading recording…';
        progress.style.width = '20%';

        this.openNoteWhenDone = true;

        const captureResponse = await api.createCapture(null, audioBlob.type);
        this.currentCaptureId = captureResponse.capture_id;
        await api.uploadAudio(captureResponse.upload_url, audioBlob);

        statusText.textContent = 'Transcribing…';
        progress.style.width = '50%';
        const completedCapture = await api.completeCapture(this.currentCaptureId);

        this.showProcessingStatus(completedCapture);

        if (completedCapture.status === 'failed') {
            ui.showToast('Could not transcribe audio: ' + (completedCapture.error || 'unknown error'), 'error');
            this.resetCaptureForm(false);
        }
    }

    async openNote(noteId) {
        if (!noteId) return;
        if (window.notes && typeof notes.showNoteDetail === 'function') {
            try {
                await notes.showNoteDetail(noteId);
            } catch (error) {
                console.warn('Open note failed:', error);
            }
        }
    }

    // The router had a guess but wasn't sure, so let the user decide before anything is written.
    async promptForRoutedTarget(capture) {
        const prompt = document.getElementById('target-prompt');
        const options = document.getElementById('target-prompt-options');
        const transcriptEl = document.getElementById('target-prompt-transcript');

        document.getElementById('recording-status').classList.add('hidden');
        prompt.classList.remove('hidden');
        transcriptEl.textContent = '';
        transcriptEl.classList.add('hidden');
        options.innerHTML = '<div class="loading">Loading options…</div>';

        this.showTranscriptPreview(capture.id, transcriptEl).catch(() => {});

        let suggested = null;
        if (capture.suggested_note_id) {
            try {
                suggested = await api.getNote(capture.suggested_note_id);
            } catch (error) {
                console.warn('Could not load suggested note:', error);
            }
        }

        let recent = [];
        try {
            recent = (await api.getNotes()) || [];
        } catch (error) {
            console.warn('Could not load notes:', error);
        }

        options.innerHTML = '';

        if (suggested) {
            const confirmBtn = document.createElement('button');
            confirmBtn.className = 'btn btn-primary btn-full';
            confirmBtn.textContent = `Add to "${suggested.title}"`;
            confirmBtn.addEventListener('click', () => {
                this.applyTarget({ note_id: suggested.id });
            });
            options.appendChild(confirmBtn);
        }

        const newBtn = document.createElement('button');
        newBtn.className = 'btn btn-secondary btn-full';
        newBtn.textContent = 'Save as a new note';
        newBtn.addEventListener('click', () => {
            this.applyTarget({ new_note_title: capture.suggested_title || this.defaultNoteTitle() });
        });
        options.appendChild(newBtn);

        const others = recent.filter(n => !suggested || n.id !== suggested.id).slice(0, 5);
        if (others.length > 0) {
            const label = document.createElement('p');
            label.className = 'target-prompt-label';
            label.textContent = 'Or pick another note:';
            options.appendChild(label);

            others.forEach(note => {
                const btn = document.createElement('button');
                btn.className = 'btn btn-ghost btn-full';
                btn.textContent = note.title;
                btn.addEventListener('click', () => {
                    this.applyTarget({ note_id: note.id });
                });
                options.appendChild(btn);
            });
        }
    }

    async showTranscriptPreview(captureId, element) {
        const { url } = await api.getDownloadUrl(captureId, 'raw');
        const response = await fetch(url);
        if (!response.ok) return;

        const text = (await response.text()).trim();
        if (!text) return;

        element.textContent = `“${text.length > 180 ? text.slice(0, 177) + '…' : text}”`;
        element.classList.remove('hidden');
    }

    async applyTarget(target) {
        const prompt = document.getElementById('target-prompt');
        const captureId = this.currentCaptureId;
        if (!captureId) return;

        prompt.classList.add('hidden');
        document.getElementById('recording-status').classList.remove('hidden');
        document.getElementById('status-text').textContent = 'Saving…';
        document.getElementById('progress').style.width = '75%';

        try {
            const capture = await api.setCaptureTarget(captureId, target);
            this.showProcessingStatus(capture);
        } catch (error) {
            console.error('Set target error:', error);
            ui.showToast('Could not save to that note: ' + error.message, 'error');
            prompt.classList.remove('hidden');
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
        document.getElementById('target-prompt').classList.add('hidden');
        document.getElementById('recording-section').classList.add('hidden');
        if (hideStatus) {
            document.getElementById('recording-status').classList.add('hidden');
        }
        
        // Reset state
        this.selectedNoteId = null;
        this.matchedNotes = [];
        this.audioChunks = [];
        this.quickMode = false;
        this.openNoteWhenDone = false;
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
            case 'needs_target':
                progress.style.width = '60%';
                this.promptForRoutedTarget(capture).catch(error => {
                    console.error('Target prompt error:', error);
                });
                return;
            case 'appended':
                statusText.textContent = 'Note saved!';
                progress.style.width = '100%';
                document.getElementById('target-prompt').classList.add('hidden');
                this.currentCaptureId = null;
                if (this.openNoteWhenDone) {
                    this.openNoteWhenDone = false;
                    this.quickMode = false;
                    this.openNote(capture.note_id).catch(() => {});
                }
                setTimeout(() => {
                    statusEl.classList.add('hidden');
                    notes.loadRecentNotes().catch(() => {});
                }, 2000);
                break;
            case 'failed':
                statusText.textContent = 'Processing failed: ' + (capture.error || 'Unknown error');
                progress.style.width = '0%';
                document.getElementById('target-prompt').classList.add('hidden');
                this.currentCaptureId = null;
                this.openNoteWhenDone = false;
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