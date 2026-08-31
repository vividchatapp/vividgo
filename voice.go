package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

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

// stripPausePunctuation removes punctuation characters ONLY from text inside
// double quotes (dialogue). This prevents the TTS engine from inserting pauses
// mid-dialogue (e.g. "You're a mess, Sarah?" becomes "You're a mess Sarah")
// while leaving narration and non-quoted text untouched.
// Apostrophes are preserved so contractions like "You're" still work.
func stripPausePunctuation(text string) string {
	// Find all double-quoted sections and strip punctuation from only those
	re := regexp.MustCompile(`"([^"]*)"`)
	return re.ReplaceAllStringFunc(text, func(match string) string {
		// Extract the inner text (without the quotes)
		inner := match[1 : len(match)-1]

		// Remove pause-inducing punctuation from the dialogue text
		// (keep apostrophes for contractions)
		punctRe := regexp.MustCompile(`[.,!?;:()\[\]{}\-—–…]`)
		inner = punctRe.ReplaceAllString(inner, " ")

		// Collapse multiple spaces
		spaceRe := regexp.MustCompile(`\s+`)
		inner = spaceRe.ReplaceAllString(inner, " ")

		// Rebuild with the quotes preserved
		return `"` + strings.TrimSpace(inner) + `"`
	})
}

func parseLocalTTSRate(rate string) int {
	rate = strings.TrimSpace(rate)
	if rate == "" {
		return 0
	}
	if !strings.ContainsAny(rate, "+-0123456789%") {
		return 0
	}
	if !strings.HasSuffix(rate, "%") {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSuffix(rate, "%"))
	if err != nil {
		return 0
	}
	return n / 10
}

func detectAudioExtension(data []byte) string {
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")) {
		return ".wav"
	}
	if len(data) >= 3 && data[0] == 0x49 && data[1] == 0x44 && data[2] == 0x33 {
		return ".mp3"
	}
	if len(data) >= 2 && data[0] == 0xFF && (data[1]&0xE0) == 0xE0 {
		return ".mp3"
	}
	return ".mp3"
}

func generateWindowsLocalTTS(text, rate string) ([]byte, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("local TTS is only supported on Windows")
	}

	cleaned := stripMarkdown(text)
	if cleaned == "" {
		cleaned = "..."
	}

	outputDir, err := os.MkdirTemp("", "vividgo-local-tts-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(outputDir)

	textPath := outputDir + "\\input.txt"
	audioPath := outputDir + "\\output.wav"
	scriptPath := outputDir + "\\synthesize.ps1"
	if err := os.WriteFile(textPath, []byte(cleaned), 0600); err != nil {
		return nil, fmt.Errorf("write temp text: %w", err)
	}

	localRate := parseLocalTTSRate(rate)
	if localRate > 10 {
		localRate = 10
	} else if localRate < -10 {
		localRate = -10
	}

	psScript := "$ErrorActionPreference = \"Stop\"\n" +
		"Add-Type -AssemblyName System.Speech\n" +
		"$synth = New-Object System.Speech.Synthesis.SpeechSynthesizer\n" +
		"$synth.Rate = [int]$args[2]\n" +
		"$synth.SetOutputToWaveFile($args[1])\n" +
		"$synth.Speak([System.IO.File]::ReadAllText($args[0]))\n" +
		"$synth.Dispose()\n"
	if err := os.WriteFile(scriptPath, []byte(psScript), 0600); err != nil {
		return nil, fmt.Errorf("write powershell script: %w", err)
	}

	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-File", scriptPath, textPath, audioPath, strconv.Itoa(localRate))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("powershell local TTS failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	wavBytes, err := os.ReadFile(audioPath)
	if err != nil {
		return nil, fmt.Errorf("read generated WAV: %w", err)
	}
	if len(wavBytes) == 0 {
		return nil, fmt.Errorf("local TTS produced empty WAV output")
	}
	return wavBytes, nil
}

func localWindowsTTSEnabled() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference='Stop'; Add-Type -AssemblyName System.Speech; $s = New-Object System.Speech.Synthesis.SpeechSynthesizer; $s.Dispose(); 'ok'")
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

func generateVoiceBufferWithBackend(text, voice, rate string, stripPunct bool) ([]byte, string, error) {
	cleaned := stripMarkdown(text)
	if stripPunct {
		cleaned = stripPausePunctuation(cleaned)
	}
	if cleaned == "" {
		cleaned = "..."
	}

	if runtime.GOOS == "windows" && localWindowsTTSEnabled() {
		start := time.Now()
		localBytes, localErr := generateWindowsLocalTTS(cleaned, rate)
		if localErr == nil {
			duration := time.Since(start)
			log.Printf("Audio generation: using local Windows SAPI TTS, output=%s, bytes=%d, duration=%s", detectAudioExtension(localBytes), len(localBytes), duration)
			return localBytes, "local", nil
		}
		log.Printf("Audio generation: local Windows SAPI failed after %s, falling back to online Edge TTS: %v", time.Since(start), localErr)
	}

	start := time.Now()
	log.Printf("Audio generation: using online Edge TTS, voice=%s, rate=%s", voice, rate)

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
		return nil, "online", fmt.Errorf("initialization error: %w", err)
	}

	var buf bytes.Buffer
	ctx := context.Background()
	chunkChan, errChan := comm.Stream(ctx)

	for chunk := range chunkChan {
		if chunk.Type == "audio" {
			if _, err := buf.Write(chunk.Data); err != nil {
				return nil, "online", fmt.Errorf("buffer write error: %w", err)
			}
		}
	}

	if err := <-errChan; err != nil {
		return nil, "online", fmt.Errorf("streaming error: %w", err)
	}

	onlineBytes := buf.Bytes()
	duration := time.Since(start)
	log.Printf("Audio generation: online Edge TTS completed, output=%s, bytes=%d, duration=%s", detectAudioExtension(onlineBytes), len(onlineBytes), duration)
	return onlineBytes, "online", nil
}

// GenerateVoiceBuffer converts text to MP3 bytes in memory without touching disk.
// It streams audio chunks directly into a bytes.Buffer in RAM.
// If stripPunct is true, all punctuation is removed before TTS to avoid
// TTS-induced pauses between words.
func GenerateVoiceBuffer(text, voice, rate string, stripPunct bool) ([]byte, error) {
	buf, _, err := generateVoiceBufferWithBackend(text, voice, rate, stripPunct)
	return buf, err
}

// speakToBytes generates MP3 audio from the given text using
// Microsoft Edge's online TTS service, returning the audio in memory.
// No temporary files are written to disk.
// speed is 1-10, where each increment adds +10% to the speech rate.
// If stripPunct is true, all punctuation is removed before TTS to avoid
// TTS-induced pauses between words.
func speakToBytes(text string, speed int, stripPunct bool) ([]byte, error) {
	// Clamp speed to valid range 1-10
	if speed < 1 {
		speed = 1
	}
	if speed > 10 {
		speed = 10
	}
	rate := fmt.Sprintf("+%d%%", speed*10)
	return GenerateVoiceBuffer(text, defaultVoice, rate, stripPunct)
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
	scriptJSON, err := callOllamaAPI(botName, effectiveBase, modelEntry, systemPrompt, nil, userMsg.String(), botParams.NumCtx, botParams.NoThink)
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
	scriptJSON, err := callOllamaAPI(botName, effectiveBase, modelEntry, systemPrompt, nil, userMsg.String(), botParams.NumCtx, botParams.NoThink)
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
	totalStart := time.Now()
	for i, seg := range segments {
		segmentStart := time.Now()
		segBytes, backend, err := generateVoiceBufferWithBackend(seg.Text, seg.Voice, rate, botParams.VoiceStripPunct)
		if err != nil {
			log.Printf("[%s] Segment generation failed for %s after %s: %v", botName, seg.Speaker, time.Since(segmentStart), err)
			sendVoiceAudio(bot, chatID, text, botName, botParams) // fallback to single voice
			return
		}
		combined.Write(segBytes)
		log.Printf("[%s] Character segment %d/%d (%s -> %s) generated via %s in %s, bytes=%d", botName, i+1, len(segments), seg.Speaker, seg.Voice, backend, time.Since(segmentStart), len(segBytes))
	}

	if combined.Len() == 0 {
		log.Printf("[%s] No audio generated for character voice", botName)
		return
	}

	combinedBytes := combined.Bytes()
	fileExt := detectAudioExtension(combinedBytes)
	log.Printf("[%s] Character voice generation complete: %d segments, total duration=%s, output=%s, bytes=%d", botName, len(segments), time.Since(totalStart), fileExt, len(combinedBytes))
	// Send the combined audio file via Telegram using the correct extension for the actual audio format.
	audio := tgbotapi.NewAudio(chatID, tgbotapi.FileBytes{
		Name:  "voice_character" + fileExt,
		Bytes: combinedBytes,
	})
	if sentMsg, err := bot.Send(audio); err != nil {
		log.Printf("[%s] Failed to send character voice audio: %v", botName, err)
	} else {
		log.Printf("[%s] Sent character voice audio (%d segments, message ID %d)", botName, len(segments), sentMsg.MessageID)
	}
}
