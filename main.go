package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"gopkg.in/yaml.v3"
)

// Global duration for auto-deleting status/listing messages (default 10 seconds)
var deleteAfterDuration = 20 * time.Second

// logToFileEnabled controls whether logToFile also prints to stdout
var logToFileEnabled = true

// logFile is the path to the file log
const logFilePath = "log.txt"

// filteredModelsCache holds the last result of "model test" (subscription-accessible models).
// Shared across all bot instances. Loaded from config/models_filtered.txt at startup.
var filteredModelsCache []string
var filteredModelsMu sync.Mutex

const filteredModelsFilePath = "config/filtered_models.txt"

// logToFile writes a message to the log file with a timestamp.
// If logToFileEnabled is true, it also prints to stdout via log.Printf.
func logToFile(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("2006/01/02 15:04:05")
	line := fmt.Sprintf("[%s] %s\n", timestamp, msg)

	// Write to file
	f, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Failed to open log file: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		log.Printf("Failed to write to log file: %v", err)
	}

	// Also print to stdout if enabled
	if logToFileEnabled {
		log.Print(msg)
	}
}

// maxTraceFilesPerBot limits how many trace payload files are kept per bot.
const maxTraceFilesPerBot = 10

// traceDir is the root folder for trace payload files.
const traceDir = "context"

// traceMu serializes trace file writes and pruning across bot goroutines.
var traceMu sync.Mutex

// sanitizeFilename makes a string safe to use in a filename/folder name
// on all platforms.
func sanitizeFilename(name string) string {
	safe := strings.NewReplacer(
		"https://", "",
		"http://", "",
		"/", "_",
		"\\", "_",
		";", "_",
		"*", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
	).Replace(name)
	if safe == "" {
		safe = "unknown"
	}
	return safe
}

// pruneTraceFiles removes the oldest trace files in a bot's directory
// beyond maxTraceFilesPerBot.
func pruneTraceFiles(botTraceDir string) {
	entries, err := os.ReadDir(botTraceDir)
	if err != nil {
		return
	}
	if len(entries) <= maxTraceFilesPerBot {
		return
	}

	// Sort by modification time (oldest first)
	sort.Slice(entries, func(i, j int) bool {
		infoI, errI := entries[i].Info()
		infoJ, errJ := entries[j].Info()
		if errI != nil || errJ != nil {
			return false
		}
		return infoI.ModTime().Before(infoJ.ModTime())
	})

	// Delete the oldest files beyond the cap
	toRemove := len(entries) - maxTraceFilesPerBot
	for i := 0; i < toRemove; i++ {
		path := filepath.Join(botTraceDir, entries[i].Name())
		if err := os.Remove(path); err != nil {
			log.Printf("Failed to prune trace file %s: %v", path, err)
		} else {
			log.Printf("Pruned old trace file %s", path)
		}
	}
}

// traceRequestPayload writes the exact LLM request payload (the full context
// sent to the model: system prompt + conversation history + user message)
// to the context/<bot>/ folder when trace mode is enabled.
// Each request gets its own timestamped file so it can be inspected.
// A maximum of maxTraceFilesPerBot are kept per bot; the oldest are pruned.
func traceRequestPayload(botName string, endpoint string, model string, jsonData []byte) {
	if !logToFileEnabled {
		return
	}

	traceMu.Lock()
	defer traceMu.Unlock()

	// Sanitize the bot name so it is safe to use in a folder name on all platforms.
	safeBot := sanitizeFilename(botName)
	botTraceDir := filepath.Join(traceDir, safeBot)
	if err := os.MkdirAll(botTraceDir, 0755); err != nil {
		log.Printf("Failed to create trace directory %s: %v", botTraceDir, err)
		return
	}

	// Wrap the raw request body with metadata so the file is self-contained.
	payload := struct {
		Timestamp string          `json:"timestamp"`
		Endpoint  string          `json:"endpoint"`
		Model     string          `json:"model"`
		Request   json.RawMessage `json:"request"`
	}{
		Timestamp: time.Now().Format(time.RFC3339),
		Endpoint:  endpoint,
		Model:     model,
		Request:   jsonData,
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal trace payload: %v", err)
		return
	}

	// Sanitize the endpoint so it is safe to use in a filename on all platforms.
	safeName := sanitizeFilename(endpoint)
	if safeName == "unknown" {
		safeName = "request"
	}

	path := filepath.Join(botTraceDir, fmt.Sprintf("%s_%d.json", safeName, time.Now().UnixNano()))
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("Failed to write trace payload to %s: %v", path, err)
		return
	}
	log.Printf("Trace payload written to %s", path)

	// Enforce the per-bot file cap by pruning the oldest files.
	pruneTraceFiles(botTraceDir)
}

// onOff returns "on" if b is true, otherwise "off".
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// BotConfig holds the configuration for a single bot
type BotConfig struct {
	Name  string `yaml:"name"`
	Token string `yaml:"token"`
}

// ModelEntry holds a single model configuration with name, API key, and API base URL
type ModelEntry struct {
	Name    string `yaml:"name"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
	APIBase string `yaml:"api_base"`
}

// OllamaConfig holds the Ollama API configuration for online models
type OllamaConfig struct {
	APIBase string       `yaml:"api_base"`
	Models  []ModelEntry `yaml:"models"`
}

// BotParams holds per-bot persistent state loaded/saved to config/<botname>_bot_params.yaml
type BotParams struct {
	SelectedProvider int               `yaml:"selected_provider"`
	CurrentModel     string            `yaml:"current_model"`
	CurrentMode      string            `yaml:"current_mode"`
	CurrentRole      string            `yaml:"current_role"`
	CurrentStory     string            `yaml:"current_story"`
	ActiveScenes     []int             `yaml:"active_scenes"`
	ActiveCharacters []int             `yaml:"active_characters"`
	NumCtx           int               `yaml:"num_ctx"`
	NoThink          bool              `yaml:"nothink"`
	Voice            bool              `yaml:"voice"`
	VoiceSpeed       int               `yaml:"voice_speed"`
	VoiceChar        bool              `yaml:"voice_char"`
	VoiceStripPunct  bool              `yaml:"voice_strip_punct"`
	VoiceAssignments map[string]string `yaml:"voice_assignments"`
}

// Config holds the bot configuration loaded from config.yaml or environment variables
type Config struct {
	Bots   []BotConfig  `yaml:"bots"`
	UserID int64        `yaml:"user_id"`
	Debug  bool         `yaml:"debug"`
	Ollama OllamaConfig `yaml:"ollama"`
}

// OllamaMessage represents a message in the Ollama chat API request
type OllamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OllamaChatRequest represents the request body for Ollama chat API
type OllamaChatRequest struct {
	Model    string                 `json:"model"`
	Messages []OllamaMessage        `json:"messages"`
	Stream   bool                   `json:"stream"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

// OllamaChatResponse represents the response from Ollama chat API
type OllamaChatResponse struct {
	Message OllamaMessage `json:"message"`
	Done    bool          `json:"done"`
}

// ContextMessage represents a single message in conversation history
type ContextMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// LedgerEntryType categorizes a message ID in the ledger
type LedgerEntryType string

const (
	LedgerCommand         LedgerEntryType = "command"
	LedgerCommandResponse LedgerEntryType = "command_response"
	LedgerChatUser        LedgerEntryType = "chat_user"
	LedgerChatAssistant   LedgerEntryType = "chat_assistant"
)

// LedgerEntry stores a message ID with its type category for UI cleanup
type LedgerEntry struct {
	MessageID int             `json:"message_id"`
	Type      LedgerEntryType `json:"type"`
}

// MessageLedger provides a thread-safe store for tracking message IDs per session
type MessageLedger struct {
	mu      sync.Mutex
	entries []LedgerEntry
}

// Add appends a new entry to the ledger
func (l *MessageLedger) Add(id int, entryType LedgerEntryType) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, LedgerEntry{MessageID: id, Type: entryType})
}

// GetByTypes returns all entries matching any of the given types
func (l *MessageLedger) GetByTypes(types ...LedgerEntryType) []LedgerEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	var result []LedgerEntry
	for _, e := range l.entries {
		for _, t := range types {
			if e.Type == t {
				result = append(result, e)
				break
			}
		}
	}
	return result
}

// RemoveByIDs removes all entries whose MessageID is in the given set
func (l *MessageLedger) RemoveByIDs(ids map[int]bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var kept []LedgerEntry
	for _, e := range l.entries {
		if !ids[e.MessageID] {
			kept = append(kept, e)
		}
	}
	l.entries = kept
}

// All returns a copy of all entries in the ledger
func (l *MessageLedger) All() []LedgerEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := make([]LedgerEntry, len(l.entries))
	copy(result, l.entries)
	return result
}

// Clear removes all entries from the ledger
func (l *MessageLedger) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = nil
}

func loadConfig() Config {
	var cfg Config

	// Try loading from config.yaml first
	data, err := os.ReadFile("config.yaml")
	if err == nil {
		if err := yaml.Unmarshal(data, &cfg); err == nil {
			if len(cfg.Bots) > 0 {
				log.Printf("Loaded %d bot(s) from config.yaml", len(cfg.Bots))
				for _, bot := range cfg.Bots {
					log.Printf("  - %s: %s (user %d)", bot.Name, bot.Token[:8]+"...", cfg.UserID)
				}
				if cfg.Ollama.APIBase == "" {
					cfg.Ollama.APIBase = "https://api.ollama.com"
				}
				if len(cfg.Ollama.Models) == 0 {
					cfg.Ollama.Models = []ModelEntry{
						{Name: "default", Model: "gemma4:31b"},
					}
				}
				log.Printf("Ollama config: api_base=%s, %d model(s)", cfg.Ollama.APIBase, len(cfg.Ollama.Models))
				for _, model := range cfg.Ollama.Models {
					log.Printf("  - %s: model=%s, api_base=%s, api_key=%s", model.Name, model.Model, getEffectiveAPIBase(model, cfg.Ollama.APIBase), maskKey(model.APIKey))
				}
				return cfg
			}
		}
	}

	// Fall back to environment variables (single bot)
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	userIDStr := os.Getenv("TELEGRAM_USER_ID")
	if token != "" && userIDStr != "" {
		if id, err := strconv.ParseInt(userIDStr, 10, 64); err == nil {
			cfg.Bots = []BotConfig{
				{
					Name:  "bot1",
					Token: token,
				},
			}
			cfg.UserID = id
			cfg.Debug = os.Getenv("TELEGRAM_DEBUG") == "true"

			// Load Ollama config from environment variables
			cfg.Ollama.APIBase = os.Getenv("OLLAMA_API_BASE")
			if cfg.Ollama.APIBase == "" {
				cfg.Ollama.APIBase = "https://api.ollama.com"
			}
			cfg.Ollama.Models = []ModelEntry{
				{
					Name:   "default",
					APIKey: os.Getenv("OLLAMA_API_KEY"),
					Model:  os.Getenv("OLLAMA_MODEL"),
				},
			}
			if cfg.Ollama.Models[0].Model == "" {
				cfg.Ollama.Models[0].Model = "gemma4:31b"
			}

			log.Println("Loaded configuration from environment variables")
			return cfg
		}
	}

	log.Fatal("No configuration found. Create config.yaml or set TELEGRAM_BOT_TOKEN and TELEGRAM_USER_ID environment variables.")
	return cfg
}

// getEffectiveAPIBase returns the per-model API base if set, otherwise falls back to the global default.
func getEffectiveAPIBase(modelEntry ModelEntry, globalBase string) string {
	if modelEntry.APIBase != "" {
		return modelEntry.APIBase
	}
	return globalBase
}

// maskKey masks the middle portion of an API key for logging
func maskKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// getBotParamsPath returns the path to the bot params file for a given bot name
func getBotParamsPath(botName string) string {
	return fmt.Sprintf("config/%s_bot_params.yaml", botName)
}

// loadBotParams loads bot parameters from disk, returning defaults if the file doesn't exist
func loadBotParams(botName string, defaultModel string) BotParams {
	path := getBotParamsPath(botName)
	data, err := os.ReadFile(path)
	if err != nil {
		fallbackPath := "config/bot_params.yaml"
		log.Printf("[%s] No bot params file found at %s, trying fallback %s", botName, path, fallbackPath)
		data, err = os.ReadFile(fallbackPath)
		if err != nil {
			log.Printf("[%s] No fallback bot params file found at %s, using defaults", botName, fallbackPath)
			return BotParams{
				SelectedProvider: 0,
				CurrentModel:     defaultModel,
				CurrentMode:      "chat",
				CurrentRole:      "Assistant",
				CurrentStory:     "general",
				NumCtx:           8192,
			}
		}
		path = fallbackPath
	}

	var params BotParams
	if err := yaml.Unmarshal(data, &params); err != nil {
		log.Printf("[%s] Failed to parse bot params file %s: %v, using defaults", botName, path, err)
		return BotParams{
			SelectedProvider: 0,
			CurrentModel:     defaultModel,
			CurrentMode:      "chat",
			CurrentRole:      "Assistant",
			CurrentStory:     "general",
			NumCtx:           8192,
		}
	}

	// Ensure CurrentRole has a default if not set (legacy params files)
	if params.CurrentRole == "" {
		params.CurrentRole = "Assistant"
	}
	// Ensure CurrentStory has a default if not set (legacy params files)
	if params.CurrentStory == "" {
		params.CurrentStory = "general"
	}
	// Initialize active scenes and characters slices if nil (legacy params files)
	if params.ActiveScenes == nil {
		params.ActiveScenes = []int{}
	}
	if params.ActiveCharacters == nil {
		params.ActiveCharacters = []int{}
	}
	// Set default num_ctx if not set (legacy params files)
	if params.NumCtx == 0 {
		params.NumCtx = 8192
	}
	// Set default voice speed if not set (legacy params files)
	if params.VoiceSpeed == 0 {
		params.VoiceSpeed = 2
	}
	// Preserve punctuation by default; set voice_strip_punct: true to opt into
	// the legacy behavior for quoted dialogue.
	if !params.VoiceStripPunct && !strings.Contains(string(data), "voice_strip_punct") {
		params.VoiceStripPunct = false
	}

	log.Printf("[%s] Loaded bot params from %s: provider=%d, model=%s, mode=%s, role=%s, story=%s, active_scenes=%v, active_characters=%v", botName, path, params.SelectedProvider, params.CurrentModel, params.CurrentMode, params.CurrentRole, params.CurrentStory, params.ActiveScenes, params.ActiveCharacters)
	return params
}

// saveBotParams saves bot parameters to disk
func saveBotParams(botName string, params BotParams) {
	path := getBotParamsPath(botName)
	data, err := yaml.Marshal(&params)
	if err != nil {
		log.Printf("[%s] Failed to marshal bot params: %v", botName, err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("[%s] Failed to save bot params to %s: %v", botName, path, err)
		return
	}
	log.Printf("[%s] Saved bot params to %s: provider=%d, model=%s, mode=%s, role=%s", botName, path, params.SelectedProvider, params.CurrentModel, params.CurrentMode, params.CurrentRole)
}

// loadRoleContent reads a role text file from the roles/ directory.
// If the file doesn't exist, returns the default system prompt.
func loadRoleContent(roleName string) string {
	path := fmt.Sprintf("roles/%s.txt", roleName)
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("Role file %s not found, using default prompt", path)
		return "You are a helpful assistant. Respond concisely and accurately."
	}
	return strings.TrimSpace(string(data))
}

// listRoleFiles returns a sorted list of role names (filenames without .txt extension)
func listRoleFiles() ([]string, error) {
	entries, err := os.ReadDir("roles")
	if err != nil {
		return nil, fmt.Errorf("failed to read roles directory: %w", err)
	}
	var roles []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".txt") && !entry.IsDir() {
			roles = append(roles, strings.TrimSuffix(name, ".txt"))
		}
	}
	sort.Strings(roles)
	return roles, nil
}

// listStoryFolders returns a sorted list of story folder names under stories/
func listStoryFolders() ([]string, error) {
	entries, err := os.ReadDir("stories")
	if err != nil {
		return nil, fmt.Errorf("failed to read stories directory: %w", err)
	}
	var stories []string
	for _, entry := range entries {
		if entry.IsDir() {
			stories = append(stories, entry.Name())
		}
	}
	sort.Strings(stories)
	return stories, nil
}

// getChatLogPath returns the path to the chat log file for a given bot and story.
// It ensures the chat/ subdirectory exists.
func getChatLogPath(storyName, botName string) string {
	chatDir := fmt.Sprintf("stories/%s/chat", storyName)
	// Ensure the chat directory exists
	if err := os.MkdirAll(chatDir, 0755); err != nil {
		log.Printf("Failed to create chat directory %s: %v", chatDir, err)
	}
	return fmt.Sprintf("%s/%s_chat_history.txt", chatDir, botName)
}

// appendToChatLog appends a single message block to the chat log file.
// Uses os.O_APPEND for efficient writes (no full file rewrite).
func appendToChatLog(path, role, content string) {
	// Determine the label based on role
	label := role // "user" or "assistant"
	block := fmt.Sprintf("%s -----------\n%s\n\n", label, content)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Failed to open chat log %s: %v", path, err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(block); err != nil {
		log.Printf("Failed to write to chat log %s: %v", path, err)
	}
}

// rewriteChatLogFile truncates the chat log file and rewrites the entire conversation
// history from the given context. Used on /del to sync file with trimmed memory.
func rewriteChatLogFile(path string, context []ContextMessage, mode string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open chat log for rewrite %s: %w", path, err)
	}
	defer f.Close()

	for _, msg := range context {
		if mode == "chat" {
			block := fmt.Sprintf("%s -----------\n%s\n\n", msg.Role, msg.Content)
			if _, err := f.WriteString(block); err != nil {
				return fmt.Errorf("failed to write to chat log %s: %w", path, err)
			}
		} else if msg.Role == "assistant" {
			block := fmt.Sprintf("assistant -----------\n%s\n\n", msg.Content)
			if _, err := f.WriteString(block); err != nil {
				return fmt.Errorf("failed to write to chat log %s: %w", path, err)
			}
		}
	}
	return nil
}

// rewriteChatLog truncates the chat log file and rewrites the entire conversation
// history from the given context. Used on /del to sync file with trimmed memory.
func rewriteChatLog(path string, context []ContextMessage, mode string) {
	if err := rewriteChatLogFile(path, context, mode); err != nil {
		log.Printf("%v", err)
	}
}

func getStorySnapshotPath(storyName, botName, snapshotName string) string {
	chatDir := fmt.Sprintf("stories/%s/chat", storyName)
	if err := os.MkdirAll(chatDir, 0755); err != nil {
		log.Printf("Failed to create chat directory %s: %v", chatDir, err)
	}
	name := strings.TrimSpace(snapshotName)
	if name == "" {
		name = "autosave"
	}
	safe := sanitizeFilename(name)
	return fmt.Sprintf("%s/%s_story_%s.txt", chatDir, botName, safe)
}

func saveStorySnapshot(storyName, botName string, context []ContextMessage, mode string) error {
	return rewriteChatLogFile(getStorySnapshotPath(storyName, botName, "autosave"), context, mode)
}

func loadStorySnapshot(storyName, botName, mode string) ([]ContextMessage, error) {
	path := getStorySnapshotPath(storyName, botName, "autosave")
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return loadChatHistory(path), nil
}

func listStorySnapshots(storyName, botName string) ([]string, error) {
	chatDir := fmt.Sprintf("stories/%s/chat", storyName)
	entries, err := os.ReadDir(chatDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to read story snapshots for %s: %w", storyName, err)
	}

	prefix := botName + "_story_"
	suffix := ".txt"
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) {
			trimmed := strings.TrimSuffix(name, suffix)
			trimmed = strings.TrimPrefix(trimmed, prefix)
			if trimmed != "" {
				names = append(names, trimmed)
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

func loadThreadSnapshotByIndex(storyName, botName string, index int) ([]ContextMessage, error) {
	snapshots, err := listStorySnapshots(storyName, botName)
	if err != nil {
		return nil, err
	}
	if len(snapshots) == 0 {
		return nil, fmt.Errorf("no saved threads found in story %s", storyName)
	}
	if index < 1 || index > len(snapshots) {
		return nil, fmt.Errorf("invalid thread number. choose 1-%d", len(snapshots))
	}
	path := getStorySnapshotPath(storyName, botName, snapshots[index-1])
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return loadChatHistory(path), nil
}

// loadChatHistory reads a chat history file and parses it into a slice of ContextMessage.
// The file format is:
//
//	user -----------
//	message content
//
//	assistant -----------
//	message content
//
// Lines starting with "user -----------" or "assistant -----------" are role markers.
// Content between markers is collected as the message body.
func loadChatHistory(path string) []ContextMessage {
	data, err := os.ReadFile(path)
	if err != nil {
		// File doesn't exist yet — that's fine, return empty
		return nil
	}

	lines := strings.Split(string(data), "\n")
	var messages []ContextMessage
	var currentRole string
	var currentContent strings.Builder

	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		// Check if this line is a role marker: "user -----------" or "assistant -----------"
		if strings.HasPrefix(trimmed, "user ") && strings.Contains(trimmed, "-----------") {
			// If we were building a previous message, save it
			if currentRole != "" {
				content := strings.TrimSpace(currentContent.String())
				if content != "" {
					messages = append(messages, ContextMessage{Role: currentRole, Content: content})
				}
				currentContent.Reset()
			}
			currentRole = "user"
			continue
		}
		if strings.HasPrefix(trimmed, "assistant ") && strings.Contains(trimmed, "-----------") {
			// If we were building a previous message, save it
			if currentRole != "" {
				content := strings.TrimSpace(currentContent.String())
				if content != "" {
					messages = append(messages, ContextMessage{Role: currentRole, Content: content})
				}
				currentContent.Reset()
			}
			currentRole = "assistant"
			continue
		}
		// If we're inside a message block, accumulate content
		if currentRole != "" {
			if currentContent.Len() > 0 {
				currentContent.WriteString("\n")
			}
			currentContent.WriteString(trimmed)
		}
	}

	// Don't forget the last message
	if currentRole != "" {
		content := strings.TrimSpace(currentContent.String())
		if content != "" {
			messages = append(messages, ContextMessage{Role: currentRole, Content: content})
		}
	}

	return messages
}

// listSceneFiles returns a sorted list of scene file names (without extension) for a given story
func listSceneFiles(story string) ([]string, error) {
	path := fmt.Sprintf("stories/%s/scenes", story)
	entries, err := os.ReadDir(path)
	if err != nil {
		// Gracefully return empty list if the directory doesn't exist
		log.Printf("Scenes directory %s not found: %v", path, err)
		return []string{}, nil
	}
	var scenes []string
	for _, entry := range entries {
		if !entry.IsDir() {
			scenes = append(scenes, entry.Name())
		}
	}
	sort.Strings(scenes)
	return scenes, nil
}

// listCharacterFiles returns a sorted list of character file names for a given story
func listCharacterFiles(story string) ([]string, error) {
	path := fmt.Sprintf("stories/%s/characters", story)
	entries, err := os.ReadDir(path)
	if err != nil {
		// Gracefully return empty list if the directory doesn't exist
		log.Printf("Characters directory %s not found: %v", path, err)
		return []string{}, nil
	}
	var chars []string
	for _, entry := range entries {
		if !entry.IsDir() {
			chars = append(chars, entry.Name())
		}
	}
	sort.Strings(chars)
	return chars, nil
}

// loadStoryContent reads a file from a story's scenes or characters directory
func loadStoryContent(story, subdir, name string) string {
	path := fmt.Sprintf("stories/%s/%s/%s", story, subdir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("Story file %s not found: %v", path, err)
		return ""
	}
	return strings.TrimSpace(string(data))
}

// displayName converts an internal story/folder name to a human-readable display name.
// It replaces underscores and hyphens with spaces.
func displayName(name string) string {
	replacer := strings.NewReplacer("-", " ", "_", " ")
	return replacer.Replace(name)
}

// escapeMarkdown escapes special characters for Telegram MarkdownV2
func escapeMarkdown(text string) string {
	replacer := strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)
	return replacer.Replace(text)
}

// OllamaModel represents a model from the Ollama API list endpoint
type OllamaModel struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// OllamaModelsResponse represents the response from /api/tags
type OllamaModelsResponse struct {
	Models []OllamaModel `json:"models"`
}

// formatModelSize converts a model size in bytes to a human-readable GB string.
// Returns an empty string if size is unknown (<= 0).
func formatModelSize(size int64) string {
	if size <= 0 {
		return ""
	}
	gb := float64(size) / (1024 * 1024 * 1024)
	return fmt.Sprintf("%.1fGB", gb)
}

// listOllamaModels fetches available models from the Ollama API
func listOllamaModels(apiBase string, apiKey string) ([]OllamaModel, error) {
	baseURL := strings.TrimRight(apiBase, "/")
	endpoint := baseURL + "/api/tags"

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch models: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var modelsResp OllamaModelsResponse
	if err := json.Unmarshal(body, &modelsResp); err != nil {
		return nil, fmt.Errorf("failed to parse models: %w", err)
	}

	return modelsResp.Models, nil
}

// isOllamaDotCom returns true if the apiBase points to api.ollama.com.
// Only ollama.com models need subscription testing; all other providers are always "ok".
func isOllamaDotCom(apiBase string) bool {
	return strings.Contains(apiBase, "api.ollama.com")
}

// testModelAccess sends a minimal chat request to a model to check if it's accessible
// (not blocked by subscription/upgrade requirements). Returns true if accessible.
// For non-ollama.com providers, this always returns true since there's no subscription model.
func testModelAccess(apiBase string, modelEntry ModelEntry, modelName string) bool {
	// Local/other providers are always accessible; only api.ollama.com has subscription gating
	if !isOllamaDotCom(apiBase) {
		return true
	}
	baseURL := strings.TrimRight(apiBase, "/")
	endpoint := baseURL + "/api/chat"

	// Minimal payload: just a "hello how are you" message to test access
	messages := []OllamaMessage{
		{Role: "user", Content: "hello how are you"},
	}

	reqBody := OllamaChatRequest{
		Model:    modelName,
		Messages: messages,
		Stream:   false,
		Options:  map[string]interface{}{"num_ctx": 2048},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return false
	}

	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	if modelEntry.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+modelEntry.APIKey)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	// Read the full response body to check for subscription message
	body, _ := io.ReadAll(resp.Body)
	bodyStr := strings.ToLower(string(body))

	// If the response contains "this model requires a subscription", it's blocked
	if strings.Contains(bodyStr, "this model requires a subscription") {
		return false
	}

	// If we get a 200 and no subscription message, the model is accessible
	if resp.StatusCode == http.StatusOK {
		return true
	}

	// Any other error (e.g. 404 model not found) — treat as inaccessible
	return false
}

// loadFilteredModels reads the filtered models list from disk into the global cache
func loadFilteredModels() {
	filteredModelsMu.Lock()
	defer filteredModelsMu.Unlock()

	data, err := os.ReadFile(filteredModelsFilePath)
	if err != nil {
		log.Printf("No filtered models file at %s, cache will be empty", filteredModelsFilePath)
		filteredModelsCache = nil
		return
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	filteredModelsCache = nil
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			filteredModelsCache = append(filteredModelsCache, trimmed)
		}
	}
	log.Printf("Loaded %d filtered models from %s", len(filteredModelsCache), filteredModelsFilePath)
}

// saveFilteredModels writes the filtered models list to disk and updates the global cache
func saveFilteredModels(models []string) {
	filteredModelsMu.Lock()
	defer filteredModelsMu.Unlock()

	content := strings.Join(models, "\n")
	if err := os.WriteFile(filteredModelsFilePath, []byte(content), 0644); err != nil {
		log.Printf("Failed to save filtered models to %s: %v", filteredModelsFilePath, err)
		return
	}
	filteredModelsCache = models
	log.Printf("Saved %d filtered models to %s", len(models), filteredModelsFilePath)
}

// callOllamaAPI sends a chat request to the Ollama API and returns the response text
func callOllamaAPI(botName string, apiBase string, modelEntry ModelEntry, systemPrompt string, context []ContextMessage, userMessage string, numCtx int, noThink bool) (string, error) {
	// Determine the endpoint URL
	// Use the native Ollama API endpoint /api/chat
	baseURL := strings.TrimRight(apiBase, "/")
	endpoint := baseURL + "/api/chat"

	// Build the request using native Ollama format
	messages := []OllamaMessage{
		{
			Role:    "system",
			Content: systemPrompt,
		},
	}
	for _, msg := range context {
		messages = append(messages, OllamaMessage{Role: msg.Role, Content: msg.Content})
	}
	messages = append(messages, OllamaMessage{Role: "user", Content: userMessage})

	options := map[string]interface{}{
		"num_ctx": numCtx,
	}
	if noThink {
		options["nothink"] = true
	}

	reqBody := OllamaChatRequest{
		Model:    modelEntry.Model,
		Messages: messages,
		Stream:   false,
		Options:  options,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	if logToFileEnabled {
		logToFile("[Ollama Request] Endpoint: %s", endpoint)
		logToFile("[Ollama Request] Model: %s", modelEntry.Model)
	}

	// When trace mode is on, save the exact payload sent to the LLM
	// (system prompt + full context + user message) to the context/ folder.
	traceRequestPayload(botName, endpoint, modelEntry.Model, jsonData)

	// Create HTTP request
	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if modelEntry.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+modelEntry.APIKey)
	}

	// Send request with timeout (300s for local CPU prefill with large context windows)
	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// If /api/chat fails with 405, try the OpenAI-compatible endpoint as fallback
		if resp.StatusCode == http.StatusMethodNotAllowed {
			fallbackEndpoint := baseURL + "/v1/chat/completions"
			return callOllamaAPIOpenAI(botName, fallbackEndpoint, modelEntry, systemPrompt, context, userMessage)
		}
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response as native Ollama format
	var ollamaResp OllamaChatResponse
	if err := json.Unmarshal(body, &ollamaResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if ollamaResp.Message.Content == "" {
		return "", fmt.Errorf("API returned empty response")
	}

	return strings.TrimSpace(ollamaResp.Message.Content), nil
}

// callOllamaAPIOpenAI sends a chat request using the OpenAI-compatible endpoint format
func callOllamaAPIOpenAI(botName string, endpoint string, modelEntry ModelEntry, systemPrompt string, context []ContextMessage, userMessage string) (string, error) {
	// Build the request in OpenAI-compatible format
	messages := []OllamaMessage{
		{
			Role:    "system",
			Content: systemPrompt,
		},
	}
	for _, msg := range context {
		messages = append(messages, OllamaMessage{Role: msg.Role, Content: msg.Content})
	}
	messages = append(messages, OllamaMessage{Role: "user", Content: userMessage})

	reqBody := struct {
		Model    string          `json:"model"`
		Messages []OllamaMessage `json:"messages"`
		Stream   bool            `json:"stream"`
	}{
		Model:    modelEntry.Model,
		Messages: messages,
		Stream:   false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// When trace mode is on, save the exact payload sent to the LLM
	// (system prompt + full context + user message) to the context/ folder.
	traceRequestPayload(botName, endpoint, modelEntry.Model, jsonData)

	// Create HTTP request
	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if modelEntry.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+modelEntry.APIKey)
	}

	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse OpenAI-compatible response
	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(body, &chatResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("API returned no choices")
	}

	return strings.TrimSpace(chatResp.Choices[0].Message.Content), nil
}

// Section headers for structured prompt construction
const (
	sectionContextLabel = "### ACTIVE CONTEXT:\n"
	sectionUserInput    = "### RAW USER INPUT TO EMBELLISH AND CONTINUE:\n"
	sectionDirective    = "### EXECUTION DIRECTIVE:\n"
)

// defaultExecutionDirective returns the standard instruction appended to every prompt.
func defaultExecutionDirective() string {
	return "Absorb the raw user input above. Apply the 'Story Weaver' rules. " +
		"Progress the narrative seamlessly using vivid prose and immersive action. " +
		"Do not break character. Do not reply with meta-commentary."
}

// buildPrompt assembles a three-part prompt: injected context, raw user text,
// and an execution directive. Returns empty string when userText is blank.
func buildPrompt(injectedParts []string, userText string, directive string) string {
	if userText == "" {
		return ""
	}

	// Pre-allocate capacity: user text + directive + section headers + injected parts overhead
	estSize := len(userText) + len(directive) + len(sectionContextLabel) + len(sectionUserInput) + len(sectionDirective) + 20
	for _, p := range injectedParts {
		estSize += len(p) + 2
	}

	var b strings.Builder
	b.Grow(estSize)

	// 1. Inject active scenes/characters with proper separators between parts
	if len(injectedParts) > 0 {
		b.WriteString(sectionContextLabel)
		for i, part := range injectedParts {
			if i > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(part)
		}
		b.WriteString("\n\n")
	}

	// 2. Raw user input section
	b.WriteString(sectionUserInput)
	b.WriteString(userText)
	b.WriteString("\n\n")

	// 3. Execution directive — the last thing the model sees before generating
	b.WriteString(sectionDirective)
	b.WriteString(directive)

	return b.String()
}

// runBot starts a single Telegram bot and listens for updates

// sendChunkedMessage sends a message with chunking support and records chunks in the ledger.
// Returns the last sent message ID, or 0 if all sends failed.
func sendChunkedMessage(bot *tgbotapi.BotAPI, chatID int64, text string, ledger *MessageLedger) int {
	// Use a safer maximum length for MarkdownV2 to account for escape characters
	const maxMessageLength = 3500
	remainingText := text
	var lastMsgID int

	for len(remainingText) > 0 {
		chunk := remainingText
		if len(chunk) > maxMessageLength {
			chunk = chunk[:maxMessageLength]
			// Try to break nicely at a newline or space instead of mid-word
			if lastSpace := strings.LastIndexAny(chunk, " \n"); lastSpace > 2500 {
				chunk = chunk[:lastSpace]
			}
		}

		respMsg := tgbotapi.NewMessage(chatID, chunk)
		respMsg.ParseMode = tgbotapi.ModeMarkdownV2

		sentMsg, err := bot.Send(respMsg)
		if err != nil {
			log.Printf("Failed to send message with MarkdownV2: %v. Retrying as plain text...", err)
			respMsg.ParseMode = ""
			sentMsg, err = bot.Send(respMsg)
			if err != nil {
				log.Printf("Critical failure sending message: %v", err)
				return lastMsgID
			}
		}

		ledger.Add(sentMsg.MessageID, LedgerCommandResponse)
		lastMsgID = sentMsg.MessageID
		remainingText = remainingText[len(chunk):]
	}
	return lastMsgID
}

// sendAndScheduleDelete sends a message, records it in the ledger as command_response,
// and schedules its deletion after deleteAfterDuration
func sendAndScheduleDelete(bot *tgbotapi.BotAPI, chatID int64, text string, ledger *MessageLedger) {
	lastMsgID := sendChunkedMessage(bot, chatID, text, ledger)
	if lastMsgID == 0 {
		return
	}
	// Schedule deletion for all chunks sent by sendChunkedMessage
	// We schedule deletion for the last chunk; all chunks share the same deletion timer
	// by iterating ledger entries for this send
	entries := ledger.All()
	var chunkIDs []int
	for _, e := range entries {
		if e.Type == LedgerCommandResponse {
			chunkIDs = append(chunkIDs, e.MessageID)
		}
	}
	// Only schedule deletion for the chunks we just sent (the last batch)
	// A simpler approach: schedule deletion for each chunk individually
	// Since we can't easily distinguish which entries belong to this call,
	// we schedule deletion for the last message ID (the final chunk)
	go func(cid int64, mid int) {
		time.Sleep(deleteAfterDuration)
		deleteMsg := tgbotapi.NewDeleteMessage(cid, mid)
		if _, err := bot.Request(deleteMsg); err != nil {
			log.Printf("Failed to delete disappearing message: %v", err)
		}
	}(chatID, lastMsgID)
}

// safeDeleteWithLedger attempts to delete a message by ID, logging errors via logToFile.
// It always marks the ID in idsToRemove so the caller can track what to purge from the ledger.
func safeDeleteWithLedger(bot *tgbotapi.BotAPI, chatID int64, messageID int, idsToRemove map[int]bool) {
	deleteMsg := tgbotapi.NewDeleteMessage(chatID, messageID)
	if _, err := bot.Request(deleteMsg); err != nil {
		logToFile("[clean] Failed to delete message %d: %v", messageID, err)
	}
	idsToRemove[messageID] = true
}

// sendAndTrack sends a plain text message, adds it to the ledger, and returns the sent message.
func sendAndTrack(bot *tgbotapi.BotAPI, chatID int64, text string, ledger *MessageLedger) *tgbotapi.Message {
	respMsg := tgbotapi.NewMessage(chatID, text)
	sentMsg, err := bot.Send(respMsg)
	if err != nil {
		log.Printf("Failed to send message: %v", err)
		return nil
	}
	ledger.Add(sentMsg.MessageID, LedgerCommandResponse)
	return &sentMsg
}

// deleteTrackedMessage attempts to delete a message by ID, logging errors.
// Returns true if the delete request succeeded (or message was already deleted).
func deleteTrackedMessage(bot *tgbotapi.BotAPI, chatID int64, messageID int) bool {
	deleteMsg := tgbotapi.NewDeleteMessage(chatID, messageID)
	if _, err := bot.Request(deleteMsg); err != nil {
		log.Printf("Failed to delete status message %d: %v", messageID, err)
		return false
	}
	return true
}

// sendVoiceAudio generates MP3 audio from the given text using edge-tts-go
// entirely in memory (no temp files) and sends it as an audio file to the chat,
// if voice is enabled in botParams.
// It logs errors but does not return them, so the caller's text flow is never interrupted.
func sendVoiceAudio(bot *tgbotapi.BotAPI, chatID int64, text string, botName string, botParams *BotParams) {
	if !botParams.Voice {
		return
	}

	log.Printf("[%s] Generating voice audio (speed %d)...", botName, botParams.VoiceSpeed)
	audioBytes, err := speakToBytes(text, botParams.VoiceSpeed, botParams.VoiceStripPunct)
	if err != nil {
		log.Printf("[%s] Failed to generate voice audio: %v", botName, err)
		return
	}

	fileExt := detectAudioExtension(audioBytes)
	// Send the generated audio as a Telegram audio attachment using the actual codec, not a guessed MP3 extension.
	audio := tgbotapi.NewAudio(chatID, tgbotapi.FileBytes{
		Name:  "voice" + fileExt,
		Bytes: audioBytes,
	})
	if sentMsg, err := bot.Send(audio); err != nil {
		log.Printf("[%s] Failed to send voice audio: %v", botName, err)
	} else {
		log.Printf("[%s] Sent voice audio (message ID %d)", botName, sentMsg.MessageID)
	}
}

func runBot(botCfg BotConfig, userID int64, ollamaCfg OllamaConfig, wg *sync.WaitGroup) {
	defer wg.Done()

	bot, err := tgbotapi.NewBotAPI(botCfg.Token)
	if err != nil {
		log.Printf("[%s] Failed to start bot: %v", botCfg.Name, err)
		return
	}

	bot.Debug = false // can be overridden from config if needed

	log.Printf("[%s] Authorized on account %s", botCfg.Name, bot.Self.UserName)

	// Create a new update config with long polling timeout
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 180

	updates := bot.GetUpdatesChan(u)

	// Load persisted bot params (selected provider, model, mode) from config/<botname>_bot_params.yaml
	defaultModel := ollamaCfg.Models[0].Model
	botParams := loadBotParams(botCfg.Name, defaultModel)

	// Validate selectedProvider against available models
	selectedProvider := botParams.SelectedProvider
	if selectedProvider < 0 || selectedProvider >= len(ollamaCfg.Models) {
		selectedProvider = 0
	}

	// Apply the loaded model to the selected provider
	if botParams.CurrentModel != "" {
		ollamaCfg.Models[selectedProvider].Model = botParams.CurrentModel
	}

	// Track the current mode: "chat" or "story"
	currentMode := botParams.CurrentMode
	if currentMode != "chat" && currentMode != "story" {
		currentMode = "chat"
	}

	// Initialize the message ledger for tracking and UI cleanup
	var ledger MessageLedger

	// Track the last user message ID and assistant response IDs for /del Telegram UI cleanup
	var lastUserMsgID int
	var lastAssistantMsgIDs []int
	var lastUserText string

	// Initialize voice character assignments (maps speaker name to assigned voice)
	voiceCharAssignments := make(map[string]string)
	// Restore voice assignments from persisted params so they survive restarts
	if botParams.VoiceAssignments != nil {
		for k, v := range botParams.VoiceAssignments {
			voiceCharAssignments[k] = v
		}
	}
	SyncVoiceAssignments(&botParams, voiceCharAssignments)

	// Initialize conversation context
	var conversationContext []ContextMessage
	contextLimit := 50

	// Load chat history from disk to restore conversation context
	chatLogPath := getChatLogPath(botParams.CurrentStory, botCfg.Name)
	loadedHistory := loadChatHistory(chatLogPath)
	if len(loadedHistory) > 0 {
		conversationContext = loadedHistory
		log.Printf("[%s] Loaded %d messages from chat history: %s", botCfg.Name, len(loadedHistory), chatLogPath)
	}

	// Initialize story/scene/character state
	currentStory := botParams.CurrentStory
	if currentStory == "" {
		currentStory = "general"
	}
	activeScenes := make(map[int]bool)
	activeCharacters := make(map[int]bool)

	// Restore active scenes and characters from botParams
	for _, sceneIdx := range botParams.ActiveScenes {
		activeScenes[sceneIdx] = true
	}
	for _, charIdx := range botParams.ActiveCharacters {
		activeCharacters[charIdx] = true
	}

	log.Printf("[%s] Initialized with provider=%d, mode=%s, model=%s, story=%s", botCfg.Name, selectedProvider, currentMode, botParams.CurrentModel, currentStory)

	// estimateTokens provides a rough estimate of token count from the context messages
	estimateTokens := func(ctx []ContextMessage) int {
		total := 0
		for _, msg := range ctx {
			total += len(msg.Content)
		}
		return total / 4 // rough estimate: ~4 chars per token
	}

	// sendContextWarning sends a disappearing warning if the context is getting too large
	sendContextWarning := func(ctx []ContextMessage, numCtx int, botName string, chatID int64) {
		if numCtx <= 0 {
			return
		}
		estimatedTokens := estimateTokens(ctx)
		if estimatedTokens == 0 {
			return
		}
		// Warn if we're above 85% of the context limit
		threshold := int(float64(numCtx) * 0.85)
		if estimatedTokens >= threshold {
			ctxK := numCtx / 1024
			warning := fmt.Sprintf("⚠️ You are reaching the context limit with your chat/story. Consider increasing it from %dK (%d) using .ctxsize [nk]", ctxK, numCtx)
			log.Printf("[%s] Context warning: estimated %d tokens of %d limit", botName, estimatedTokens, numCtx)
			respMsg := tgbotapi.NewMessage(chatID, warning)
			if sentMsg, err := bot.Send(respMsg); err == nil {
				ledger.Add(sentMsg.MessageID, LedgerCommandResponse)
				go func(cid int64, mid int) {
					time.Sleep(deleteAfterDuration)
					deleteMsg := tgbotapi.NewDeleteMessage(cid, mid)
					if _, err := bot.Request(deleteMsg); err != nil {
						log.Printf("[%s] Failed to delete warning message: %v", botName, err)
					}
				}(sentMsg.Chat.ID, sentMsg.MessageID)
			}
		}
	}

	for update := range updates {
		if update.Message == nil || update.Message.Text == "" {
			continue
		}

		// Only respond to the configured user ID
		if update.Message.From.ID == userID {
			log.Printf("[%s] Received message from user %d: %s", botCfg.Name, userID, update.Message.Text)

			// Parse command from the first word of the message, strip leading dot
			parts := strings.Fields(update.Message.Text)
			command := strings.TrimPrefix(strings.ToLower(parts[0]), ".")

			// Capture the incoming message ID in the ledger
			// Determine if it's a command (starts with a dot) or chat_user
			if strings.HasPrefix(parts[0], ".") {
				ledger.Add(update.Message.MessageID, LedgerCommand)
			} else {
				ledger.Add(update.Message.MessageID, LedgerChatUser)
				// Track the last non-command user message ID for /del UI cleanup
				lastUserMsgID = update.Message.MessageID
			}

			// Guard: "nothink" / "no" shortcut should only be treated as a command
			// when the message starts with ".", or has no other arguments, or has
			// valid nothink arguments (on/off/think). If there are other words,
			// send it to the AI chat handler (default case).
			if !strings.HasPrefix(parts[0], ".") && (command == "nothink" || command == "no") {
				if len(parts) > 1 {
					firstArg := strings.ToLower(parts[1])
					if firstArg != "think" && firstArg != "on" && firstArg != "off" {
						command = "" // force to default/AI handler
					}
				}
			}

			var responseText string
			// voiceThisResponse is set to true for AI-generated responses (chat and resend),
			// so sendVoiceAudio is called after the text is sent. Command responses are not spoken.
			voiceThisResponse := false

			switch command {
			case "help", "h":
				responseText = `🤖 i5 Assistant Help
📚 Stories & Threads
• story - List story folders
• story [n] - Select story folder (clears scenes/chars)
• thread - List saved threads in the current story
• thread save [name] - Save the current chat as a named thread
• thread load [n] - Load a saved thread by number

👤 Characters
• char - List characters in current story
• char 1 -2 3 - Multi-toggle (prefix - to deactivate)
• char all off - Deactivate all characters
• char edit [n] - Show character bio
• char save [name] [text] - Save character bio

🎦 Scenes
• scene - List scenes in current story
• scene 1 -2 3 - Multi-toggle (prefix - to deactivate)
• scene all off - Deactivate all scenes
• scene edit [n] - Show scene description
• scene save [name] [text] - Save scene description

🎭 Roles
• role - List available roles
• role [n] - Switch to role
• role edit [n] - Get role text to edit
• role save [name] [text] - Save role profile
• reload - Reload role from disk
• rs [n] - Summarize current role

🧠 AI Providers & Models
• provider / p - List providers
• provider [n] / p [n] - Switch provider
• model - List available models
• model [n] - Switch model
• model + - Next model (won't go past last)
• model - - Previous model (won't go below first)
• model test - Test online models for subscription requirements
• mf [n/next/prev] - List or cycle accessible models
• model loaded - Sync with RAM
• think - Toggle Think Mode
• nothink [on/off] - Toggle NoThink flag for Ollama API
• no think [on/off] - Same as nothink

• llmctx [nk] - Set/show model context window (e.g. 8k)

💾 Conversation & Chats
• chat save [name] - Save conversation
• chat load [name] - Load conversation
• clear - Wipe bot memory (context)
• clean - Wipe memory and delete messages from chat UI
• mode [chat/story] - Toggle history behavior
• recap [n] - Summarize last 50 msgs using prompt n
• ask [text] - Ask a question outside of roleplay context
• del - Delete last msg + response
• resend - Resend last user message
• context [n] - Set/show limit
• history [n] [full] [keep] - Show last n interactions (default 5, trunc 100 chars). Add "keep" to prevent auto-deletion

⚙️ Status & Shortcuts
• status - Show current settings
• verbose - Toggle Verbose Status
• trace [on/off] - Write payloads to context folder
	• voice [on/off] - Speak AI responses as audio
	• voice speed [1-10] - Set voice speed (10% increments)
	• voice char [on/off] - Multi-voice character narration
	• voice list - Show character voice assignments
	• voice change [n] [accent] - Change a character's voice (e.g. .voice change 2 deep southern accent)
Synonyms: r=role, rs=rolesummary, p=provider, m=model, s=status, h=help, c=chat, cl=clean, mf=modelsfiltered, sc=scene, hs=history, mo=mode, mc=llmctx`

			case "provider", "p":
				if len(parts) == 1 {
					// Just "provider" - list available models with numbers
					var sb strings.Builder
					sb.WriteString("Available providers:\n")
					sortedModels := make([]ModelEntry, len(ollamaCfg.Models))
					copy(sortedModels, ollamaCfg.Models)
					sort.Slice(sortedModels, func(i, j int) bool {
						return sortedModels[i].Name < sortedModels[j].Name
					})
					for i, model := range sortedModels {
						if model.Name == ollamaCfg.Models[selectedProvider].Name {
							sb.WriteString(fmt.Sprintf("  %d) %s ✅\n", i+1, model.Name))
						} else {
							sb.WriteString(fmt.Sprintf("  %d) %s\n", i+1, model.Name))
						}
					}
					responseText = sb.String()
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				} else if len(parts) == 2 {
					// "provider <number>" - select a provider
					num, err := strconv.Atoi(parts[1])
					if err != nil || num < 1 || num > len(ollamaCfg.Models) {
						responseText = fmt.Sprintf("Invalid provider number. Please choose 1-%d.", len(ollamaCfg.Models))
					} else {
						// Sort providers alphabetically by name first so the numbering
						// matches the listing shown by "provider" (without arguments).
						sortedIndices := make([]int, len(ollamaCfg.Models))
						for i := range sortedIndices {
							sortedIndices[i] = i
						}
						sort.Slice(sortedIndices, func(i, j int) bool {
							return ollamaCfg.Models[sortedIndices[i]].Name < ollamaCfg.Models[sortedIndices[j]].Name
						})
						selectedProvider = sortedIndices[num-1]
						responseText = fmt.Sprintf("Selected provider: %s", ollamaCfg.Models[selectedProvider].Name)

						// Persist the provider selection
						botParams.SelectedProvider = selectedProvider
						botParams.CurrentModel = ollamaCfg.Models[selectedProvider].Model
						saveBotParams(botCfg.Name, botParams)

						// Send the response and schedule deletion after 10 seconds
						respMsg := tgbotapi.NewMessage(update.Message.Chat.ID, responseText)
						respMsg.ParseMode = tgbotapi.ModeMarkdown
						sentMsg, err := bot.Send(respMsg)
						if err != nil {
							log.Printf("[%s] Failed to send provider message: %v", botCfg.Name, err)
						} else {
							ledger.Add(sentMsg.MessageID, LedgerCommandResponse)
							log.Printf("[%s] Sent provider response to user %d", botCfg.Name, userID)
							// Schedule deletion after deleteAfterDuration
							go func(chatID int64, messageID int) {
								time.Sleep(deleteAfterDuration)
								deleteMsg := tgbotapi.NewDeleteMessage(chatID, messageID)
								if _, err := bot.Request(deleteMsg); err != nil {
									log.Printf("[%s] Failed to delete provider message: %v", botCfg.Name, err)
								}
							}(sentMsg.Chat.ID, sentMsg.MessageID)
						}
						continue // Skip the default response sending below
					}
				} else {
					responseText = "Usage: provider [number] - list or select a provider"
				}

			case "mode":
				if len(parts) == 2 {
					mode := strings.ToLower(parts[1])
					if mode == "story" || mode == "chat" {
						currentMode = mode
						responseText = fmt.Sprintf("%s mode enabled", mode)

						// Persist the mode selection
						botParams.CurrentMode = currentMode
						saveBotParams(botCfg.Name, botParams)

						// Send the response and schedule deletion after deleteAfterDuration
						respMsg := tgbotapi.NewMessage(update.Message.Chat.ID, responseText)
						respMsg.ParseMode = tgbotapi.ModeMarkdown
						sentMsg, err := bot.Send(respMsg)
						if err != nil {
							log.Printf("[%s] Failed to send mode message: %v", botCfg.Name, err)
						} else {
							ledger.Add(sentMsg.MessageID, LedgerCommandResponse)
							log.Printf("[%s] Sent mode response to user %d", botCfg.Name, userID)
							// Schedule deletion after deleteAfterDuration
							go func(chatID int64, messageID int) {
								time.Sleep(deleteAfterDuration)
								deleteMsg := tgbotapi.NewDeleteMessage(chatID, messageID)
								if _, err := bot.Request(deleteMsg); err != nil {
									log.Printf("[%s] Failed to delete mode message: %v", botCfg.Name, err)
								}
							}(sentMsg.Chat.ID, sentMsg.MessageID)
						}
						continue // Skip the default response sending below
					} else {
						responseText = "Invalid mode. Use: mode chat or mode story"
						sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
						continue
					}
				} else {
					responseText = fmt.Sprintf("Current mode: %s\nUsage: mode [chat/story]", currentMode)
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				}

			case "role", "r":
				if len(parts) == 1 {
					// List available roles
					roles, err := listRoleFiles()
					if err != nil {
						responseText = fmt.Sprintf("Error listing roles: %v", err)
					} else {
						var sb strings.Builder
						sb.WriteString("Available roles:\n")
						for i, role := range roles {
							if role == botParams.CurrentRole {
								sb.WriteString(fmt.Sprintf("  %d) %s ✅\n", i+1, role))
							} else {
								sb.WriteString(fmt.Sprintf("  %d) %s\n", i+1, role))
							}
						}
						responseText = sb.String()
					}
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				} else if len(parts) >= 2 && parts[1] == "edit" {
					// role edit [n] - show the role text
					if len(parts) == 3 {
						num, err := strconv.Atoi(parts[2])
						if err != nil {
							responseText = "Invalid role number."
						} else {
							roles, err := listRoleFiles()
							if err != nil {
								responseText = fmt.Sprintf("Error listing roles: %v", err)
							} else if num < 1 || num > len(roles) {
								responseText = fmt.Sprintf("Invalid role number. Please choose 1-%d.", len(roles))
							} else {
								roleName := roles[num-1]
								content := loadRoleContent(roleName)
								responseText = fmt.Sprintf("Role: %s\n\n%s", roleName, content)
							}
						}
					} else {
						responseText = "Usage: role edit [n] - show role text"
					}
				} else if len(parts) >= 3 && parts[1] == "save" {
					// role save [name] [text] - save a new role
					roleName := parts[2]
					// The rest of the message after "role save [name]" is the content
					contentStart := len("role save " + roleName + " ")
					if len(update.Message.Text) <= contentStart {
						responseText = "Usage: role save [name] [text] - provide role content after the name"
					} else {
						content := update.Message.Text[contentStart:]
						path := fmt.Sprintf("roles/%s.txt", roleName)
						if err := os.WriteFile(path, []byte(content), 0644); err != nil {
							responseText = fmt.Sprintf("Failed to save role: %v", err)
						} else {
							responseText = fmt.Sprintf("Role '%s' saved.", roleName)
						}
					}
				} else if len(parts) == 2 {
					// role [n] - switch to role by number
					num, err := strconv.Atoi(parts[1])
					if err != nil {
						responseText = "Invalid role number."
					} else {
						roles, err := listRoleFiles()
						if err != nil {
							responseText = fmt.Sprintf("Error listing roles: %v", err)
						} else if num < 1 || num > len(roles) {
							responseText = fmt.Sprintf("Invalid role number. Please choose 1-%d.", len(roles))
						} else {
							roleName := roles[num-1]
							botParams.CurrentRole = roleName
							saveBotParams(botCfg.Name, botParams)
							responseText = fmt.Sprintf("Switched to role: %s", roleName)

							// Send the response and schedule deletion after deleteAfterDuration
							respMsg := tgbotapi.NewMessage(update.Message.Chat.ID, responseText)
							respMsg.ParseMode = tgbotapi.ModeMarkdown
							sentMsg, err := bot.Send(respMsg)
							if err != nil {
								log.Printf("[%s] Failed to send role message: %v", botCfg.Name, err)
							} else {
								ledger.Add(sentMsg.MessageID, LedgerCommandResponse)
								log.Printf("[%s] Sent role response to user %d", botCfg.Name, userID)
								// Schedule deletion after deleteAfterDuration
								go func(chatID int64, messageID int) {
									time.Sleep(deleteAfterDuration)
									deleteMsg := tgbotapi.NewDeleteMessage(chatID, messageID)
									if _, err := bot.Request(deleteMsg); err != nil {
										log.Printf("[%s] Failed to delete role message: %v", botCfg.Name, err)
									}
								}(sentMsg.Chat.ID, sentMsg.MessageID)
							}
							continue // Skip the default response sending below
						}
					}
				} else {
					responseText = "Usage: role [n] - list or select a role"
				}

			case "reload":
				// Reload the current role from disk (just confirm it exists)
				content := loadRoleContent(botParams.CurrentRole)
				responseText = fmt.Sprintf("Role '%s' reloaded (%d characters).", botParams.CurrentRole, len(content))
				sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
				continue

			case "clear":
				// Clear conversation context only (no UI cleanup)
				conversationContext = nil
				// Sync the chat log file with the empty context (truncate)
				chatLogPath := getChatLogPath(currentStory, botCfg.Name)
				rewriteChatLog(chatLogPath, conversationContext, currentMode)
				// Clear character voice assignments from memory and persist to YAML
				voiceCharAssignments = make(map[string]string)
				SyncVoiceAssignments(&botParams, voiceCharAssignments)
				saveBotParams(botCfg.Name, botParams)
				responseText = "Conversation context cleared."
				sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
				continue

			case "clean":
				if len(parts) >= 2 && parts[1] == "cmd" {
					// clean [cmd] - Surgical UI wipe: delete only command + command_response messages
					// Do NOT touch conversationContext - LLM brain stays intact
					entries := ledger.GetByTypes(LedgerCommand, LedgerCommandResponse)
					idsToRemove := make(map[int]bool)
					for _, e := range entries {
						safeDeleteWithLedger(bot, update.Message.Chat.ID, e.MessageID, idsToRemove)
					}
					ledger.RemoveByIDs(idsToRemove)
					responseText = "Command interface cleaned. Conversation memory preserved."
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				}
				// clean (no args) - Full hard reset: delete ALL messages, clear ledger, reset context
				allEntries := ledger.All()
				idsToRemove := make(map[int]bool)
				for _, e := range allEntries {
					safeDeleteWithLedger(bot, update.Message.Chat.ID, e.MessageID, idsToRemove)
				}
				ledger.Clear()
				conversationContext = nil
				voiceCharAssignments = make(map[string]string)
				SyncVoiceAssignments(&botParams, voiceCharAssignments)
				saveBotParams(botCfg.Name, botParams)
				responseText = "Full cleanup complete. All messages deleted and memory wiped."
				sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
				continue

			case "rs":
				// Summarize current role
				content := loadRoleContent(botParams.CurrentRole)
				lines := strings.Split(content, "\n")
				lineCount := len(lines)
				charCount := len(content)
				// Show first 200 chars as preview
				preview := content
				if len(preview) > 200 {
					preview = preview[:200] + "..."
				}
				responseText = fmt.Sprintf("Role: %s\nLines: %d\nChars: %d\n\n%s", botParams.CurrentRole, lineCount, charCount, preview)
				sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
				continue

			case "del":
				// Delete the tracked Telegram messages from UI first
				idsToRemove := make(map[int]bool)
				if lastUserMsgID > 0 {
					safeDeleteWithLedger(bot, update.Message.Chat.ID, lastUserMsgID, idsToRemove)
				}
				for _, aid := range lastAssistantMsgIDs {
					safeDeleteWithLedger(bot, update.Message.Chat.ID, aid, idsToRemove)
				}
				ledger.RemoveByIDs(idsToRemove)
				// Reset tracking
				lastUserMsgID = 0
				lastAssistantMsgIDs = nil

				// Delete last user+assistant exchange from memory
				if len(conversationContext) == 0 {
					responseText = "No messages to delete."
				} else if currentMode == "chat" {
					if len(conversationContext) < 2 {
						responseText = "Not enough messages to delete a pair."
					} else {
						conversationContext = conversationContext[:len(conversationContext)-2]
						responseText = "Last exchange deleted."
					}
				} else {
					// Story mode: delete last assistant message only
					conversationContext = conversationContext[:len(conversationContext)-1]
					responseText = "Last assistant message deleted."
				}
				// Sync the chat log file with the trimmed context
				chatLogPath := getChatLogPath(currentStory, botCfg.Name)
				rewriteChatLog(chatLogPath, conversationContext, currentMode)
				sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
				continue

			case "ctxsize", "ctx":
				if len(parts) == 2 {
					arg := strings.ToLower(parts[1])
					if strings.HasSuffix(arg, "k") {
						num, err := strconv.Atoi(strings.TrimSuffix(arg, "k"))
						if err != nil || num < 1 {
							responseText = "Invalid context size. Use a number ending in k (e.g. 8k, 16k, 32k)."
						} else {
							botParams.NumCtx = num * 1024
							saveBotParams(botCfg.Name, botParams)
							responseText = fmt.Sprintf("Context set to %dK (%d)", num, botParams.NumCtx)
						}
					} else {
						responseText = "Invalid format. Use a number ending in k (e.g. 8k, 16k, 32k)."
					}
				} else {
					ctxK := botParams.NumCtx / 1024
					responseText = fmt.Sprintf("Current context size: %dK (%d)\nUsage: .ctxsize [nk] - set the model context window (e.g. 8k, 16k, 32k)", ctxK, botParams.NumCtx)
				}
				sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
				continue

			case "context":
				if len(parts) == 2 {
					num, err := strconv.Atoi(parts[1])
					if err != nil || num < 1 {
						responseText = "Invalid context limit. Please provide a positive number."
					} else {
						contextLimit = num
						responseText = fmt.Sprintf("Context limit set to %d messages.", contextLimit)
					}
				} else {
					responseText = fmt.Sprintf("Current context limit: %d messages.\nUsage: .context [n] - set the maximum number of messages to keep in context.", contextLimit)
				}
				sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
				continue

			case "history", "hs":
				// Parse arguments: history [n] [full] [keep]
				keepMsg := false
				showFull := false
				numLines := 5
				if len(parts) >= 2 {
					if n, err := strconv.Atoi(parts[1]); err == nil {
						numLines = n
					}
				}
				if len(parts) >= 3 {
					for _, arg := range parts[2:] {
						if arg == "full" {
							showFull = true
						} else if arg == "keep" {
							keepMsg = true
						}
					}
				}

				if len(conversationContext) == 0 {
					responseText = "No conversation history."
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				}

				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("📜 History (%d total messages, showing last %d):\n", len(conversationContext), numLines*2))
				startIdx := len(conversationContext) - numLines*2
				if startIdx < 0 {
					startIdx = 0
				}
				for i := startIdx; i < len(conversationContext); i++ {
					msg := conversationContext[i]
					content := msg.Content
					if !showFull && len(content) > 100 {
						content = content[:100] + "..."
					}
					sb.WriteString(fmt.Sprintf("%d) [%s] %s\n", i+1, msg.Role, content))
				}
				responseText = sb.String()

				if keepMsg {
					// Send without scheduling deletion
					respMsg := tgbotapi.NewMessage(update.Message.Chat.ID, responseText)
					respMsg.ParseMode = tgbotapi.ModeMarkdownV2
					sentMsg, err := bot.Send(respMsg)
					if err != nil {
						log.Printf("[%s] Failed to send history message: %v", botCfg.Name, err)
						// Retry as plain text
						respMsg.ParseMode = ""
						sentMsg, err = bot.Send(respMsg)
						if err != nil {
							log.Printf("[%s] Critical failure sending history message: %v", botCfg.Name, err)
						}
					}
					if err == nil {
						ledger.Add(sentMsg.MessageID, LedgerCommandResponse)
					}
				} else {
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
				}
				continue

			case "status", "s":
				// Build a verbose status showing all current settings
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("👤 User: %d\n", userID))

				// Provider and model info
				modelName := ollamaCfg.Models[selectedProvider].Model
				sb.WriteString(fmt.Sprintf("🧠 Provider: %s\n", ollamaCfg.Models[selectedProvider].Name))
				sb.WriteString(fmt.Sprintf("🤖 Model: %s\n", modelName))
				ctxK := botParams.NumCtx / 1024
				sb.WriteString(fmt.Sprintf("📐 Context: %dK (%d)\n", ctxK, botParams.NumCtx))

				// Current role
				sb.WriteString(fmt.Sprintf("🎭 Role: %s\n", botParams.CurrentRole))

				// Mode
				sb.WriteString(fmt.Sprintf("📝 Mode: %s\n", currentMode))

				// Story info
				sb.WriteString(fmt.Sprintf("📖 Story: %s\n", displayName(currentStory)))

				// Active scenes
				scenes, _ := listSceneFiles(currentStory)
				var activeSceneNames []string
				for idx := range activeScenes {
					if idx >= 1 && idx <= len(scenes) {
						activeSceneNames = append(activeSceneNames, scenes[idx-1])
					}
				}
				if len(activeSceneNames) > 0 {
					sb.WriteString(fmt.Sprintf("🎦 Scenes: %s\n", strings.Join(activeSceneNames, ", ")))
				}

				// Active characters
				chars, _ := listCharacterFiles(currentStory)
				var activeCharNames []string
				for idx := range activeCharacters {
					if idx >= 1 && idx <= len(chars) {
						activeCharNames = append(activeCharNames, strings.TrimSuffix(chars[idx-1], ".txt"))
					}
				}
				if len(activeCharNames) > 0 {
					sb.WriteString(fmt.Sprintf("👥 Characters: %s\n", strings.Join(activeCharNames, ", ")))
				}

				// Conversation stats
				sb.WriteString(fmt.Sprintf("💬 Memory: %d messages (limit %d)\n", len(conversationContext), contextLimit))

				// Not think flag
				if botParams.NoThink {
					sb.WriteString("🚫 NoThink: enabled\n")
				}

				// Voice flag
				if botParams.Voice {
					sb.WriteString(fmt.Sprintf("🔊 Voice: enabled (speed %d)", botParams.VoiceSpeed))
					if botParams.VoiceChar {
						sb.WriteString(" [char mode]")
					}
					sb.WriteString("\n")
				}

				responseText = sb.String()
				sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
				continue

			case "think":
				newVal := !botParams.NoThink
				if newVal {
					botParams.NoThink = true
					responseText = "Think mode disabled (NoThink enabled)."
				} else {
					botParams.NoThink = false
					responseText = "Think mode enabled (NoThink disabled)."
				}
				saveBotParams(botCfg.Name, botParams)
				sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
				continue

			case "nothink", "no":
				if len(parts) >= 2 {
					arg := strings.ToLower(parts[1])
					if arg == "on" {
						botParams.NoThink = true
						saveBotParams(botCfg.Name, botParams)
						responseText = "NoThink enabled."
					} else if arg == "off" || arg == "think" {
						botParams.NoThink = false
						saveBotParams(botCfg.Name, botParams)
						responseText = "NoThink disabled."
					} else {
						responseText = fmt.Sprintf("Current NoThink: %v\nUsage: nothink [on/off] or no think [on/off]", botParams.NoThink)
					}
				} else {
					responseText = fmt.Sprintf("Current NoThink: %v\nUsage: nothink [on/off] or no think [on/off]", botParams.NoThink)
				}
				sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
				continue

			case "verbose":
				verboseMode := !logToFileEnabled
				logToFileEnabled = verboseMode
				if verboseMode {
					responseText = "Verbose logging enabled."
				} else {
					responseText = "Verbose logging disabled."
				}
				sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
				continue

			case "voice":
				// Voice output control: .voice on / .voice off / .voice speed <1-10> / .voice char [on/off]
				if len(parts) >= 3 && strings.ToLower(parts[1]) == "speed" {
					// .voice speed <1-10> - set voice speed (10% increments)
					speed, err := strconv.Atoi(parts[2])
					if err != nil || speed < 1 || speed > 10 {
						responseText = fmt.Sprintf("Invalid voice speed. Please choose 1-10.\nCurrent speed: %d", botParams.VoiceSpeed)
					} else {
						botParams.VoiceSpeed = speed
						saveBotParams(botCfg.Name, botParams)
						responseText = fmt.Sprintf("Voice speed set to %d (+%d%%).", speed, speed*10)
					}
				} else if len(parts) >= 2 && strings.ToLower(parts[1]) == "char" {
					// .voice char [on/off] - character voice mode (multiple voices per story)
					if len(parts) >= 3 {
						arg := strings.ToLower(parts[2])
						if arg == "on" {
							botParams.VoiceChar = true
							botParams.Voice = true // char mode implies voice is on
							saveBotParams(botCfg.Name, botParams)
							responseText = "Character voice mode enabled. AI responses will use multiple character-specific voices.\nVoice generation can have longer delays."
						} else if arg == "off" {
							botParams.VoiceChar = false
							botParams.VoiceAssignments = nil
							voiceCharAssignments = make(map[string]string)
							SyncVoiceAssignments(&botParams, voiceCharAssignments)
							saveBotParams(botCfg.Name, botParams)
							responseText = "Character voice mode disabled. Voice assignments cleared."
						} else {
							responseText = fmt.Sprintf("Voice char mode is %s\nUsage: voice char [on/off]", onOff(botParams.VoiceChar))
						}
					} else {
						responseText = fmt.Sprintf("Voice char mode is %s\nUsage: voice char [on/off]", onOff(botParams.VoiceChar))
					}
				} else if len(parts) >= 2 && strings.ToLower(parts[1]) == "on" {
					botParams.Voice = true
					saveBotParams(botCfg.Name, botParams)
					responseText = "Voice enabled. AI responses will include audio."
				} else if len(parts) >= 2 && strings.ToLower(parts[1]) == "off" {
					botParams.Voice = false
					saveBotParams(botCfg.Name, botParams)
					responseText = "Voice disabled."
				} else if len(parts) >= 2 && strings.ToLower(parts[1]) == "list" {
					// .voice list - show character→voice assignments
					SyncVoiceAssignments(&botParams, voiceCharAssignments)
					responseText = GetVoiceAssignmentsDisplay(voiceCharAssignments)
				} else if len(parts) >= 4 && strings.ToLower(parts[1]) == "change" {
					// .voice change [n] [accent] - ask AI to pick a new voice for a character
					// The index [n] refers to the numbered list shown by .voice list
					SyncVoiceAssignments(&botParams, voiceCharAssignments)
					if len(voiceCharAssignments) == 0 {
						responseText = "No character voice assignments yet. Use .voice char on and generate a response first."
					} else {
						num, err := strconv.Atoi(parts[2])
						if err != nil || num < 1 || num > len(voiceCharAssignments) {
							responseText = fmt.Sprintf("Invalid character number. Please choose 1-%d.", len(voiceCharAssignments))
						} else {
							// Get the character name at the given index (sorted order matches .voice list)
							var names []string
							for name := range voiceCharAssignments {
								names = append(names, name)
							}
							sort.Strings(names)
							characterName := names[num-1]

							// The accent is everything after "voice change [n]"
							accent := strings.TrimSpace(strings.TrimPrefix(update.Message.Text, parts[0]+" "+parts[1]+" "+parts[2]))
							if accent == "" {
								responseText = "Usage: voice change [n] [accent] - e.g. .voice change 2 deep southern accent"
							} else {
								// Send typing indicator while the AI selects a voice
								typingAction := tgbotapi.NewChatAction(update.Message.Chat.ID, tgbotapi.ChatTyping)
								bot.Send(typingAction)

								modelEntry := ollamaCfg.Models[selectedProvider]
								changeMsg, err := ChangeCharacterVoice(botCfg.Name, &botParams, modelEntry, ollamaCfg.APIBase, &voiceCharAssignments, characterName, accent)
								if err != nil {
									log.Printf("[%s] Voice change failed: %v", botCfg.Name, err)
									responseText = fmt.Sprintf("Failed to change voice for '%s': %v", characterName, err)
								} else {
									responseText = changeMsg
								}
							}
						}
					}
				} else {
					responseText = fmt.Sprintf("Voice is %s, speed %d, char mode %s\nUsage: voice on/off, voice speed 1-10, voice char on/off, voice list, voice change [n] [accent]", onOff(botParams.Voice), botParams.VoiceSpeed, onOff(botParams.VoiceChar))
				}
				sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
				continue

			case "model", "m":
				if len(parts) >= 2 && parts[1] == "loaded" {
					// Force re-load the model from cached file
					loadFilteredModels()
					filteredModelsMu.Lock()
					models := make([]string, len(filteredModelsCache))
					copy(models, filteredModelsCache)
					filteredModelsMu.Unlock()
					responseText = fmt.Sprintf("Loaded %d filtered models from disk.", len(models))
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				}
				if len(parts) == 1 {
					// Just "model" - list available models with numbers
					apiKey := ollamaCfg.Models[selectedProvider].APIKey
					models, err := listOllamaModels(getEffectiveAPIBase(ollamaCfg.Models[selectedProvider], ollamaCfg.APIBase), apiKey)
					if err != nil {
						responseText = fmt.Sprintf("Error fetching models: %v", err)
					} else {
						// Sort models to match the order displayed in the list command
						sort.Slice(models, func(i, j int) bool {
							return models[i].Name < models[j].Name
						})
						// Determine if we need to check subscription filtering (only for api.ollama.com)
						needsFiltering := isOllamaDotCom(getEffectiveAPIBase(ollamaCfg.Models[selectedProvider], ollamaCfg.APIBase))
						var filteredSet map[string]bool
						if needsFiltering {
							// Load filtered models to check which are accessible
							loadFilteredModels()
							filteredModelsMu.Lock()
							filteredSet = make(map[string]bool, len(filteredModelsCache))
							for _, f := range filteredModelsCache {
								filteredSet[f] = true
							}
							filteredModelsMu.Unlock()
						}
						var sb strings.Builder
						sb.WriteString("Available models:\n")
						for i, m := range models {
							prefix := ""
							if needsFiltering && !filteredSet[m.Name] {
								prefix = " ❌"
							}
							sizeStr := formatModelSize(m.Size)
							if sizeStr != "" {
								sizeStr = " (" + sizeStr + ")"
							}
							if m.Name == ollamaCfg.Models[selectedProvider].Model {
								sb.WriteString(fmt.Sprintf("  %d)%s %s%s ✅\n", i+1, prefix, m.Name, sizeStr))
							} else {
								sb.WriteString(fmt.Sprintf("  %d)%s %s%s\n", i+1, prefix, m.Name, sizeStr))
							}
						}
						responseText = sb.String()
					}
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				} else if len(parts) == 2 && (parts[1] == "+" || parts[1] == "-") {
					// "model +" or "model -" - cycle to next/previous model
					apiKey := ollamaCfg.Models[selectedProvider].APIKey
					models, err := listOllamaModels(getEffectiveAPIBase(ollamaCfg.Models[selectedProvider], ollamaCfg.APIBase), apiKey)
					if err != nil {
						responseText = fmt.Sprintf("Error fetching models: %v", err)
						sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
						continue
					}
					{
						// Sort models to match the order displayed in the list command
						sort.Slice(models, func(i, j int) bool {
							return models[i].Name < models[j].Name
						})

						currentModel := ollamaCfg.Models[selectedProvider].Model
						currentIdx := -1
						for i, m := range models {
							if m.Name == currentModel {
								currentIdx = i
								break
							}
						}

						if currentIdx == -1 {
							responseText = "Current model not found in API list."
						} else {
							newIdx := currentIdx
							if parts[1] == "+" {
								// Next model, but don't go past the last one
								if newIdx < len(models)-1 {
									newIdx++
								} else {
									responseText = "Already at the last model."
								}
							} else { // "-"
								// Previous model, but don't go below the first one
								if newIdx > 0 {
									newIdx--
								} else {
									responseText = "Already at the first model."
								}
							}

							if newIdx != currentIdx {
								ollamaCfg.Models[selectedProvider].Model = models[newIdx].Name
								botParams.CurrentModel = models[newIdx].Name
								saveBotParams(botCfg.Name, botParams)
								responseText = fmt.Sprintf("Switched to model: %s", models[newIdx].Name)
							}
						}

						// Send the response and schedule deletion after deleteAfterDuration
						respMsg := tgbotapi.NewMessage(update.Message.Chat.ID, responseText)
						respMsg.ParseMode = tgbotapi.ModeMarkdown
						sentMsg, err := bot.Send(respMsg)
						if err != nil {
							log.Printf("[%s] Failed to send model message: %v", botCfg.Name, err)
						} else {
							ledger.Add(sentMsg.MessageID, LedgerCommandResponse)
							log.Printf("[%s] Sent model response to user %d", botCfg.Name, userID)
							go func(chatID int64, messageID int) {
								time.Sleep(deleteAfterDuration)
								deleteMsg := tgbotapi.NewDeleteMessage(chatID, messageID)
								if _, err := bot.Request(deleteMsg); err != nil {
									log.Printf("[%s] Failed to delete model message: %v", botCfg.Name, err)
								}
							}(sentMsg.Chat.ID, sentMsg.MessageID)
						}
						continue
					}
				} else if len(parts) >= 2 && parts[1] == "test" {
					// "model test" - test all models for subscription/upgrade requirements
					// This must be checked BEFORE the "model <number>" handler below,
					// since "model test" also has len(parts) == 2.
					// model test - test all models for subscription/upgrade requirements
					apiKey := ollamaCfg.Models[selectedProvider].APIKey
					models, err := listOllamaModels(getEffectiveAPIBase(ollamaCfg.Models[selectedProvider], ollamaCfg.APIBase), apiKey)
					if err != nil {
						responseText = fmt.Sprintf("Error fetching models: %v", err)
						sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
						continue
					}

					responseText = fmt.Sprintf("Testing %d models for subscription requirements... This may take a while.", len(models))
					respMsg := tgbotapi.NewMessage(update.Message.Chat.ID, responseText)
					sentMsg, err := bot.Send(respMsg)
					if err == nil {
						ledger.Add(sentMsg.MessageID, LedgerCommandResponse)
					}

					modelEntry := ollamaCfg.Models[selectedProvider]
					var accessible []string
					var blocked []string

					// Track the previous status message ID for deletion
					var prevStatusMsgID int

					for _, m := range models {
						log.Printf("[%s] Testing model: %s", botCfg.Name, m.Name)

						// Send a status message showing which model is being tested
						statusText := fmt.Sprintf("testing model %s", m.Name)
						statusSent := sendAndTrack(bot, update.Message.Chat.ID, statusText, &ledger)
						if statusSent != nil {
							// Delete the previous status message if it exists
							if prevStatusMsgID != 0 {
								deleteTrackedMessage(bot, update.Message.Chat.ID, prevStatusMsgID)
							}
							prevStatusMsgID = statusSent.MessageID
						}

						if testModelAccess(getEffectiveAPIBase(modelEntry, ollamaCfg.APIBase), modelEntry, m.Name) {
							accessible = append(accessible, m.Name)
						} else {
							blocked = append(blocked, m.Name)
						}
					}

					// Delete the last status message since we're about to show results
					if prevStatusMsgID != 0 {
						deleteTrackedMessage(bot, update.Message.Chat.ID, prevStatusMsgID)
					}

					saveFilteredModels(accessible)

					var sb strings.Builder
					sb.WriteString(fmt.Sprintf("✅ Accessible: %d\n", len(accessible)))
					for _, name := range accessible {
						sb.WriteString(fmt.Sprintf("  • %s\n", name))
					}
					if len(blocked) > 0 {
						sb.WriteString(fmt.Sprintf("\n❌ Blocked (subscription/upgrade): %d\n", len(blocked)))
						for _, name := range blocked {
							sb.WriteString(fmt.Sprintf("  • %s\n", name))
						}
					}

					responseText = sb.String()
				} else if len(parts) == 2 {
					// "model <number>" - select a model by number from the sorted list
					num, err := strconv.Atoi(parts[1])
					if err != nil {
						responseText = "Usage: model [number] - list or select a model"
					} else {
						apiKey := ollamaCfg.Models[selectedProvider].APIKey
						models, err := listOllamaModels(getEffectiveAPIBase(ollamaCfg.Models[selectedProvider], ollamaCfg.APIBase), apiKey)
						if err != nil {
							responseText = fmt.Sprintf("Error fetching models: %v", err)
						} else if num < 1 || num > len(models) {
							responseText = fmt.Sprintf("Invalid model number. Please choose 1-%d.", len(models))
						} else {
							// Sort models to match the order displayed in the list command
							sort.Slice(models, func(i, j int) bool {
								return models[i].Name < models[j].Name
							})
							ollamaCfg.Models[selectedProvider].Model = models[num-1].Name
							botParams.CurrentModel = models[num-1].Name
							saveBotParams(botCfg.Name, botParams)
							responseText = fmt.Sprintf("Selected model: %s", models[num-1].Name)
						}
					}
				} else {
					responseText = "Usage: model [number] - list or select a model"
				}

			case "mf":
				// Reload filtered models cache from disk
				loadFilteredModels()

				filteredModelsMu.Lock()
				models := make([]string, len(filteredModelsCache))
				copy(models, filteredModelsCache)
				filteredModelsMu.Unlock()

				if len(models) == 0 {
					responseText = "No filtered models available. Run `.model test` first to generate the list."
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				}

				if len(parts) == 1 {
					// mf - list filtered models
					var sb strings.Builder
					sb.WriteString("Accessible models:\n")
					currentModel := ollamaCfg.Models[selectedProvider].Model
					for i, name := range models {
						if name == currentModel {
							sb.WriteString(fmt.Sprintf("  %d) %s ✅\n", i+1, name))
						} else {
							sb.WriteString(fmt.Sprintf("  %d) %s\n", i+1, name))
						}
					}
					responseText = sb.String()
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				} else if len(parts) == 2 {
					switch parts[1] {
					case "next":
						// Cycle to next filtered model
						currentModel := ollamaCfg.Models[selectedProvider].Model
						var currentIdx int = -1
						for i, name := range models {
							if name == currentModel {
								currentIdx = i
								break
							}
						}
						nextIdx := (currentIdx + 1) % len(models)
						ollamaCfg.Models[selectedProvider].Model = models[nextIdx]
						botParams.CurrentModel = models[nextIdx]
						saveBotParams(botCfg.Name, botParams)
						responseText = fmt.Sprintf("Switched to model: %s", models[nextIdx])
					case "prev":
						// Cycle to previous filtered model
						currentModel := ollamaCfg.Models[selectedProvider].Model
						var currentIdx int = -1
						for i, name := range models {
							if name == currentModel {
								currentIdx = i
								break
							}
						}
						prevIdx := (currentIdx - 1 + len(models)) % len(models)
						ollamaCfg.Models[selectedProvider].Model = models[prevIdx]
						botParams.CurrentModel = models[prevIdx]
						saveBotParams(botCfg.Name, botParams)
						responseText = fmt.Sprintf("Switched to model: %s", models[prevIdx])
					default:
						// mf [n] - select a model by number
						num, err := strconv.Atoi(parts[1])
						if err != nil || num < 1 || num > len(models) {
							responseText = fmt.Sprintf("Invalid model number. Please choose 1-%d.", len(models))
						} else {
							ollamaCfg.Models[selectedProvider].Model = models[num-1]
							botParams.CurrentModel = models[num-1]
							saveBotParams(botCfg.Name, botParams)
							responseText = fmt.Sprintf("Selected model: %s", models[num-1])
						}
					}
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				} else {
					responseText = "Usage: mf [n/next/prev] - list or cycle accessible models"
				}

			case "llmctx", "mc":
				if len(parts) == 2 {
					arg := strings.ToLower(parts[1])
					if strings.HasSuffix(arg, "k") {
						num, err := strconv.Atoi(strings.TrimSuffix(arg, "k"))
						if err != nil || num < 1 {
							responseText = "Invalid context size. Use a number ending in k (e.g. 8k, 16k, 32k)."
						} else {
							botParams.NumCtx = num * 1024
							saveBotParams(botCfg.Name, botParams)
							responseText = fmt.Sprintf("Context set to %dK (%d)", num, botParams.NumCtx)
						}
					} else {
						responseText = "Invalid format. Use a number ending in k (e.g. 8k, 16k, 32k)."
					}
				} else {
					ctxK := botParams.NumCtx / 1024
					responseText = fmt.Sprintf("Current context size: %dK (%d)\nUsage: .llmctx [nk] - set the model context window (e.g. 8k, 16k, 32k)", ctxK, botParams.NumCtx)
				}
				sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
				continue

			case "trace":
				if len(parts) == 2 {
					arg := strings.ToLower(parts[1])
					if arg == "on" {
						logToFileEnabled = true
						responseText = fmt.Sprintf("Trace enabled. LLM request payloads will be written to context/<bot>/ (max %d per bot).", maxTraceFilesPerBot)
					} else if arg == "off" {
						logToFileEnabled = false
						responseText = "Trace disabled. No more payloads written."
					} else {
						responseText = fmt.Sprintf("Current trace: %v\nUsage: trace [on/off]", logToFileEnabled)
					}
				} else {
					responseText = fmt.Sprintf("Current trace: %v\nUsage: trace [on/off]", logToFileEnabled)
				}
				sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
				continue

			case "story":
				if len(parts) == 1 {
					// List available story folders
					stories, err := listStoryFolders()
					if err != nil {
						responseText = fmt.Sprintf("Error listing stories: %v", err)
					} else {
						var sb strings.Builder
						sb.WriteString("Available stories:\n")
						for i, story := range stories {
							if story == currentStory {
								sb.WriteString(fmt.Sprintf("  %d) %s ✅\n", i+1, displayName(story)))
							} else {
								sb.WriteString(fmt.Sprintf("  %d) %s\n", i+1, displayName(story)))
							}
						}
						responseText = sb.String()
					}
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				} else if len(parts) >= 2 && parts[1] == "save" {
					name := "autosave"
					if len(parts) >= 3 {
						name = parts[2]
					}
					path := getStorySnapshotPath(currentStory, botCfg.Name, name)
					if err := rewriteChatLogFile(path, conversationContext, currentMode); err != nil {
						responseText = fmt.Sprintf("Failed to save story snapshot: %v", err)
					} else {
						responseText = fmt.Sprintf("Saved story snapshot '%s' for %s.", name, displayName(currentStory))
					}
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				} else if len(parts) >= 2 && parts[1] == "load" {
					name := "autosave"
					if len(parts) >= 3 {
						name = parts[2]
					}
					path := getStorySnapshotPath(currentStory, botCfg.Name, name)
					if _, err := os.Stat(path); err != nil {
						responseText = fmt.Sprintf("No saved story snapshot named '%s' in %s.", name, displayName(currentStory))
					} else {
						loaded := loadChatHistory(path)
						conversationContext = loaded
						responseText = fmt.Sprintf("Loaded story snapshot '%s' for %s.", name, displayName(currentStory))
					}
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				} else if len(parts) == 2 {
					// story [n] - select a story by number
					num, err := strconv.Atoi(parts[1])
					if err != nil {
						responseText = "Invalid story number."
					} else {
						stories, err := listStoryFolders()
						if err != nil {
							responseText = fmt.Sprintf("Error listing stories: %v", err)
						} else if num < 1 || num > len(stories) {
							responseText = fmt.Sprintf("Invalid story number. Please choose 1-%d.", len(stories))
						} else {
							currentStory = stories[num-1]
							// Clear active scenes and characters when switching stories
							activeScenes = make(map[int]bool)
							activeCharacters = make(map[int]bool)
							botParams.CurrentStory = currentStory
							botParams.ActiveScenes = []int{}
							botParams.ActiveCharacters = []int{}
							saveBotParams(botCfg.Name, botParams)
							responseText = fmt.Sprintf("Switched to story: %s", displayName(currentStory))
						}
					}
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				} else {
					responseText = "Usage: story [n] | story save [name] | story load [name]"
				}

			case "thread":
				if len(parts) == 1 {
					threads, err := listStorySnapshots(currentStory, botCfg.Name)
					if err != nil {
						responseText = fmt.Sprintf("Error listing story threads: %v", err)
					} else if len(threads) == 0 {
						responseText = fmt.Sprintf("No saved threads in %s yet.", displayName(currentStory))
					} else {
						var sb strings.Builder
						sb.WriteString(fmt.Sprintf("Saved threads in '%s':\n", displayName(currentStory)))
						for i, name := range threads {
							sb.WriteString(fmt.Sprintf("  %d) %s\n", i+1, displayName(name)))
						}
						responseText = sb.String()
					}
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				}
				if len(parts) >= 2 && parts[1] == "save" {
					name := "autosave"
					if len(parts) >= 3 {
						name = parts[2]
					}
					path := getStorySnapshotPath(currentStory, botCfg.Name, name)
					if err := rewriteChatLogFile(path, conversationContext, currentMode); err != nil {
						responseText = fmt.Sprintf("Failed to save thread '%s': %v", name, err)
					} else {
						responseText = fmt.Sprintf("Saved thread '%s' in %s.", name, displayName(currentStory))
					}
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				}
				if len(parts) >= 2 && parts[1] == "load" {
					if len(parts) == 3 {
						if num, err := strconv.Atoi(parts[2]); err == nil {
							loaded, err := loadThreadSnapshotByIndex(currentStory, botCfg.Name, num)
							if err != nil {
								responseText = err.Error()
							} else {
								conversationContext = loaded
								responseText = fmt.Sprintf("Loaded thread #%d from %s.", num, displayName(currentStory))
							}
							sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
							continue
						}
					}
					name := "autosave"
					if len(parts) >= 3 {
						name = parts[2]
					}
					path := getStorySnapshotPath(currentStory, botCfg.Name, name)
					if _, err := os.Stat(path); err != nil {
						responseText = fmt.Sprintf("No saved thread named '%s' in %s.", name, displayName(currentStory))
					} else {
						loaded := loadChatHistory(path)
						conversationContext = loaded
						responseText = fmt.Sprintf("Loaded thread '%s' from %s.", name, displayName(currentStory))
					}
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				}
				if len(parts) == 2 {
					num, err := strconv.Atoi(parts[1])
					if err != nil {
						responseText = "Usage: thread [list] | thread save [name] | thread load [number|name]"
					} else {
						loaded, err := loadThreadSnapshotByIndex(currentStory, botCfg.Name, num)
						if err != nil {
							responseText = err.Error()
						} else {
							conversationContext = loaded
							responseText = fmt.Sprintf("Loaded thread #%d from %s.", num, displayName(currentStory))
						}
					}
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				}
				responseText = "Usage: thread | thread save [name] | thread load [number|name]"
				sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
				continue

			case "scene", "sc":
				if len(parts) == 1 {
					// List scene files in current story
					scenes, err := listSceneFiles(currentStory)
					if err != nil {
						responseText = fmt.Sprintf("Error listing scenes: %v", err)
					} else {
						var sb strings.Builder
						sb.WriteString(fmt.Sprintf("Scenes in '%s':\n", displayName(currentStory)))
						for i, s := range scenes {
							sceneDisp := displayName(s)
							if activeScenes[i+1] {
								sb.WriteString(fmt.Sprintf("  %d) %s ✅\n", i+1, sceneDisp))
							} else {
								sb.WriteString(fmt.Sprintf("  %d) %s\n", i+1, sceneDisp))
							}
						}
						responseText = sb.String()
					}
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				} else if len(parts) >= 2 && parts[1] == "all" && len(parts) == 3 && parts[2] == "off" {
					// scene all off
					activeScenes = make(map[int]bool)
					responseText = "All scenes deactivated."
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				} else if len(parts) >= 2 && parts[1] == "edit" {
					// scene edit [n] - show scene content
					if len(parts) == 3 {
						num, err := strconv.Atoi(parts[2])
						if err != nil {
							responseText = "Invalid scene number."
						} else {
							scenes, err := listSceneFiles(currentStory)
							if err != nil {
								responseText = fmt.Sprintf("Error listing scenes: %v", err)
							} else if num < 1 || num > len(scenes) {
								responseText = fmt.Sprintf("Invalid scene number. Please choose 1-%d.", len(scenes))
							} else {
								content := loadStoryContent(currentStory, "scenes", scenes[num-1])
								responseText = fmt.Sprintf("Scene: %s\n\n%s", scenes[num-1], content)
							}
						}
					} else {
						responseText = "Usage: scene edit [n] - show scene content"
					}
				} else if len(parts) >= 3 && parts[1] == "save" {
					// scene save [name] [text] - save a new scene
					sceneName := parts[2]
					contentStart := len("scene save " + sceneName + " ")
					if len(update.Message.Text) <= contentStart {
						responseText = "Usage: scene save [name] [text] - provide scene content after the name"
					} else {
						content := update.Message.Text[contentStart:]
						path := fmt.Sprintf("stories/%s/scenes/%s", currentStory, sceneName)
						if err := os.WriteFile(path, []byte(content), 0644); err != nil {
							responseText = fmt.Sprintf("Failed to save scene: %v", err)
						} else {
							responseText = fmt.Sprintf("Scene '%s' saved.", sceneName)
						}
					}
				} else {
					// Multi-toggle: scene 1 -2 3
					scenes, err := listSceneFiles(currentStory)
					if err != nil {
						responseText = fmt.Sprintf("Error listing scenes: %v", err)
					} else {
						var toggled []string
						for _, arg := range parts[1:] {
							num, err := strconv.Atoi(arg)
							if err != nil {
								continue
							}
							if num < 0 {
								// Deactivate
								idx := -num
								if idx >= 1 && idx <= len(scenes) {
									delete(activeScenes, idx)
									sceneName := scenes[idx-1]
									toggled = append(toggled, fmt.Sprintf("%s off", sceneName))
								}
							} else {
								// Activate
								if num >= 1 && num <= len(scenes) {
									activeScenes[num] = true
									sceneName := scenes[num-1]
									toggled = append(toggled, fmt.Sprintf("%s on", sceneName))
								}
							}
						}
						if len(toggled) > 0 {
							// Persist the changes
							var newActiveScenes []int
							for idx := range activeScenes {
								newActiveScenes = append(newActiveScenes, idx)
							}
							botParams.ActiveScenes = newActiveScenes
							saveBotParams(botCfg.Name, botParams)
							responseText = fmt.Sprintf("Scenes toggled:\n%s", strings.Join(toggled, "\n"))
						} else {
							responseText = "No valid scene numbers provided."
						}
					}
				}

			case "char", "c":
				if len(parts) == 1 {
					// List character files in current story
					chars, err := listCharacterFiles(currentStory)
					if err != nil {
						responseText = fmt.Sprintf("Error listing characters: %v", err)
					} else {
						var sb strings.Builder
						sb.WriteString(fmt.Sprintf("Characters in '%s':\n", displayName(currentStory)))
						for i, c := range chars {
							charDisp := displayName(c)
							if activeCharacters[i+1] {
								sb.WriteString(fmt.Sprintf("  %d) %s ✅\n", i+1, charDisp))
							} else {
								sb.WriteString(fmt.Sprintf("  %d) %s\n", i+1, charDisp))
							}
						}
						responseText = sb.String()
					}
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				} else if len(parts) >= 2 && parts[1] == "all" && len(parts) == 3 && parts[2] == "off" {
					// char all off
					activeCharacters = make(map[int]bool)
					responseText = "All characters deactivated."
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				} else if len(parts) >= 2 && parts[1] == "edit" {
					// char edit [n] - show character content
					if len(parts) == 3 {
						num, err := strconv.Atoi(parts[2])
						if err != nil {
							responseText = "Invalid character number."
						} else {
							chars, err := listCharacterFiles(currentStory)
							if err != nil {
								responseText = fmt.Sprintf("Error listing characters: %v", err)
							} else if num < 1 || num > len(chars) {
								responseText = fmt.Sprintf("Invalid character number. Please choose 1-%d.", len(chars))
							} else {
								content := loadStoryContent(currentStory, "characters", chars[num-1])
								responseText = fmt.Sprintf("Character: %s\n\n%s", chars[num-1], content)
							}
						}
					} else {
						responseText = "Usage: char edit [n] - show character content"
					}
				} else if len(parts) >= 3 && parts[1] == "save" {
					// char save [name] [text] - save a new character
					charName := parts[2]
					contentStart := len("char save " + charName + " ")
					if len(update.Message.Text) <= contentStart {
						responseText = "Usage: char save [name] [text] - provide character content after the name"
					} else {
						content := update.Message.Text[contentStart:]
						path := fmt.Sprintf("stories/%s/characters/%s", currentStory, charName)
						if err := os.WriteFile(path, []byte(content), 0644); err != nil {
							responseText = fmt.Sprintf("Failed to save character: %v", err)
						} else {
							responseText = fmt.Sprintf("Character '%s' saved.", charName)
						}
					}
				} else {
					// Multi-toggle: char 1 -2 3
					chars, err := listCharacterFiles(currentStory)
					if err != nil {
						responseText = fmt.Sprintf("Error listing characters: %v", err)
					} else {
						var toggled []string
						for _, arg := range parts[1:] {
							num, err := strconv.Atoi(arg)
							if err != nil {
								continue
							}
							if num < 0 {
								// Deactivate
								idx := -num
								if idx >= 1 && idx <= len(chars) {
									delete(activeCharacters, idx)
									charName := strings.TrimSuffix(chars[idx-1], ".txt")
									toggled = append(toggled, fmt.Sprintf("%s off", charName))
								}
							} else {
								// Activate
								if num >= 1 && num <= len(chars) {
									activeCharacters[num] = true
									charName := strings.TrimSuffix(chars[num-1], ".txt")
									toggled = append(toggled, fmt.Sprintf("%s on", charName))
								}
							}
						}
						if len(toggled) > 0 {
							// Persist the changes
							var newActiveChars []int
							for idx := range activeCharacters {
								newActiveChars = append(newActiveChars, idx)
							}
							botParams.ActiveCharacters = newActiveChars
							saveBotParams(botCfg.Name, botParams)
							responseText = fmt.Sprintf("Characters toggled:\n%s", strings.Join(toggled, "\n"))
						} else {
							responseText = "No valid character numbers provided."
						}
					}
				}

			case "ask":
				// Ask a question outside of roleplay context, but with access to conversation history
				if len(parts) < 2 {
					responseText = "Usage: ask [question] - ask a question about the conversation"
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				}

				// Extract the question text (everything after ".ask")
				question := strings.TrimSpace(strings.TrimPrefix(update.Message.Text, parts[0]))
				if question == "" {
					responseText = "Usage: ask [question] - ask a question about the conversation"
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				}

				// Send typing indicator
				typingAction := tgbotapi.NewChatAction(update.Message.Chat.ID, tgbotapi.ChatTyping)
				bot.Send(typingAction)

				// Use neutral system prompt instead of the current role
				systemPrompt := "You are a helpful assistant. Answer any questions based on the provided data."

				// Build injected context from active scenes and characters
				var injectedParts []string

				// Add active scenes
				scenes, _ := listSceneFiles(currentStory)
				var activeSceneLines []string
				for idx := range activeScenes {
					if idx >= 1 && idx <= len(scenes) {
						content := loadStoryContent(currentStory, "scenes", scenes[idx-1])
						if content != "" {
							activeSceneLines = append(activeSceneLines, fmt.Sprintf("Scene %d (%s): %s", idx, scenes[idx-1], content))
						} else {
							activeSceneLines = append(activeSceneLines, fmt.Sprintf("Scene %d (%s)", idx, scenes[idx-1]))
						}
					}
				}
				if len(activeSceneLines) > 0 {
					sort.Strings(activeSceneLines)
					injectedParts = append(injectedParts, "[Active Scenes]\n"+strings.Join(activeSceneLines, "\n"))
				}

				// Add active characters
				chars, _ := listCharacterFiles(currentStory)
				var activeCharLines []string
				for idx := range activeCharacters {
					if idx >= 1 && idx <= len(chars) {
						content := loadStoryContent(currentStory, "characters", chars[idx-1])
						if content != "" {
							activeCharLines = append(activeCharLines, fmt.Sprintf("Character %d (%s): %s", idx, chars[idx-1], content))
						} else {
							activeCharLines = append(activeCharLines, fmt.Sprintf("Character %d (%s)", idx, chars[idx-1]))
						}
					}
				}
				if len(activeCharLines) > 0 {
					sort.Strings(activeCharLines)
					injectedParts = append(injectedParts, "[Active Characters]\n"+strings.Join(activeCharLines, "\n"))
				}

				// Build a plain, neutral message: simply provide context and the question.
				// Do NOT use buildPrompt() or defaultExecutionDirective() — those apply "Story Weaver"
				// narrative rules which contradict the neutral system prompt of the ask command.
				var enrichedMessage string
				if len(injectedParts) > 0 {
					enrichedMessage = "Context:\n" + strings.Join(injectedParts, "\n\n") + "\n\nQuestion: " + question
				} else {
					enrichedMessage = "Question: " + question
				}

				// Build context from conversation history (same logic as default case)
				var apiContext []ContextMessage
				if currentMode == "chat" {
					// Chat mode: include all past user + assistant messages
					apiContext = conversationContext
				} else {
					// Story mode: include only past assistant messages
					for _, msg := range conversationContext {
						if msg.Role == "assistant" {
							apiContext = append(apiContext, msg)
						}
					}
				}

				// Call Ollama API
				modelEntry := ollamaCfg.Models[selectedProvider]
				askResponse, err := callOllamaAPI(botCfg.Name, getEffectiveAPIBase(modelEntry, ollamaCfg.APIBase), modelEntry, systemPrompt, apiContext, enrichedMessage, botParams.NumCtx, botParams.NoThink)
				if err != nil {
					log.Printf("[%s] Ollama API error (ask): %v", botCfg.Name, err)
					askResponse = fmt.Sprintf("Sorry, I encountered an error: %v", err)
				}

				// Do NOT store the ask/response in conversationContext

				// Send the ask response as plain text to avoid markdown parsing errors from LLM output
				respMsg := tgbotapi.NewMessage(update.Message.Chat.ID, askResponse)
				// NO ParseMode - send as plain text to handle LLM responses with special characters
				if sentMsg, err := bot.Send(respMsg); err != nil {
					log.Printf("[%s] Failed to send ask response: %v", botCfg.Name, err)
				} else {
					ledger.Add(sentMsg.MessageID, LedgerCommandResponse)
					log.Printf("[%s] Sent ask response to user %d", botCfg.Name, userID)
				}
				continue // Skip the default response sending below

			case "resend":
				// Resend the last user message to the AI
				if lastUserText == "" {
					responseText = "No previous message to resend."
					sendAndScheduleDelete(bot, update.Message.Chat.ID, responseText, &ledger)
					continue
				}

				log.Printf("[%s] Resending last user message: %s", botCfg.Name, lastUserText)

				// Send a typing indicator while we process
				typingAction := tgbotapi.NewChatAction(update.Message.Chat.ID, tgbotapi.ChatTyping)
				bot.Send(typingAction)

				// Use the selected model entry for responses
				modelEntry := ollamaCfg.Models[selectedProvider]

				// Load the current role content as the system prompt
				systemPrompt := loadRoleContent(botParams.CurrentRole)

				// Build injected context from active scenes and characters
				var injectedParts []string

				// Add active scenes
				scenes, _ := listSceneFiles(currentStory)
				var activeSceneLines []string
				for idx := range activeScenes {
					if idx >= 1 && idx <= len(scenes) {
						content := loadStoryContent(currentStory, "scenes", scenes[idx-1])
						if content != "" {
							activeSceneLines = append(activeSceneLines, fmt.Sprintf("Scene %d (%s): %s", idx, scenes[idx-1], content))
						} else {
							activeSceneLines = append(activeSceneLines, fmt.Sprintf("Scene %d (%s)", idx, scenes[idx-1]))
						}
					}
				}
				if len(activeSceneLines) > 0 {
					sort.Strings(activeSceneLines)
					injectedParts = append(injectedParts, "[Active Scenes]\n"+strings.Join(activeSceneLines, "\n"))
				}

				// Add active characters
				chars, _ := listCharacterFiles(currentStory)
				var activeCharLines []string
				for idx := range activeCharacters {
					if idx >= 1 && idx <= len(chars) {
						content := loadStoryContent(currentStory, "characters", chars[idx-1])
						if content != "" {
							activeCharLines = append(activeCharLines, fmt.Sprintf("Character %d (%s): %s", idx, chars[idx-1], content))
						} else {
							activeCharLines = append(activeCharLines, fmt.Sprintf("Character %d (%s)", idx, chars[idx-1]))
						}
					}
				}
				if len(activeCharLines) > 0 {
					sort.Strings(activeCharLines)
					injectedParts = append(injectedParts, "[Active Characters]\n"+strings.Join(activeCharLines, "\n"))
				}

				// Build the final user message with injected context and an Execution Anchor
				enrichedMessage := buildPrompt(injectedParts, lastUserText, defaultExecutionDirective())

				// Do NOT re-store the user message in context - it was already stored the first time

				// Build context for API based on mode
				var apiContext []ContextMessage
				if currentMode == "chat" {
					// Chat mode: include all past user + assistant messages
					apiContext = conversationContext
				} else {
					// Story mode: include only past assistant messages
					for _, msg := range conversationContext {
						if msg.Role == "assistant" {
							apiContext = append(apiContext, msg)
						}
					}
				}

				// Call Ollama API for a response with context
				responseText, err = callOllamaAPI(botCfg.Name, getEffectiveAPIBase(modelEntry, ollamaCfg.APIBase), modelEntry, systemPrompt, apiContext, enrichedMessage, botParams.NumCtx, botParams.NoThink)
				if err != nil {
					log.Printf("[%s] Ollama API error (resend): %v", botCfg.Name, err)
					responseText = fmt.Sprintf("Sorry, I encountered an error contacting the AI: %v\n\nUse `resend` to resend your last message.", err)
				}

				// Store assistant response in context (both modes)
				conversationContext = append(conversationContext, ContextMessage{Role: "assistant", Content: responseText})

				// Append assistant response to chat log (both modes)
				chatLogPath := getChatLogPath(currentStory, botCfg.Name)
				appendToChatLog(chatLogPath, "assistant", responseText)

				// Trim context if over limit
				if len(conversationContext) > contextLimit {
					conversationContext = conversationContext[len(conversationContext)-contextLimit:]
				}

				// Check context warning after processing response
				sendContextWarning(conversationContext, botParams.NumCtx, botCfg.Name, update.Message.Chat.ID)
				voiceThisResponse = true

			default:
				// If the message is a single word that is not a recognized command and not "continue",
				// send a disappearing "[msg] ignored" response to prevent misspelled commands from reaching the LLM
				if len(parts) == 1 && command != "continue" {
					ignoredText := fmt.Sprintf("%s ignored", parts[0])
					respMsg := tgbotapi.NewMessage(update.Message.Chat.ID, ignoredText)
					sentMsg, err := bot.Send(respMsg)
					if err != nil {
						log.Printf("[%s] Failed to send ignored message: %v", botCfg.Name, err)
					} else {
						ledger.Add(sentMsg.MessageID, LedgerCommandResponse)
						log.Printf("[%s] Sent ignored response for single-word message: %s", botCfg.Name, parts[0])
						// Schedule deletion after deleteAfterDuration
						go func(chatID int64, messageID int) {
							time.Sleep(deleteAfterDuration)
							deleteMsg := tgbotapi.NewDeleteMessage(chatID, messageID)
							if _, err := bot.Request(deleteMsg); err != nil {
								log.Printf("[%s] Failed to delete ignored message: %v", botCfg.Name, err)
							}
						}(sentMsg.Chat.ID, sentMsg.MessageID)
					}
					continue // Skip the default response sending below
				}

				// Not a command - send to the AI using the selected provider
				// Save the user text for potential resend
				lastUserText = update.Message.Text

				// Send a typing indicator while we process
				typingAction := tgbotapi.NewChatAction(update.Message.Chat.ID, tgbotapi.ChatTyping)
				bot.Send(typingAction)

				// Use the selected model entry for responses
				modelEntry := ollamaCfg.Models[selectedProvider]

				// Load the current role content as the system prompt
				systemPrompt := loadRoleContent(botParams.CurrentRole)

				// Build injected context from active scenes and characters
				var injectedParts []string

				// Add active scenes
				scenes, _ := listSceneFiles(currentStory)
				var activeSceneLines []string
				for idx := range activeScenes {
					if idx >= 1 && idx <= len(scenes) {
						content := loadStoryContent(currentStory, "scenes", scenes[idx-1])
						if content != "" {
							activeSceneLines = append(activeSceneLines, fmt.Sprintf("Scene %d (%s): %s", idx, scenes[idx-1], content))
						} else {
							activeSceneLines = append(activeSceneLines, fmt.Sprintf("Scene %d (%s)", idx, scenes[idx-1]))
						}
					}
				}
				if len(activeSceneLines) > 0 {
					sort.Strings(activeSceneLines)
					injectedParts = append(injectedParts, "[Active Scenes]\n"+strings.Join(activeSceneLines, "\n"))
				}

				// Add active characters
				chars, _ := listCharacterFiles(currentStory)
				var activeCharLines []string
				for idx := range activeCharacters {
					if idx >= 1 && idx <= len(chars) {
						content := loadStoryContent(currentStory, "characters", chars[idx-1])
						if content != "" {
							activeCharLines = append(activeCharLines, fmt.Sprintf("Character %d (%s): %s", idx, chars[idx-1], content))
						} else {
							activeCharLines = append(activeCharLines, fmt.Sprintf("Character %d (%s)", idx, chars[idx-1]))
						}
					}
				}
				if len(activeCharLines) > 0 {
					sort.Strings(activeCharLines)
					injectedParts = append(injectedParts, "[Active Characters]\n"+strings.Join(activeCharLines, "\n"))
				}

				// Build the final user message with injected context and an Execution Anchor
				enrichedMessage := buildPrompt(injectedParts, update.Message.Text, defaultExecutionDirective())

				// Store user message in context (chat mode only)
				if currentMode == "chat" {
					conversationContext = append(conversationContext, ContextMessage{Role: "user", Content: update.Message.Text})
					// Append user message to chat log
					chatLogPath := getChatLogPath(currentStory, botCfg.Name)
					appendToChatLog(chatLogPath, "user", update.Message.Text)
				}

				// Build context for API based on mode
				var apiContext []ContextMessage
				if currentMode == "chat" {
					// Chat mode: include all past user + assistant messages
					apiContext = conversationContext
				} else {
					// Story mode: include only past assistant messages
					for _, msg := range conversationContext {
						if msg.Role == "assistant" {
							apiContext = append(apiContext, msg)
						}
					}
				}

				// Call Ollama API for a response with context
				responseText, err = callOllamaAPI(botCfg.Name, getEffectiveAPIBase(modelEntry, ollamaCfg.APIBase), modelEntry, systemPrompt, apiContext, enrichedMessage, botParams.NumCtx, botParams.NoThink)
				if err != nil {
					log.Printf("[%s] Ollama API error: %v", botCfg.Name, err)
					responseText = fmt.Sprintf("Sorry, I encountered an error contacting the AI: %v\n\nUse `resend` to resend your last message.", err)
				}

				// Store assistant response in context (both modes)
				conversationContext = append(conversationContext, ContextMessage{Role: "assistant", Content: responseText})

				// Append assistant response to chat log (both modes)
				chatLogPath := getChatLogPath(currentStory, botCfg.Name)
				appendToChatLog(chatLogPath, "assistant", responseText)

				// Trim context if over limit
				if len(conversationContext) > contextLimit {
					conversationContext = conversationContext[len(conversationContext)-contextLimit:]
				}

				// Check context warning after processing response
				sendContextWarning(conversationContext, botParams.NumCtx, botCfg.Name, update.Message.Chat.ID)
				voiceThisResponse = true
			}

			// Use a safer maximum length for MarkdownV2 to account for escape characters
			const maxMessageLength = 3500
			remainingText := responseText

			for len(remainingText) > 0 {
				chunk := remainingText
				if len(chunk) > maxMessageLength {
					chunk = chunk[:maxMessageLength]
					// Try to break nicely at a newline or space instead of mid-word/mid-tag
					if lastSpace := strings.LastIndexAny(chunk, " \n"); lastSpace > 2500 {
						chunk = chunk[:lastSpace]
					}
				}

				// Escape characters for MarkdownV2 so Telegram doesn't choke on lone symbols
				escapedChunk := escapeMarkdown(chunk)

				respMsg := tgbotapi.NewMessage(
					update.Message.Chat.ID,
					escapedChunk,
				)
				// Use MarkdownV2 for modern block/bullet styling
				respMsg.ParseMode = tgbotapi.ModeMarkdownV2

				if sentMsg, err := bot.Send(respMsg); err != nil {
					log.Printf("[%s] Failed to send message chunk: %v", botCfg.Name, err)

					// Fallback: If MarkdownV2 fails, try sending as plain text so you don't lose the message
					respMsg.ParseMode = ""
					if retrySent, retryErr := bot.Send(respMsg); retryErr != nil {
						log.Printf("[%s] Critical failure sending plain text fallback: %v", botCfg.Name, retryErr)
					} else {
						ledger.Add(retrySent.MessageID, LedgerChatAssistant)
					}
					break
				} else {
					ledger.Add(sentMsg.MessageID, LedgerChatAssistant)
					// Track assistant response chunk IDs for /del UI cleanup
					lastAssistantMsgIDs = append(lastAssistantMsgIDs, sentMsg.MessageID)
					log.Printf("[%s] Sent response chunk to user %d", botCfg.Name, userID)
				}

				remainingText = remainingText[len(chunk):]
			}

			// If this was an AI-generated response and voice is enabled, send the audio too
			if voiceThisResponse {
				if botParams.VoiceChar {
					sendVoiceCharacterAudio(bot, update.Message.Chat.ID, responseText, botCfg.Name, &botParams, ollamaCfg.Models[selectedProvider], ollamaCfg.APIBase, &voiceCharAssignments, &ledger)
				} else {
					sendVoiceAudio(bot, update.Message.Chat.ID, responseText, botCfg.Name, &botParams)
				}
			}
		} else {
			log.Printf("[%s] Ignored message from unauthorized user %d", botCfg.Name, update.Message.From.ID)
		}
	}
}

func main() {
	// Parse command-line flags
	initMode := flag.Bool("init", false, "Re-run configuration wizard")
	noColorFlag := flag.Bool("no-color", false, "Disable ANSI color output in setup wizard")
	flag.Parse()

	// Propagate --no-color to the setup package
	if noColorFlag != nil && *noColorFlag {
		noColor = true
	}

	var cfg Config

	// Check if config.yaml exists; if not, run the setup wizard
	if _, err := os.Stat("config.yaml"); os.IsNotExist(err) {
		log.Println("config.yaml not found. Starting setup wizard...")
		setupCfg := initializeSetup(nil)
		if setupCfg == nil {
			log.Fatal("Setup aborted. Exiting.")
		}
		cfg = *setupCfg
	} else {
		// Load config from file or environment variables
		cfg = loadConfig()
	}

	// If --init flag is passed, re-run the setup wizard with existing values
	if *initMode {
		log.Println("--init flag detected. Re-running configuration wizard...")
		newCfg := initializeSetup(&cfg)
		if newCfg != nil {
			cfg = *newCfg
		} else {
			log.Println("Setup cancelled. Continuing with existing configuration.")
		}
	}

	// Set debug level on config
	_ = cfg.Debug // future use

	// Load the filtered models cache from disk on startup
	loadFilteredModels()

	// Handle graceful shutdown on SIGINT/SIGTERM
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	var wg sync.WaitGroup

	// Start a goroutine for each bot
	for _, botCfg := range cfg.Bots {
		wg.Add(1)
		go runBot(botCfg, cfg.UserID, cfg.Ollama, &wg)
		log.Printf("Started bot goroutine: %s", botCfg.Name)
	}

	// Wait for shutdown signal
	<-sigChan
	log.Println("Shutting down...")

	// Note: In a real app, we'd cancel contexts for each bot goroutine.
	// For now, the program exits after signal.
	wg.Wait()
	log.Println("All bots shut down.")
}
