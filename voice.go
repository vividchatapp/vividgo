package main

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/difyz9/edge-tts-go/pkg/communicate"
)

// defaultVoice is the Edge TTS voice used when speaking responses.
// Change this to pick a different voice in the future.
const defaultVoice = "en-GB-SoniaNeural"

// stripMarkdown removes common markdown formatting from text.
// It keeps the actual content (bold/italic/link text) and removes the syntax.
func stripMarkdown(text string) string {
	// Remove bold/italic markers: **text**, *text*, __text__, _text_
	re := regexp.MustCompile(`(\*\*|__)(.*?)(\*\*|__)`)
	text = re.ReplaceAllString(text, "$2")

	re = regexp.MustCompile(`(\*|_)(.*?)(\*|_)`)
	text = re.ReplaceAllString(text, "$2")

	// Remove inline code: `text`
	re = regexp.MustCompile("`([^`]*)`")
	text = re.ReplaceAllString(text, "$1")

	// Remove code blocks: ```text```
	re = regexp.MustCompile("```[^`]*```")
	text = re.ReplaceAllString(text, "")

	// Remove headers: # text, ## text, etc.
	re = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+`)
	text = re.ReplaceAllString(text, "")

	// Remove links: [text](url)
	re = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	text = re.ReplaceAllString(text, "$1")

	// Remove images: ![alt](url)
	re = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	text = re.ReplaceAllString(text, "$1")

	// Remove blockquotes: > text
	re = regexp.MustCompile(`(?m)^\s{0,3}>\s?`)
	text = re.ReplaceAllString(text, "")

	// Remove horizontal rules: ---, ***, ___
	re = regexp.MustCompile(`(?m)^\s{0,3}((-{3,})|(\*{3,})|(_{3,}))\s*$`)
	text = re.ReplaceAllString(text, "")

	// Remove list markers: - item, * item, + item, 1. item
	re = regexp.MustCompile(`(?m)^\s{0,3}([-*+]|\d+\.)\s+`)
	text = re.ReplaceAllString(text, "")

	// Remove strikethrough: ~~text~~
	re = regexp.MustCompile(`~~(.*?)~~`)
	text = re.ReplaceAllString(text, "$1")

	// Clean up any leftover markdown symbols and collapse multiple spaces
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "__", "")
	text = strings.ReplaceAll(text, "*", "")
	text = strings.ReplaceAll(text, "_", "")
	text = strings.ReplaceAll(text, "`", "")
	text = strings.ReplaceAll(text, "~~", "")

	// Collapse multiple spaces and trim
	re = regexp.MustCompile(`\s+`)
	text = re.ReplaceAllString(text, " ")

	return strings.TrimSpace(text)
}

// GenerateVoiceBuffer converts text to MP3 bytes in memory without touching disk.
// It streams audio chunks directly into a bytes.Buffer in RAM.
// This is a universal, cross-platform method that works on all devices
// (Windows, Linux, macOS, etc.) via Microsoft Edge's online TTS service.
func GenerateVoiceBuffer(text, voice, rate string) ([]byte, error) {
	cleaned := stripMarkdown(text)
	if cleaned == "" {
		cleaned = "..."
	}

	comm, err := communicate.NewCommunicate(
		cleaned,
		voice,
		rate,
		"+0%",
		"+0Hz",
		"",
		10,
		60,
	)
	if err != nil {
		return nil, fmt.Errorf("initialization error: %w", err)
	}

	var buf bytes.Buffer
	ctx := context.Background()

	// Stream returns a chunk channel and an error channel
	chunkChan, errChan := comm.Stream(ctx)

	// Write audio chunks directly into the memory buffer
	for chunk := range chunkChan {
		if chunk.Type == "audio" {
			if _, err := buf.Write(chunk.Data); err != nil {
				return nil, fmt.Errorf("buffer write error: %w", err)
			}
		}
	}

	// Check for streaming errors
	if err := <-errChan; err != nil {
		return nil, fmt.Errorf("streaming error: %w", err)
	}

	return buf.Bytes(), nil
}

// speakToBytes generates MP3 audio from the given text using
// Microsoft Edge's online TTS service, returning the audio in memory.
// No temporary files are written to disk.
// This is a universal, cross-platform method that works on all devices.
// speed is 1-10, where each increment adds +10% to the speech rate.
func speakToBytes(text string, speed int) ([]byte, error) {
	// Clamp speed to valid range 1-10
	if speed < 1 {
		speed = 1
	}
	if speed > 10 {
		speed = 10
	}
	rate := fmt.Sprintf("+%d%%", speed*10)
	return GenerateVoiceBuffer(text, defaultVoice, rate)
}
