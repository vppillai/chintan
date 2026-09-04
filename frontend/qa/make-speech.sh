#!/usr/bin/env bash
# Builds qa/speech.wav — twelve seconds of synthetic speech Chromium feeds to
# the fake microphone (--use-file-for-fake-audio-capture). Needs espeak-ng and ffmpeg.
set -euo pipefail
cd "$(dirname "$0")"
espeak-ng -v en-us -s 150 -w /tmp/chintan-speech-raw.wav \
    "The gutter on the north side is leaking again near the downpipe. Ask the roofer whether the flashing was replaced last time or just sealed. Bring the ladder round before Saturday so the quote can include the soffit."
ffmpeg -y -loglevel error -i /tmp/chintan-speech-raw.wav -ar 48000 -ac 1 -af "apad=whole_dur=12" -t 12 speech.wav
rm -f /tmp/chintan-speech-raw.wav
echo "wrote $(pwd)/speech.wav"
