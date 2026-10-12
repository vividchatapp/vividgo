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

// commandArg extracts the argument text of a raw command message, skipping the
// first `skip` whitespace-separated tokens. Unlike strings.Fields (which splits
// on every space), this keeps the whole remainder of the line, so multi-word
// arguments such as chapter names ("The Awakening of Sakura") survive, and it
// strips surrounding quotes when the user typed them (chapter save "My Chapter").
func commandArg(raw string, skip int) string {
	rest := raw
	for i := 0; i < skip; i++ {
		trimmed := strings.TrimLeft(rest, " \t\r\n")
		sep := strings.IndexAny(trimmed, " \t\r\n")
		if sep == -1 {
			return ""
		}
		rest = trimmed[sep:]
	}
	return trimQuotes(strings.TrimSpace(rest))
}

// trimQuotes removes a single matching pair of surrounding quotes (straight or
// typographic) from s, along with any whitespace left behind.
func trimQuotes(s string) string {
	pairs := [][2]string{
		{"\"", "\""},
		{"'", "'"},
		{"\u201c", "\u201d"},
		{"\u2018", "\u2019"},
	}
	for _, p := range pairs {
		if len(s) >= len(p[0])+len(p[1]) && strings.HasPrefix(s, p[0]) && strings.HasSuffix(s, p[1]) {
			return strings.TrimSpace(s[len(p[0]) : len(s)-len(p[1])])
		}
	}
	return s
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

// resolveStripPunct maps a ".voice punct" argument to the VoiceStripPunct setting.
// "on"/"keep" keeps punctuation (strip=false); "off"/"strip" strips it (strip=true).
// ok is false when the argument is not recognised.
func resolveStripPunct(arg string) (strip bool, ok bool) {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "on", "keep":
		return false, true
	case "off", "strip":
		return true, true
	default:
		return false, false
	}
}

// BotConfig holds the configuration for a single bot
type BotConfig struct {
	Name  string `yaml:"name"`
	Token string `yaml:"token"`
}

// ProviderType identifies which LLM backend a ModelEntry routes to.
const (
	ProviderTypeOllama = "ollama"
	ProviderTypeGoogle = "google"
)

// providerTypeOf normalizes a ModelEntry type, defaulting legacy entries to ollama.
func providerTypeOf(e ModelEntry) string {
	t := strings.ToLower(strings.TrimSpace(e.Type))
	if t == ProviderTypeGoogle || t == "gemini" {
		return ProviderTypeGoogle
	}
	return ProviderTypeOllama
}

// ModelEntry holds a single model configuration with name, API key, and API base URL
type ModelEntry struct {
	Name    string `yaml:"name"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
	APIBase string `yaml:"api_base"`
	Type    string `yaml:"type,omitempty"`
}

// OllamaConfig holds the API configuration for online models.
// Each entry in Models carries its own api_base so different providers
// (ollama.com, local Ollama, llama.cpp server, OpenAI-compatible, ...) can be mixed.
// The top-level api_base field is legacy: still parsed so old config.yaml files
// keep working, but it is never written anymore and is migrated into per-model values.
type OllamaConfig struct {
	APIBase string       `yaml:"api_base,omitempty"`
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
				if len(cfg.Ollama.Models) == 0 {
					cfg.Ollama.Models = []ModelEntry{
						{Name: "default", Model: "gemma4:31b", APIBase: "https://api.ollama.com"},
					}
				}
				migrateOllamaBases(&cfg)
				log.Printf("Loaded %d provider(s) (per-model api_base)", len(cfg.Ollama.Models))
				for _, model := range cfg.Ollama.Models {
					log.Printf("  - %s: model=%s, api_base=%s, api_key=%s", model.Name, model.Model, getEffectiveAPIBase(model), maskKey(model.APIKey))
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

			// Load Ollama config from environment variables (per-model api_base).
			envBase := os.Getenv("OLLAMA_API_BASE")
			if envBase == "" {
				envBase = "https://api.ollama.com"
			}
			cfg.Ollama.Models = []ModelEntry{
				{
					Name:    "default",
					APIKey:  os.Getenv("OLLAMA_API_KEY"),
					Model:   os.Getenv("OLLAMA_MODEL"),
					APIBase: envBase,
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

// migrateOllamaBases moves the legacy top-level ollama.api_base into any model
// entry that lacks its own api_base, then clears the global. Old config.yaml
// files keep working; new files only carry per-model api_base values.
func migrateOllamaBases(cfg *Config) {
	global := strings.TrimSpace(cfg.Ollama.APIBase)
	if global == "" {
		global = "https://api.ollama.com"
	}
	for i := range cfg.Ollama.Models {
		if strings.TrimSpace(cfg.Ollama.Models[i].APIBase) == "" {
			cfg.Ollama.Models[i].APIBase = global
		}
	}
	// Drop the legacy global so mixed providers are explicit per model.
	cfg.Ollama.APIBase = ""
}

// getEffectiveAPIBase returns the per-model API base. The optional globalBase
// arg is only honored for legacy configs that still carry ollama.api_base.
func getEffectiveAPIBase(modelEntry ModelEntry, globalBase ...string) string {
	if strings.TrimSpace(modelEntry.APIBase) != "" {
		return strings.TrimRight(modelEntry.APIBase, "/")
	}
	if len(globalBase) > 0 && strings.TrimSpace(globalBase[0]) != "" {
		return strings.TrimRight(globalBase[0], "/")
	}
	return "https://api.ollama.com"
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
	// Punctuation is kept by default (VoiceStripPunct=false). Use ".voice punct off"
	// or set voice_strip_punct: true in the param file to enable the legacy
	// quoted-dialogue punctuation stripping behavior.

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

// normalizeStoryName converts user input into a folder-safe story name.
// Whitespace is collapsed into single underscores and the result is lowercased
// to match the existing naming convention (e.g. "My New Story" becomes
// "my_new_story"). It returns "" when the input cannot form a valid story name.
func normalizeStoryName(name string) string {
	slug := strings.Join(strings.Fields(name), "_")
	if slug == "" {
		return ""
	}
	slug = sanitizeFilename(strings.ToLower(slug))
	// sanitizeFilename does not cover ":" or "?", both of which are invalid in
	// Windows folder names, so replace them here too.
	slug = strings.NewReplacer(":", "_", "?", "_").Replace(slug)
	// Collapse underscore runs left behind by replaced separators, so
	// "My Story: Part 2" becomes "my_story_part_2" rather than "my_story__part_2".
	for strings.Contains(slug, "__") {
		slug = strings.ReplaceAll(slug, "__", "_")
	}
	// Trim leading/trailing dots and dashes: Windows rejects folder names that
	// end in a dot, and leading dots would create hidden folders on Unix.
	slug = strings.Trim(slug, "._-")
	if slug == "" {
		return ""
	}
	return slug
}

// createStoryFolders creates stories/<name>/ together with the chat, scenes and
// characters sub-folders (the same layout as the default "general" story).
// The bool return reports whether the story folder already existed, so callers
// can tell a fresh story apart from an existing one. MkdirAll makes the call
// idempotent, so switching to an existing story is harmless.
func createStoryFolders(name string) (bool, error) {
	root := filepath.Join("stories", name)
	existed := false
	if info, err := os.Stat(root); err == nil && info.IsDir() {
		existed = true
	}
	for _, sub := range []string{"", "chat", "scenes", "characters"} {
		dir := filepath.Join(root, sub)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return existed, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	return existed, nil
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

func loadChapterSnapshotByIndex(storyName, botName string, index int) ([]ContextMessage, error) {
	snapshots, err := listStorySnapshots(storyName, botName)
	if err != nil {
		return nil, err
	}
	if len(snapshots) == 0 {
		return nil, fmt.Errorf("no saved chapters found in story %s", storyName)
	}
	if index < 1 || index > len(snapshots) {
		return nil, fmt.Errorf("invalid chapter number. choose 1-%d", len(snapshots))
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

// loadStoryContext loads a story's saved chat history for a bot and returns it
// as conversation context. It is used when switching stories so the in-memory
// context follows the story being switched to; a story without a chat log yet
// yields an empty context (getChatLogPath creates the chat folder if needed).
func loadStoryContext(story, botName string) []ContextMessage {
	return loadChatHistory(getChatLogPath(story, botName))
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

// listOllamaModels fetches available models. It tries the native Ollama
// endpoint (/api/tags) first, then falls back to the OpenAI-compatible
// endpoint (/v1/models) used by llama.cpp server and other providers.
func listOllamaModels(apiBase string, apiKey string) ([]OllamaModel, error) {
	baseURL := strings.TrimRight(apiBase, "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1")

	client := &http.Client{Timeout: 30 * time.Second}
	doGet := func(endpoint string) ([]byte, int, error) {
		req, err := http.NewRequest("GET", endpoint, nil)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create request: %w", err)
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to fetch models: %w", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to read response: %w", err)
		}
		return body, resp.StatusCode, nil
	}

	// 1. Native Ollama: GET {base}/api/tags -> {"models":[{"name":...,"size":...}]}
	if body, status, err := doGet(baseURL + "/api/tags"); err == nil && status == http.StatusOK {
		var modelsResp OllamaModelsResponse
		if err := json.Unmarshal(body, &modelsResp); err == nil && len(modelsResp.Models) > 0 {
			return modelsResp.Models, nil
		}
	}

	// 2. llama.cpp / OpenAI-compatible: GET {base}/v1/models -> {"data":[{"id":...}]}
	if body, status, err := doGet(baseURL + "/v1/models"); err == nil && status == http.StatusOK {
		var openAIResp struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &openAIResp); err == nil && len(openAIResp.Data) > 0 {
			models := make([]OllamaModel, 0, len(openAIResp.Data))
			for _, m := range openAIResp.Data {
				if m.ID != "" {
					models = append(models, OllamaModel{Name: m.ID})
				}
			}
			if len(models) > 0 {
				return models, nil
			}
		}
		return nil, fmt.Errorf("API returned no models at %s/v1/models", baseURL)
	} else if err != nil {
		return nil, err
	} else {
		return nil, fmt.Errorf("API returned status %d: %s", status, string(body))
	}
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

// callOllamaAPI sends a chat request and returns the response text.
// Native Ollama bases use POST /api/chat. Bases ending in /v1 (llama.cpp
// server, OpenAI-compatible) route straight to POST /v1/chat/completions.
func callOllamaAPI(botName string, apiBase string, modelEntry ModelEntry, systemPrompt string, context []ContextMessage, userMessage string, numCtx int, noThink bool) (string, error) {
	// Determine the endpoint URL
	baseURL := strings.TrimRight(apiBase, "/")
	if strings.HasSuffix(baseURL, "/v1") {
		// llama.cpp server / OpenAI-compatible base: use chat completions directly.
		return callOllamaAPIOpenAI(botName, baseURL+"/chat/completions", modelEntry, systemPrompt, context, userMessage)
	}
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
	// Mirror the audio to open web chat pages so the browser can play it too.
	publishVoiceAudio(botName, audioBytes)
}

// runBot attaches the Telegram transport to an existing BotSession and drives
// it from the long-polling loop.
//
// All command handling lives in HandleMessage (commands.go); this function is
// only responsible for Telegram-specific rendering: MarkdownV2 escaping,
// chunking, auto-delete of command replies, the context-limit warning and
// voice audio. The session itself is created and registered in main() so the
// web transport keeps working even when the token here is invalid.
func runBot(s *BotSession, wg *sync.WaitGroup) {
	defer wg.Done()

	bot, err := tgbotapi.NewBotAPI(s.botCfg.Token)
	if err != nil {
		log.Printf("[%s] Failed to start bot: %v", s.botCfg.Name, err)
		return
	}

	bot.Debug = false // can be overridden from config if needed

	// Register sender so web chat can forward replies to Telegram (shared session).
	registerTelegramSender(s.botCfg.Name, bot)

	s.mu.Lock()
	s.bot = bot
	userID := s.userID
	botCfg := s.botCfg
	s.mu.Unlock()

	log.Printf("[%s] Authorized on account %s", s.botCfg.Name, bot.Self.UserName)

	// Create a new update config with long polling timeout
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 180

	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil || update.Message.Text == "" {
			continue
		}

		// Only respond to the configured user ID
		if update.Message.From.ID != userID {
			log.Printf("[%s] Ignored message from unauthorized user %d", botCfg.Name, update.Message.From.ID)
			continue
		}

		log.Printf("[%s] Received message from user %d: %s", botCfg.Name, userID, update.Message.Text)

		result := s.HandleMessage(IncomingMessage{
			Text:      update.Message.Text,
			Source:    SourceTelegram,
			ChatID:    update.Message.Chat.ID,
			MessageID: update.Message.MessageID,
		})
		if result.Text == "" {
			continue
		}

		switch {
		case result.IsCommand:
			// Command replies disappear after deleteAfterDuration to keep the
			// chat UI clean, unless the command asked to be kept.
			if result.Persist {
				sendChunkedMessage(bot, update.Message.Chat.ID, result.Text, &s.ledger)
			} else {
				sendAndScheduleDelete(bot, update.Message.Chat.ID, result.Text, &s.ledger)
			}

		case result.Plain:
			// .ask sends plain text so LLM output containing special characters
			// does not break Markdown parsing.
			respMsg := tgbotapi.NewMessage(update.Message.Chat.ID, result.Text)
			if sentMsg, serr := bot.Send(respMsg); serr != nil {
				log.Printf("[%s] Failed to send ask response: %v", botCfg.Name, serr)
			} else {
				s.ledger.Add(sentMsg.MessageID, LedgerCommandResponse)
			}

		default:
			sendAIResponse(bot, update.Message.Chat.ID, result.Text, &s.ledger, &s.lastAssistantMsgIDs, botCfg.Name, userID)
		}

		// Send the context-limit warning after the reply so the user sees the
		// message first (it was previously sent before the reply and easy to miss).
		if result.ContextWarn {
			s.sendContextWarning(update.Message.Chat.ID)
		}

		// If this was an AI-generated response and voice is enabled, send the audio too
		if result.Spoken && !result.Plain {
			// The voice helpers replace the assignments map wholesale
			// (*assignments = make(...)), so hand them the session field itself
			// rather than a copy - otherwise new character voices are discarded.
			// The mutex is already held for the whole dispatch, so reading the
			// fields here is safe.
			voiceParams := s.botParams
			if voiceParams.VoiceChar {
				sendVoiceCharacterAudio(bot, update.Message.Chat.ID, result.Text, botCfg.Name,
					&voiceParams, s.ollamaCfg.Models[s.selectedProvider], s.ollamaCfg.APIBase,
					&s.voiceCharAssignments, &s.ledger)
			} else {
				sendVoiceAudio(bot, update.Message.Chat.ID, result.Text, botCfg.Name, &voiceParams)
			}
		}
	}
}

// sendAIResponse renders an AI reply to Telegram: it chunks long text, escapes
// MarkdownV2, tracks the sent message IDs for /del, and falls back to plain
// text when Telegram rejects the formatting.
func sendAIResponse(bot *tgbotapi.BotAPI, chatID int64, text string, ledger *MessageLedger, assistantIDs *[]int, botName string, userID int64) {
	// Use a safer maximum length for MarkdownV2 to account for escape characters
	const maxMessageLength = 3500
	remainingText := text

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

		respMsg := tgbotapi.NewMessage(chatID, escapedChunk)
		// Use MarkdownV2 for modern block/bullet styling
		respMsg.ParseMode = tgbotapi.ModeMarkdownV2

		if sentMsg, err := bot.Send(respMsg); err != nil {
			log.Printf("[%s] Failed to send message chunk: %v", botName, err)

			// Fallback: If MarkdownV2 fails, try sending as plain text so you don't lose the message
			respMsg.ParseMode = ""
			if retrySent, retryErr := bot.Send(respMsg); retryErr != nil {
				log.Printf("[%s] Critical failure sending plain text fallback: %v", botName, retryErr)
			} else {
				ledger.Add(retrySent.MessageID, LedgerChatAssistant)
				// Track assistant response chunk IDs for /del UI cleanup
				*assistantIDs = append(*assistantIDs, retrySent.MessageID)
			}
			break
		} else {
			ledger.Add(sentMsg.MessageID, LedgerChatAssistant)
			// Track assistant response chunk IDs for /del UI cleanup
			*assistantIDs = append(*assistantIDs, sentMsg.MessageID)
			log.Printf("[%s] Sent response chunk to user %d", botName, userID)
		}

		remainingText = remainingText[len(chunk):]
	}
}

func main() {
	// Parse command-line flags
	initMode := flag.Bool("init", false, "Re-run configuration wizard")
	webPort := flag.String("port", "8080", "Web UI port (setup + chat)")
	noWeb := flag.Bool("no-web", false, "Disable the web UI")
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

	// Share config with the web backend (setup + chat + Telegram bridge).
	setWebConfig(&cfg)

	// Load the filtered models cache from disk on startup
	loadFilteredModels()

	// Start the backend-first web UI (setup wizard + chat). Runs alongside bots.
	if noWeb != nil && *noWeb {
		log.Println("Web UI disabled (--no-web).")
	} else {
		port := "8080"
		if webPort != nil && *webPort != "" {
			port = *webPort
		}
		if envPort := os.Getenv("PORT"); envPort != "" && (webPort == nil || *webPort == "8080") {
			port = envPort
		}
		go startWebServer("127.0.0.1:" + port)
		log.Printf("Open setup/chat at http://localhost:%s", port)
	}

	// Handle graceful shutdown on SIGINT/SIGTERM
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	var wg sync.WaitGroup

	// Build and register a session for every configured bot *before* any
	// Telegram connection is attempted. This ordering matters: if a token is
	// invalid runBot returns early, but the session must already exist so the
	// web transport still works for that bot.
	for _, botCfg := range cfg.Bots {
		session := newBotSession(botCfg, cfg.UserID, cfg.Ollama)
		registerSession(session)
		log.Printf("Initialized %s with provider=%d, mode=%s, model=%s, story=%s",
			botCfg.Name, session.selectedProvider, session.currentMode,
			session.botParams.CurrentModel, session.currentStory)
	}

	// Start a goroutine for each bot
	for _, botCfg := range cfg.Bots {
		session := getSession(botCfg.Name)
		if session == nil {
			continue
		}
		wg.Add(1)
		go runBot(session, &wg)
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
