package main

import (
	"os"
	"testing"
)

// TestLiveEdgeTTSGeneratesMP3 performs a real network call to Microsoft Edge's
// online TTS service. It is skipped unless VIVIDGO_LIVE_TTS=1 is set so the
// normal unit test run stays offline.
func TestLiveEdgeTTSGeneratesMP3(t *testing.T) {
	if os.Getenv("VIVIDGO_LIVE_TTS") == "" {
		t.Skip("set VIVIDGO_LIVE_TTS=1 to run the live Edge TTS check")
	}

	audio, err := GenerateVoiceBuffer("This is a live Edge TTS check.", defaultVoice, "+20%", false)
	if err != nil {
		t.Fatalf("Edge TTS generation failed: %v", err)
	}
	if len(audio) == 0 {
		t.Fatal("Edge TTS returned no audio bytes")
	}
	ext := detectAudioExtension(audio)
	if ext != ".mp3" {
		t.Fatalf("expected .mp3 output from Edge TTS, got %s", ext)
	}
	t.Logf("Edge TTS voice %s produced %d bytes (%s)", defaultVoice, len(audio), ext)
}
