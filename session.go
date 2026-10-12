package main

import (
	"log"
	"strconv"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// BotSession holds every piece of per-bot runtime state so that all transports
// (Telegram, web) drive the *same* command dispatcher against the *same* state.
//
// Before this type existed the state lived in closure variables inside runBot,
// which meant the command switch was only reachable from the Telegram polling
// loop and the web handler had to re-derive provider/role/history from disk.
// That duplication is what caused the web page to ignore commands like
// `.model`, `.role` and `.status`.
//
// Concurrency: only one user is ever active (a single Telegram user ID), and
// that user is expected to use one transport at a time. A single mutex is
// therefore enough - there is never a second waiter to starve, so holding it
// across an LLM call is harmless and keeps the reasoning simple.
type BotSession struct {
	mu sync.Mutex

	botCfg    BotConfig
	userID    int64
	ollamaCfg OllamaConfig

	// bot is nil until Telegram authentication succeeds. A nil bot means
	// "Telegram transport unavailable" - the web transport still works, and
	// Telegram-only side effects (message deletion, voice audio) become no-ops.
	bot *tgbotapi.BotAPI

	botParams            BotParams
	selectedProvider     int
	currentMode          string
	currentStory         string
	activeScenes         map[int]bool
	activeCharacters     map[int]bool
	conversationContext  []ContextMessage
	contextLimit         int
	voiceCharAssignments map[string]string

	// Telegram-transport bookkeeping (unused on the web transport).
	ledger              MessageLedger
	lastUserMsgID       int
	lastAssistantMsgIDs []int
	lastUserText        string
}

// ── Session registry ───────────────────────────────────────────────────
// Both the Telegram polling loop and the web HTTP handlers need to find the
// live session for a bot name. Registration happens in main() *before* any
// Telegram connection is attempted so that a bad token degrades to
// "web only" instead of taking the web UI down with it.

var (
	sessionsMu  sync.RWMutex
	botSessions = map[string]*BotSession{}
)

func registerSession(s *BotSession) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	botSessions[s.botCfg.Name] = s
}

func getSession(botName string) *BotSession {
	sessionsMu.RLock()
	defer sessionsMu.RUnlock()
	return botSessions[botName]
}

// newBotSession rebuilds the in-memory state for a bot from the persisted
// config and chat log. It performs no network access, so it is safe to call
// for every configured bot at startup regardless of token validity.
func newBotSession(botCfg BotConfig, userID int64, ollamaCfg OllamaConfig) *BotSession {
	defaultModel := ""
	if len(ollamaCfg.Models) > 0 {
		defaultModel = ollamaCfg.Models[0].Model
	}
	params := loadBotParams(botCfg.Name, defaultModel)

	s := &BotSession{
		botCfg:    botCfg,
		userID:    userID,
		ollamaCfg: ollamaCfg,
		botParams: params,
	}

	// Validate the selected provider against the available models.
	s.selectedProvider = params.SelectedProvider
	if s.selectedProvider < 0 || s.selectedProvider >= len(ollamaCfg.Models) {
		s.selectedProvider = 0
	}

	// Apply the persisted model to the selected provider.
	if params.CurrentModel != "" && s.selectedProvider < len(s.ollamaCfg.Models) {
		s.ollamaCfg.Models[s.selectedProvider].Model = params.CurrentModel
	}

	// Track the current mode: "chat" or "story".
	s.currentMode = params.CurrentMode
	if s.currentMode != "chat" && s.currentMode != "story" {
		s.currentMode = "chat"
	}

	s.contextLimit = 50

	// Restore voice character assignments so they survive restarts.
	s.voiceCharAssignments = make(map[string]string)
	if params.VoiceAssignments != nil {
		for k, v := range params.VoiceAssignments {
			s.voiceCharAssignments[k] = v
		}
	}
	SyncVoiceAssignments(&s.botParams, s.voiceCharAssignments)

	// Restore conversation context from the chat log on disk.
	chatLogPath := getChatLogPath(s.botParams.CurrentStory, s.botCfg.Name)
	if loaded := loadChatHistory(chatLogPath); len(loaded) > 0 {
		s.conversationContext = loaded
	}

	// Restore story/scene/character state.
	s.currentStory = params.CurrentStory
	if s.currentStory == "" {
		s.currentStory = "general"
	}
	s.activeScenes = make(map[int]bool)
	s.activeCharacters = make(map[int]bool)
	for _, idx := range params.ActiveScenes {
		s.activeScenes[idx] = true
	}
	for _, idx := range params.ActiveCharacters {
		s.activeCharacters[idx] = true
	}

	return s
}

// save persists the current bot params to config/<botname>_bot_params.yaml.
func (s *BotSession) save() {
	saveBotParams(s.botCfg.Name, s.botParams)
}

// selectStory switches the bot to the named story folder: it clears the
// active scenes and characters (those lists are per-story), persists the
// selection, and reloads the conversation context from the target story's
// chat history so the bot stops carrying the previous story's memory.
// The number of messages loaded is returned for the user-facing reply.
func (s *BotSession) selectStory(name string) int {
	s.currentStory = name
	s.activeScenes = make(map[int]bool)
	s.activeCharacters = make(map[int]bool)
	s.botParams.CurrentStory = name
	s.botParams.ActiveScenes = []int{}
	s.botParams.ActiveCharacters = []int{}
	s.save()

	// Swap in the new story's history (empty when it has no log yet).
	s.conversationContext = loadStoryContext(name, s.botCfg.Name)
	if s.conversationContext == nil {
		s.conversationContext = []ContextMessage{}
	}
	// The tracked Telegram messages belong to the story we just left, so
	// forget them: otherwise "del" would delete unrelated messages and trim
	// the freshly loaded context of the new story.
	s.lastUserMsgID = 0
	s.lastAssistantMsgIDs = nil

	return len(s.conversationContext)
}

// estimateTokens provides a rough estimate of token count from the context messages.
func estimateTokens(ctx []ContextMessage) int {
	total := 0
	for _, msg := range ctx {
		total += len(msg.Content)
	}
	return total / 4 // rough estimate: ~4 chars per token
}

// sendContextWarning sends a disappearing warning if the context is getting
// too large. Telegram-only: it is a no-op when no Telegram transport exists.
func (s *BotSession) sendContextWarning(chatID int64) {
	if s.bot == nil || s.botParams.NumCtx <= 0 {
		return
	}
	estimatedTokens := estimateTokens(s.conversationContext)
	if estimatedTokens == 0 {
		return
	}
	// Warn if we're above 85% of the context limit.
	threshold := int(float64(s.botParams.NumCtx) * 0.85)
	if estimatedTokens < threshold {
		return
	}
	ctxK := s.botParams.NumCtx / 1024
	warning := "⚠️ You are reaching the context limit with your chat/story. Consider increasing it from " +
		strconv.Itoa(ctxK) + "K (" + strconv.Itoa(s.botParams.NumCtx) + ") using .ctxsize [nk]"
	if sentMsg, err := s.bot.Send(tgbotapi.NewMessage(chatID, warning)); err == nil {
		s.ledger.Add(sentMsg.MessageID, LedgerCommandResponse)
		go func(cid int64, mid int) {
			time.Sleep(deleteAfterDuration)
			if _, err := s.bot.Request(tgbotapi.NewDeleteMessage(cid, mid)); err != nil {
				log.Printf("[%s] Failed to delete warning message: %v", s.botCfg.Name, err)
			}
		}(sentMsg.Chat.ID, sentMsg.MessageID)
	}
}

// ── Telegram-transport helpers ─────────────────────────────────────────
// Each of these is a no-op when the message did not arrive over Telegram, so
// the command logic in commands.go stays transport-neutral.

// sendTyping shows the Telegram typing indicator while work is in progress.
func (s *BotSession) sendTyping(msg IncomingMessage) {
	if s.bot == nil || msg.Source != SourceTelegram {
		return
	}
	s.bot.Send(tgbotapi.NewChatAction(msg.ChatID, tgbotapi.ChatTyping))
}

// trackMessage sends a progress message and returns its ID (0 on web).
func (s *BotSession) trackMessage(msg IncomingMessage, text string) int {
	if s.bot == nil || msg.Source != SourceTelegram {
		return 0
	}
	if sent := sendAndTrack(s.bot, msg.ChatID, text, &s.ledger); sent != nil {
		return sent.MessageID
	}
	return 0
}

// untrackMessage deletes a previously tracked progress message.
func (s *BotSession) untrackMessage(msg IncomingMessage, messageID int) {
	if messageID == 0 || s.bot == nil || msg.Source != SourceTelegram {
		return
	}
	deleteTrackedMessage(s.bot, msg.ChatID, messageID)
}

// deleteTelegramMessage removes a message from the Telegram chat UI and marks
// it for purging from the ledger. On web there are no Telegram message IDs to
// clean up, so it does nothing at all.
func (s *BotSession) deleteTelegramMessage(msg IncomingMessage, messageID int, idsToRemove map[int]bool) {
	if s.bot == nil || msg.Source != SourceTelegram {
		return
	}
	safeDeleteWithLedger(s.bot, msg.ChatID, messageID, idsToRemove)
}

// generateVoice renders spoken audio for text using the session's current voice
// settings (single voice or multi-voice character mode) and returns the raw
// audio bytes, or nil when voice is disabled. It is transport-neutral so the
// web transport can produce the same audio the Telegram adapter would send.
//
// The caller must NOT hold s.mu: HandleMessage already released it before the
// transport runs its post-reply side effects, and voice generation is slow.
// Like the Telegram adapter, it hands generateCharacterVoiceBytes the live
// assignments map so newly chosen character voices persist for future turns.
func (s *BotSession) generateVoice(text string) ([]byte, error) {
	s.mu.Lock()
	voice := s.botParams.Voice
	voiceChar := s.botParams.VoiceChar
	botParams := s.botParams
	modelEntry := s.ollamaCfg.Models[s.selectedProvider]
	assignments := &s.voiceCharAssignments
	s.mu.Unlock()

	if !voice {
		return nil, nil
	}
	if voiceChar {
		return generateCharacterVoiceBytes(text, s.botCfg.Name, &botParams, modelEntry, assignments)
	}
	return speakToBytes(text, botParams.VoiceSpeed, botParams.VoiceStripPunct)
}

