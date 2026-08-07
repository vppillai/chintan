// Audio capture and recording functionality
class CaptureManager {
    constructor() {
        this.mediaRecorder = null;
        this.audioChunks = [];
        this.isRecording = false;
        this.selectedNoteId = null;
        this.matchedNotes = [];
        this.currentCaptureId = null;
        
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

    displayMatchResults(result) {
        const resultsContainer = document.getElementById('match-results');
        const candidatesContainer = document.getElementById('match-candidates');
        
        this.matchedNotes = result.candidates || [];

        // Clear previous results
        candidatesContainer.innerHTML = '';

        if (result.auto_select_id) {
            // Auto-selected a note
            this.selectedNoteId = result.auto_select_id;
            const selectedNote = this.matchedNotes.find(n => n.id === result.auto_select_id);
            this.showRecordingSection(selectedNote ? selectedNote.title : 'Selected Note');
        } else {
            // Show candidate selection
            this.matchedNotes.forEach(note => {
                const candidate = document.createElement('div');
                candidate.className = 'match-candidate';
                candidate.dataset.noteId = note.id;
                
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
    }

    selectCandidate(candidateElement, note) {
        // Clear previous selections
        document.querySelectorAll('.match-candidate').forEach(c => {
            c.classList.remove('selected');
        });
        
        // Select this candidate
        candidateElement.classList.add('selected');
        this.selectedNoteId = note.id;
        
        // Show recording section
        this.showRecordingSection(note.title);
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
            this.selectedNoteId = note.id;
            this.showRecordingSection(note.title);
            ui.showToast('Created new note: ' + note.title, 'success');
        } catch (error) {
            console.error('Create note error:', error);
            ui.showToast('Failed to create note: ' + error.message, 'error');
        } finally {
            ui.setButtonLoading(newNoteBtn, false);
        }
    }

    showRecordingSection(noteTitle) {
        document.getElementById('target-note-name').textContent = noteTitle;
        document.getElementById('recording-section').classList.remove('hidden');
        
        // Hide match results
        document.getElementById('match-results').classList.add('hidden');
    }

    async startRecording() {
        if (!this.selectedNoteId) {
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
            recordBtn.classList.remove('hidden');
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

            // Update status
            document.getElementById('status-text').textContent = 'Uploading recording...';

            // Create capture record
            const captureResponse = await api.createCapture(this.selectedNoteId, audioBlob.type);
            this.currentCaptureId = captureResponse.capture_id;

            // Upload audio
            await api.uploadAudio(captureResponse.upload_url, audioBlob);

            // Complete capture (start processing)
            document.getElementById('status-text').textContent = 'Processing audio...';
            const completedCapture = await api.completeCapture(this.currentCaptureId);

            // Show success
            ui.showToast('Recording uploaded successfully!', 'success');
            
            // Reset form
            this.resetCaptureForm();
            
            // Show processing status
            this.showProcessingStatus(completedCapture);
            
        } catch (error) {
            console.error('Upload error:', error);
            ui.showToast('Failed to upload recording: ' + error.message, 'error');
            
            // Reset UI but keep the recording
            document.getElementById('recording-status').classList.add('hidden');
            document.getElementById('record-btn').classList.remove('hidden');
            document.getElementById('stop-btn').classList.add('hidden');
        }
    }

    resetCaptureForm() {
        // Clear form
        document.getElementById('query-input').value = '';
        document.getElementById('match-results').classList.add('hidden');
        document.getElementById('recording-section').classList.add('hidden');
        document.getElementById('recording-status').classList.add('hidden');
        
        // Reset state
        this.selectedNoteId = null;
        this.matchedNotes = [];
        this.currentCaptureId = null;
        this.audioChunks = [];
    }

    showProcessingStatus(capture) {
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
                statusText.textContent = 'Successfully added to note!';
                progress.style.width = '100%';
                setTimeout(() => {
                    document.getElementById('recording-status').classList.add('hidden');
                }, 3000);
                break;
            case 'failed':
                statusText.textContent = 'Processing failed: ' + (capture.error || 'Unknown error');
                progress.style.width = '0%';
                ui.showToast('Recording processing failed', 'error');
                break;
        }

        // If still processing, check status periodically
        if (['uploaded', 'transcribed', 'cleaned'].includes(capture.status)) {
            setTimeout(() => {
                this.checkCaptureStatus();
            }, 3000);
        }
    }

    async checkCaptureStatus() {
        if (!this.currentCaptureId) return;

        try {
            // We don't have a direct status endpoint, so we'll retry the complete call
            // This is idempotent and will return the current status
            const capture = await api.completeCapture(this.currentCaptureId);
            this.showProcessingStatus(capture);
        } catch (error) {
            console.error('Status check error:', error);
            // Don't show error toast for status checks, just stop checking
            document.getElementById('recording-status').classList.add('hidden');
        }
    }
}

// Create and export capture manager instance
const capture = new CaptureManager();
window.capture = capture;