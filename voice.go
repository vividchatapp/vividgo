package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"

	"github.com/difyz9/edge-tts-go/pkg/communicate"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// defaultVoice is the Edge TTS voice used when speaking responses.
// Change this to pick a different voice in the future.
const defaultVoice = "en-GB-SoniaNeural"

// VoiceSegment represents a single spoken line with an assigned voice.
// Returned by the LLM when character voice mode converts a story into a script.
type VoiceSegment struct {
	Speaker string `json:"speaker"`
	Voice   string `json:"voice"`
	Text    string `json:"text"`
}

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

// parseVoiceSegments converts an LLM JSON response into a slice of voice segments.
// It strips any markdown code fences that the model might have added.
func parseVoiceSegments(raw string) ([]VoiceSegment, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty LLM response")
	}
	// Strip ```json ... ``` fences if the model ignored instructions
	re := regexp.MustCompile("(?s)^```(?:json)?\\s*\\n?(.*?)\\n?```$")
	if m := re.FindStringSubmatch(raw); m != nil {
		raw = strings.TrimSpace(m[1])
	}
	var segments []VoiceSegment
	if err := json.Unmarshal([]byte(raw), &segments); err != nil {
		return nil, fmt.Errorf("failed to parse voice segments: %w", err)
	}
	// Filter out empty/blank segments
	valid := segments[:0]
	for _, s := range segments {
		if strings.TrimSpace(s.Text) != "" {
			valid = append(valid, s)
		}
	}
	if len(valid) == 0 {
		return nil, fmt.Errorf("no valid text segments in LLM response")
	}
	return valid, nil
}

// voiceHintText formats the stored character→voice assignments as a hint block
// prepended to the LLM prompt so characters keep consistent voices across turns.
func voiceHintText(assignments map[string]string) string {
	if len(assignments) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Known character voice assignments, use these EXACT voices for these characters:\n")
	for name, voice := range assignments {
		fmt.Fprintf(&sb, "- %s: %s\n", name, voice)
	}
	return sb.String()
}

// formatVoiceAssignments formats the character→voice assignments map as a
// human-readable, sorted list suitable for display in a Telegram message.
// Each entry is prefixed with a 1-based index so users can reference characters
// in commands like "voice change [n] [accent]".
// Returns a friendly message when there are no assignments yet.
func formatVoiceAssignments(assignments map[string]string) string {
	if len(assignments) == 0 {
		return "No character voice assignments yet. Assignments are created when character voice mode is used."
	}
	var sb strings.Builder
	sb.WriteString("Character voice assignments:\n")
	// Sort keys for consistent output
	var names []string
	for name := range assignments {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		fmt.Fprintf(&sb, "  %d) %s: %s\n", i+1, name, assignments[name])
	}
	return sb.String()
}

// GetVoiceAssignmentsDisplay returns a readable summary of the current
// character-to-voice mapping for display in the Telegram UI.
func GetVoiceAssignmentsDisplay(assignments map[string]string) string {
	return formatVoiceAssignments(assignments)
}

// SyncVoiceAssignments copies a runtime assignment map into the persisted bot params.
func SyncVoiceAssignments(botParams *BotParams, assignments map[string]string) {
	if botParams == nil {
		return
	}
	if len(assignments) == 0 {
		botParams.VoiceAssignments = nil
		return
	}
	cloned := make(map[string]string, len(assignments))
	for name, voice := range assignments {
		cloned[name] = voice
	}
	botParams.VoiceAssignments = cloned
}

// ChangeCharacterVoice asks the AI to select an appropriate voice for a character
// based on an accent/description, then updates the character→voice assignment.
// It returns a human-readable confirmation message describing the change.
func ChangeCharacterVoice(botName string, botParams *BotParams, modelEntry ModelEntry, apiBase string, assignments *map[string]string, characterName string, accent string) (string, error) {
	// Seed the assignments map if nil
	if assignments == nil {
		*assignments = make(map[string]string)
	}

	// Build the LLM prompt: use the voiceAssistant role as the system prompt,
	// and ask the model to pick a voice for the character.
	systemPrompt := loadRoleContent("voiceAssistant")

	var userMsg strings.Builder
	userMsg.WriteString("Select an appropriate voice for the character below.\n\n")
	userMsg.WriteString(fmt.Sprintf("Character: %s\n", characterName))
	userMsg.WriteString(fmt.Sprintf("Desired accent/description: %s\n\n", accent))
	userMsg.WriteString("Respond ONLY with a valid JSON array containing a single object in this exact format:\n")
	userMsg.WriteString(`[{"speaker": "Character Name", "voice": "SELECTED_VOICE", "text": "A short sample line spoken by this character."}]`)

	// Call the LLM with NO conversation context
	effectiveBase := getEffectiveAPIBase(modelEntry, apiBase)
	scriptJSON, err := callOllamaAPI(effectiveBase, modelEntry, systemPrompt, nil, userMsg.String(), botParams.NumCtx, botParams.NoThink)
	if err != nil {
		return "", fmt.Errorf("voice selection LLM call failed: %w", err)
	}

	segments, err := parseVoiceSegments(scriptJSON)
	if err != nil {
		return "", fmt.Errorf("voice selection parse failed: %w", err)
	}
	if len(segments) == 0 {
		return "", fmt.Errorf("AI returned no voice selection")
	}

	// Use the first segment's voice as the new assignment
	newVoice := strings.TrimSpace(segments[0].Voice)
	if newVoice == "" {
		return "", fmt.Errorf("AI returned an empty voice")
	}

	// Update the assignment map
	(*assignments)[characterName] = newVoice
	SyncVoiceAssignments(botParams, *assignments)
	if botParams != nil {
		saveBotParams(botName, *botParams)
	}

	return fmt.Sprintf("Voice for '%s' changed to: %s", characterName, newVoice), nil
}

// sendVoiceCharacterAudio converts the latest story text into a multi-voice
// spoken script using the roles/voiceAssistant.txt system prompt. It sends the
// merged audio as a single Telegram file and tracks character→voice assignments
// in-memory so the same character keeps the same voice on future turns.
func sendVoiceCharacterAudio(bot *tgbotapi.BotAPI, chatID int64, text string, botName string, botParams *BotParams, modelEntry ModelEntry, apiBase string, assignments *map[string]string, ledger *MessageLedger) {
	if !botParams.Voice {
		return
	}

	// Warn the user that multi-voice generation can take longer.
	if ledger != nil {
		sendAndScheduleDelete(bot, chatID, "⏳ Voice generation can have longer delays...", ledger)
	}

	log.Printf("[%s] Generating character voice audio...", botName)

	// Seed the assignments map if nil
	if assignments == nil {
		*assignments = make(map[string]string)
	}

	// Build the LLM prompt: use the voiceAssistant role as the system prompt,
	// and send ONLY the latest response text (no chat context).
	systemPrompt := loadRoleContent("voiceAssistant")

	var userMsg strings.Builder
	userMsg.WriteString("Convert the following story/scene into a spoken script:\n\n")
	if hint := voiceHintText(*assignments); hint != "" {
		userMsg.WriteString(hint)
		userMsg.WriteString("\n")
	}
	userMsg.WriteString(text)

	// Call the LLM with NO conversation context (only the current response text)
	// Use the effective API base (per-model override or global default).
	effectiveBase := getEffectiveAPIBase(modelEntry, apiBase)
	scriptJSON, err := callOllamaAPI(effectiveBase, modelEntry, systemPrompt, nil, userMsg.String(), botParams.NumCtx, botParams.NoThink)
	if err != nil {
		log.Printf("[%s] Character voice LLM call failed: %v", botName, err)
		sendVoiceAudio(bot, chatID, text, botName, botParams) // fallback to single voice
		return
	}

	segments, err := parseVoiceSegments(scriptJSON)
	if err != nil {
		log.Printf("[%s] Character voice parse failed: %v", botName, err)
		sendVoiceAudio(bot, chatID, text, botName, botParams) // fallback to single voice
		return
	}

	// Reconcile voices: keep previously-assigned voices for known characters,
	// and remember newly-assigned ones for future turns.
	for i := range segments {
		name := strings.TrimSpace(segments[i].Speaker)
		if name == "" {
			name = "Narrator"
			segments[i].Speaker = name
		}
		if stored, ok := (*assignments)[name]; ok && stored != "" {
			// Force the stored voice so the character always sounds the same
			segments[i].Voice = stored
		} else if segments[i].Voice != "" {
			// First time seeing this speaker — remember the LLM-chosen voice
			(*assignments)[name] = segments[i].Voice
		} else {
			// No voice assigned by the LLM; fall back to the default narrator voice
			segments[i].Voice = defaultVoice
			(*assignments)[name] = defaultVoice
		}
	}
	SyncVoiceAssignments(botParams, *assignments)
	if botParams != nil {
		saveBotParams(botName, *botParams)
	}

	// Debug: print the accent/voice chosen for each character
	for i, seg := range segments {
		log.Printf("[%s] Character %d accent chosen: %s -> %s", botName, i+1, seg.Speaker, seg.Voice)
	}

	// Clamp speed and build the rate string
	speed := botParams.VoiceSpeed
	if speed < 1 {
		speed = 1
	}
	if speed > 10 {
		speed = 10
	}
	rate := fmt.Sprintf("+%d%%", speed*10)

	// Generate MP3 for each segment and concatenate into one combined audio file
	var combined bytes.Buffer
	for _, seg := range segments {
		segBytes, err := GenerateVoiceBuffer(seg.Text, seg.Voice, rate)
		if err != nil {
			log.Printf("[%s] Segment generation failed for %s: %v", botName, seg.Speaker, err)
			sendVoiceAudio(bot, chatID, text, botName, botParams) // fallback to single voice
			return
		}
		combined.Write(segBytes)
	}

	if combined.Len() == 0 {
		log.Printf("[%s] No audio generated for character voice", botName)
		return
	}

	// Send the combined MP3 as one audio file via Telegram
	audio := tgbotapi.NewAudio(chatID, tgbotapi.FileBytes{
		Name:  "voice_character.mp3",
		Bytes: combined.Bytes(),
	})
	if sentMsg, err := bot.Send(audio); err != nil {
		log.Printf("[%s] Failed to send character voice audio: %v", botName, err)
	} else {
		log.Printf("[%s] Sent character voice audio (%d segments, message ID %d)", botName, len(segments), sentMsg.MessageID)
	}
}
