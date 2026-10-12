package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
)

// ── Transport-neutral command layer ────────────────────────────────────
//
// This file is the single "action area" for the bot. Every transport
// (Telegram, web) funnels its incoming text through HandleMessage, which
// parses the command, mutates the session, and *returns* the reply text.
// It never touches tgbotapi directly: Telegram-specific rendering
// (MarkdownV2 escaping, chunking, auto-delete, voice audio) lives in the
// Telegram adapter in main.go, and web rendering lives in web.go.

// MessageSource identifies which transport a message arrived on.
type MessageSource string

const (
	// SourceTelegram is a message from the Telegram long-polling loop.
	SourceTelegram MessageSource = "telegram"
	// SourceWeb is a message from the /api/chat HTTP handler.
	SourceWeb MessageSource = "web"
)

// IncomingMessage is the transport-neutral envelope handed to the dispatcher.
// ChatID and MessageID are only meaningful on the Telegram transport; the web
// transport passes zero values and Telegram-only side effects become no-ops.
type IncomingMessage struct {
	Text      string
	Source    MessageSource
	ChatID    int64
	MessageID int
}

// CommandResult is what the dispatcher hands back to the transport.
type CommandResult struct {
	// Text is the reply to show the user. Empty means "say nothing".
	Text string
	// Spoken requests voice playback of Text (Telegram only).
	Spoken bool
	// ContextWarn requests the context-limit warning after the reply
	// (Telegram only).
	ContextWarn bool
	// IsCommand marks a command reply rather than a chat reply, so the web UI
	// can style it differently and the Telegram adapter can auto-delete it.
	IsCommand bool
	// Persist suppresses the Telegram auto-delete timer (`.history n keep`).
	Persist bool
	// Plain sends the reply without MarkdownV2 parsing, for LLM output that
	// contains characters MarkdownV2 would choke on (`.ask`).
	Plain bool
}

// knownCommand reports whether name is a recognised command word. Used by
// parseCommand to decide whether a bare (dotless) word is a command.
func knownCommand(name string) bool {
	switch name {
	case "help", "h",
		"provider", "p",
		"mode", "mo",
		"role", "r",
		"reload",
		"clear",
		"clean", "cl",
		"rs",
		"del",
		"ctxsize", "ctx", "mc", "llmctx",
		"context",
		"history", "hs",
		"status", "s",
		"think",
		"nothink", "no",
		"verbose",
		"voice",
		"model", "m",
		"mf",
		"trace",
		"story",
		"chapter", "ch",
		"scene", "sc",
		"char", "c",
		"ask",
		"resend":
		return true
	}
	return false
}

// parseCommand splits raw text into the command word and its arguments and
// decides whether it should be treated as a command at all.
//
// Telegram accepts both `.model 3` and bare `model 3`. The web transport only
// accepts the dotted form: the default branch replies "<word> ignored" for any
// unrecognised single word, so honouring bare words on web would swallow normal
// one-word chat messages like "hello".
func parseCommand(text string, source MessageSource) (command string, parts []string, isCommand bool) {
	parts = strings.Fields(text)
	if len(parts) == 0 {
		return "", nil, false
	}

	dotted := strings.HasPrefix(parts[0], ".")
	command = strings.TrimPrefix(strings.ToLower(parts[0]), ".")

	if dotted {
		return command, parts, true
	}
	if source == SourceTelegram {
		return command, parts, true
	}
	// Web, undotted: only treat it as a command when it is a known command
	// word, so ordinary chat text always reaches the LLM.
	if knownCommand(command) {
		return command, parts, true
	}
	return "", parts, false
}

// HandleMessage is the single entry point both transports use.
//
// It parses the incoming text, mutates the session for state-changing commands,
// and returns the reply text for the caller to render. It never talks to
// tgbotapi directly: Telegram-specific rendering (MarkdownV2 escaping,
// chunking, auto-delete, voice audio) lives in the Telegram adapter in main.go
// and web rendering lives in web.go.
//
// The mutex is held for the whole call, including LLM requests. That is safe
// here because there is only ever one user and one active transport, so there
// is no second request to block.
func (s *BotSession) HandleMessage(msg IncomingMessage) CommandResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	command, parts, isCommand := parseCommand(msg.Text, msg.Source)
	if len(parts) == 0 {
		return CommandResult{}
	}

	// Record the incoming Telegram message in the ledger so /clean and /del can
	// find it again. Web messages have no Telegram message IDs.
	if s.bot != nil && msg.Source == SourceTelegram {
		if strings.HasPrefix(parts[0], ".") {
			s.ledger.Add(msg.MessageID, LedgerCommand)
		} else {
			s.ledger.Add(msg.MessageID, LedgerChatUser)
			// Track the last non-command user message ID for /del UI cleanup
			s.lastUserMsgID = msg.MessageID
		}
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
				isCommand = false
			}
		}
	}

	var responseText string
	// spoken is set for AI-generated responses (chat and resend) so the
	// Telegram adapter calls sendVoiceAudio after the text is sent.
	// Command responses are not spoken.
	spoken := false
	// contextWarn is set for AI-generated responses so the context-limit
	// warning is sent after the reply (it used to be sent before the reply,
	// which made it easy to miss).
	contextWarn := false

	switch command {
	case "help", "h":
		responseText = `🤖 i5 Assistant Help
📚 Stories & Chapters
• story - List story folders
• story [n] - Select story folder (clears scenes/chars, loads its chat history)
• story create [name] - Create a new story folder and switch to it
• story save [name] / story load [name] - Save/load a story snapshot
• chapter / ch - List saved chapters in the current story
• chapter [n] / ch [n] - Pick/load listed chapter by number
• chapter save [name] / ch save [name] - Save current chat as a chapter
• chapter load [n|name] / ch load [n|name] - Load a saved chapter

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
• clear - Wipe s.bot memory (context)
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
Synonyms: r=role, rs=rolesummary, p=provider, m=model, s=status, h=help, ch=chapter, c=chat, cl=clean, mf=modelsfiltered, sc=scene, hs=history, mo=mode, mc=llmctx`

	case "provider", "p":
		if len(parts) == 1 {
			// Just "provider" - list available models with numbers
			var sb strings.Builder
			sb.WriteString("Available providers:\n")
			sortedModels := make([]ModelEntry, len(s.ollamaCfg.Models))
			copy(sortedModels, s.ollamaCfg.Models)
			sort.Slice(sortedModels, func(i, j int) bool {
				return sortedModels[i].Name < sortedModels[j].Name
			})
			for i, model := range sortedModels {
				if model.Name == s.ollamaCfg.Models[s.selectedProvider].Name {
					sb.WriteString(fmt.Sprintf("  %d) %s ✅\n", i+1, model.Name))
				} else {
					sb.WriteString(fmt.Sprintf("  %d) %s\n", i+1, model.Name))
				}
			}
			responseText = sb.String()
			return CommandResult{Text: responseText, IsCommand: true}
		} else if len(parts) == 2 {
			// "provider <number>" - select a provider
			num, err := strconv.Atoi(parts[1])
			if err != nil || num < 1 || num > len(s.ollamaCfg.Models) {
				responseText = fmt.Sprintf("Invalid provider number. Please choose 1-%d.", len(s.ollamaCfg.Models))
			} else {
				// Sort providers alphabetically by name first so the numbering
				// matches the listing shown by "provider" (without arguments).
				sortedIndices := make([]int, len(s.ollamaCfg.Models))
				for i := range sortedIndices {
					sortedIndices[i] = i
				}
				sort.Slice(sortedIndices, func(i, j int) bool {
					return s.ollamaCfg.Models[sortedIndices[i]].Name < s.ollamaCfg.Models[sortedIndices[j]].Name
				})
				s.selectedProvider = sortedIndices[num-1]
				responseText = fmt.Sprintf("Selected provider: %s", s.ollamaCfg.Models[s.selectedProvider].Name)

				// Persist the provider selection
				s.botParams.SelectedProvider = s.selectedProvider
				s.botParams.CurrentModel = s.ollamaCfg.Models[s.selectedProvider].Model
				saveBotParams(s.botCfg.Name, s.botParams)

				// Send the response and schedule deletion after 10 seconds
				return CommandResult{Text: responseText, IsCommand: true}
			}
		} else {
			responseText = "Usage: provider [number] - list or select a provider"
		}

	case "mode":
		if len(parts) == 2 {
			mode := strings.ToLower(parts[1])
			if mode == "story" || mode == "chat" {
				s.currentMode = mode
				responseText = fmt.Sprintf("%s mode enabled", mode)

				// Persist the mode selection
				s.botParams.CurrentMode = s.currentMode
				saveBotParams(s.botCfg.Name, s.botParams)

				// Send the response and schedule deletion after deleteAfterDuration
				return CommandResult{Text: responseText, IsCommand: true}
			} else {
				responseText = "Invalid mode. Use: mode chat or mode story"
				return CommandResult{Text: responseText, IsCommand: true}
			}
		} else {
			responseText = fmt.Sprintf("Current mode: %s\nUsage: mode [chat/story]", s.currentMode)
			return CommandResult{Text: responseText, IsCommand: true}
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
					if role == s.botParams.CurrentRole {
						sb.WriteString(fmt.Sprintf("  %d) %s ✅\n", i+1, role))
					} else {
						sb.WriteString(fmt.Sprintf("  %d) %s\n", i+1, role))
					}
				}
				responseText = sb.String()
			}
			return CommandResult{Text: responseText, IsCommand: true}
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
			if len(msg.Text) <= contentStart {
				responseText = "Usage: role save [name] [text] - provide role content after the name"
			} else {
				content := msg.Text[contentStart:]
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
					s.botParams.CurrentRole = roleName
					saveBotParams(s.botCfg.Name, s.botParams)
					responseText = fmt.Sprintf("Switched to role: %s", roleName)

					// Send the response and schedule deletion after deleteAfterDuration
					return CommandResult{Text: responseText, IsCommand: true}
				}
			}
		} else {
			responseText = "Usage: role [n] - list or select a role"
		}

	case "reload":
		// Reload the current role from disk (just confirm it exists)
		content := loadRoleContent(s.botParams.CurrentRole)
		responseText = fmt.Sprintf("Role '%s' reloaded (%d characters).", s.botParams.CurrentRole, len(content))
		return CommandResult{Text: responseText, IsCommand: true}

	case "clear":
		// Clear conversation context only (no UI cleanup)
		s.conversationContext = nil
		// Sync the chat log file with the empty context (truncate)
		chatLogPath := getChatLogPath(s.currentStory, s.botCfg.Name)
		rewriteChatLog(chatLogPath, s.conversationContext, s.currentMode)
		// Clear character voice assignments from memory and persist to YAML
		s.voiceCharAssignments = make(map[string]string)
		SyncVoiceAssignments(&s.botParams, s.voiceCharAssignments)
		saveBotParams(s.botCfg.Name, s.botParams)
		responseText = "Conversation context cleared."
		return CommandResult{Text: responseText, IsCommand: true}

	case "clean":
		if len(parts) >= 2 && parts[1] == "cmd" {
			// clean [cmd] - Surgical UI wipe: delete only command + command_response messages
			// Do NOT touch s.conversationContext - LLM brain stays intact
			entries := s.ledger.GetByTypes(LedgerCommand, LedgerCommandResponse)
			idsToRemove := make(map[int]bool)
			for _, e := range entries {
				s.deleteTelegramMessage(msg, e.MessageID, idsToRemove)
			}
			s.ledger.RemoveByIDs(idsToRemove)
			responseText = "Command interface cleaned. Conversation memory preserved."
			return CommandResult{Text: responseText, IsCommand: true}
		}
		// clean (no args) - Full hard reset: delete ALL messages, clear s.ledger, reset context
		allEntries := s.ledger.All()
		idsToRemove := make(map[int]bool)
		for _, e := range allEntries {
			s.deleteTelegramMessage(msg, e.MessageID, idsToRemove)
		}
		s.ledger.Clear()
		s.conversationContext = nil
		s.voiceCharAssignments = make(map[string]string)
		SyncVoiceAssignments(&s.botParams, s.voiceCharAssignments)
		saveBotParams(s.botCfg.Name, s.botParams)
		responseText = "Full cleanup complete. All messages deleted and memory wiped."
		return CommandResult{Text: responseText, IsCommand: true}

	case "rs":
		// Summarize current role
		content := loadRoleContent(s.botParams.CurrentRole)
		lines := strings.Split(content, "\n")
		lineCount := len(lines)
		charCount := len(content)
		// Show first 200 chars as preview
		preview := content
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		responseText = fmt.Sprintf("Role: %s\nLines: %d\nChars: %d\n\n%s", s.botParams.CurrentRole, lineCount, charCount, preview)
		return CommandResult{Text: responseText, IsCommand: true}

	case "del":
		// Delete the tracked Telegram messages from UI first
		idsToRemove := make(map[int]bool)
		if s.lastUserMsgID > 0 {
			s.deleteTelegramMessage(msg, s.lastUserMsgID, idsToRemove)
		}
		for _, aid := range s.lastAssistantMsgIDs {
			s.deleteTelegramMessage(msg, aid, idsToRemove)
		}
		s.ledger.RemoveByIDs(idsToRemove)
		// Reset tracking
		s.lastUserMsgID = 0
		s.lastAssistantMsgIDs = nil

		// Delete last user+assistant exchange from memory
		if len(s.conversationContext) == 0 {
			responseText = "No messages to delete."
		} else if s.currentMode == "chat" {
			if len(s.conversationContext) < 2 {
				responseText = "Not enough messages to delete a pair."
			} else {
				s.conversationContext = s.conversationContext[:len(s.conversationContext)-2]
				responseText = "Last exchange deleted."
			}
		} else {
			// Story mode: delete last assistant message only
			s.conversationContext = s.conversationContext[:len(s.conversationContext)-1]
			responseText = "Last assistant message deleted."
		}
		// Sync the chat log file with the trimmed context
		chatLogPath := getChatLogPath(s.currentStory, s.botCfg.Name)
		rewriteChatLog(chatLogPath, s.conversationContext, s.currentMode)
		return CommandResult{Text: responseText, IsCommand: true}

	case "ctxsize", "ctx":
		if len(parts) == 2 {
			arg := strings.ToLower(parts[1])
			if strings.HasSuffix(arg, "k") {
				num, err := strconv.Atoi(strings.TrimSuffix(arg, "k"))
				if err != nil || num < 1 {
					responseText = "Invalid context size. Use a number ending in k (e.g. 8k, 16k, 32k)."
				} else {
					s.botParams.NumCtx = num * 1024
					saveBotParams(s.botCfg.Name, s.botParams)
					responseText = fmt.Sprintf("Context set to %dK (%d)", num, s.botParams.NumCtx)
				}
			} else {
				responseText = "Invalid format. Use a number ending in k (e.g. 8k, 16k, 32k)."
			}
		} else {
			ctxK := s.botParams.NumCtx / 1024
			responseText = fmt.Sprintf("Current context size: %dK (%d)\nUsage: .ctxsize [nk] - set the model context window (e.g. 8k, 16k, 32k)", ctxK, s.botParams.NumCtx)
		}
		return CommandResult{Text: responseText, IsCommand: true}

	case "context":
		if len(parts) == 2 {
			num, err := strconv.Atoi(parts[1])
			if err != nil || num < 1 {
				responseText = "Invalid context limit. Please provide a positive number."
			} else {
				s.contextLimit = num
				responseText = fmt.Sprintf("Context limit set to %d messages.", s.contextLimit)
			}
		} else {
			responseText = fmt.Sprintf("Current context limit: %d messages.\nUsage: .context [n] - set the maximum number of messages to keep in context.", s.contextLimit)
		}
		return CommandResult{Text: responseText, IsCommand: true}

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

		if len(s.conversationContext) == 0 {
			responseText = "No conversation history."
			return CommandResult{Text: responseText, IsCommand: true}
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("📜 History (%d total messages, showing last %d):\n", len(s.conversationContext), numLines*2))
		startIdx := len(s.conversationContext) - numLines*2
		if startIdx < 0 {
			startIdx = 0
		}
		for i := startIdx; i < len(s.conversationContext); i++ {
			msg := s.conversationContext[i]
			content := msg.Content
			if !showFull && len(content) > 100 {
				content = content[:100] + "..."
			}
			sb.WriteString(fmt.Sprintf("%d) [%s] %s\n", i+1, msg.Role, content))
		}
		responseText = sb.String()

		return CommandResult{Text: responseText, IsCommand: true, Persist: keepMsg}

	case "status", "s":
		// Build a verbose status showing all current settings
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("👤 User: %d\n", s.userID))

		// Provider and model info
		modelName := s.ollamaCfg.Models[s.selectedProvider].Model
		sb.WriteString(fmt.Sprintf("🧠 Provider: %s\n", s.ollamaCfg.Models[s.selectedProvider].Name))
		sb.WriteString(fmt.Sprintf("🤖 Model: %s\n", modelName))
		ctxK := s.botParams.NumCtx / 1024
		sb.WriteString(fmt.Sprintf("📐 Context: %dK (%d)\n", ctxK, s.botParams.NumCtx))

		// Current role
		sb.WriteString(fmt.Sprintf("🎭 Role: %s\n", s.botParams.CurrentRole))

		// Mode
		sb.WriteString(fmt.Sprintf("📝 Mode: %s\n", s.currentMode))

		// Story info
		sb.WriteString(fmt.Sprintf("📖 Story: %s\n", displayName(s.currentStory)))

		// Active scenes
		scenes, _ := listSceneFiles(s.currentStory)
		var activeSceneNames []string
		for idx := range s.activeScenes {
			if idx >= 1 && idx <= len(scenes) {
				activeSceneNames = append(activeSceneNames, scenes[idx-1])
			}
		}
		if len(activeSceneNames) > 0 {
			sb.WriteString(fmt.Sprintf("🎦 Scenes: %s\n", strings.Join(activeSceneNames, ", ")))
		}

		// Active characters
		chars, _ := listCharacterFiles(s.currentStory)
		var activeCharNames []string
		for idx := range s.activeCharacters {
			if idx >= 1 && idx <= len(chars) {
				activeCharNames = append(activeCharNames, strings.TrimSuffix(chars[idx-1], ".txt"))
			}
		}
		if len(activeCharNames) > 0 {
			sb.WriteString(fmt.Sprintf("👥 Characters: %s\n", strings.Join(activeCharNames, ", ")))
		}

		// Conversation stats
		sb.WriteString(fmt.Sprintf("💬 Memory: %d messages (limit %d)\n", len(s.conversationContext), s.contextLimit))

		// Not think flag
		if s.botParams.NoThink {
			sb.WriteString("🚫 NoThink: enabled\n")
		}

		// Voice flag
		if s.botParams.Voice {
			sb.WriteString(fmt.Sprintf("🔊 Voice: enabled (speed %d)", s.botParams.VoiceSpeed))
			if s.botParams.VoiceChar {
				sb.WriteString(" [char mode]")
			}
			sb.WriteString("\n")
		}

		responseText = sb.String()
		return CommandResult{Text: responseText, IsCommand: true}

	case "think":
		newVal := !s.botParams.NoThink
		if newVal {
			s.botParams.NoThink = true
			responseText = "Think mode disabled (NoThink enabled)."
		} else {
			s.botParams.NoThink = false
			responseText = "Think mode enabled (NoThink disabled)."
		}
		saveBotParams(s.botCfg.Name, s.botParams)
		return CommandResult{Text: responseText, IsCommand: true}

	case "nothink", "no":
		if len(parts) >= 2 {
			arg := strings.ToLower(parts[1])
			if arg == "on" {
				s.botParams.NoThink = true
				saveBotParams(s.botCfg.Name, s.botParams)
				responseText = "NoThink enabled."
			} else if arg == "off" || arg == "think" {
				s.botParams.NoThink = false
				saveBotParams(s.botCfg.Name, s.botParams)
				responseText = "NoThink disabled."
			} else {
				responseText = fmt.Sprintf("Current NoThink: %v\nUsage: nothink [on/off] or no think [on/off]", s.botParams.NoThink)
			}
		} else {
			responseText = fmt.Sprintf("Current NoThink: %v\nUsage: nothink [on/off] or no think [on/off]", s.botParams.NoThink)
		}
		return CommandResult{Text: responseText, IsCommand: true}

	case "verbose":
		verboseMode := !logToFileEnabled
		logToFileEnabled = verboseMode
		if verboseMode {
			responseText = "Verbose logging enabled."
		} else {
			responseText = "Verbose logging disabled."
		}
		return CommandResult{Text: responseText, IsCommand: true}

	case "voice":
		// Voice output control: .voice on/off / .voice speed <1-10> / .voice char [on/off] / .voice punct [on/off]
		if len(parts) >= 3 && strings.ToLower(parts[1]) == "speed" {
			// .voice speed <1-10> - set voice speed (10% increments)
			speed, err := strconv.Atoi(parts[2])
			if err != nil || speed < 1 || speed > 10 {
				responseText = fmt.Sprintf("Invalid voice speed. Please choose 1-10.\nCurrent speed: %d", s.botParams.VoiceSpeed)
			} else {
				s.botParams.VoiceSpeed = speed
				saveBotParams(s.botCfg.Name, s.botParams)
				responseText = fmt.Sprintf("Voice speed set to %d (+%d%%).", speed, speed*10)
			}
		} else if len(parts) >= 2 && strings.ToLower(parts[1]) == "char" {
			// .voice char [on/off] - character voice mode (multiple voices per story)
			if len(parts) >= 3 {
				arg := strings.ToLower(parts[2])
				if arg == "on" {
					s.botParams.VoiceChar = true
					s.botParams.Voice = true // char mode implies voice is on
					saveBotParams(s.botCfg.Name, s.botParams)
					responseText = "Character voice mode enabled. AI responses will use multiple character-specific voices.\nVoice generation can have longer delays."
				} else if arg == "off" {
					s.botParams.VoiceChar = false
					s.botParams.VoiceAssignments = nil
					s.voiceCharAssignments = make(map[string]string)
					SyncVoiceAssignments(&s.botParams, s.voiceCharAssignments)
					saveBotParams(s.botCfg.Name, s.botParams)
					responseText = "Character voice mode disabled. Voice assignments cleared."
				} else {
					responseText = fmt.Sprintf("Voice char mode is %s\nUsage: voice char [on/off]", onOff(s.botParams.VoiceChar))
				}
			} else {
				responseText = fmt.Sprintf("Voice char mode is %s\nUsage: voice char [on/off]", onOff(s.botParams.VoiceChar))
			}
		} else if len(parts) >= 2 && strings.ToLower(parts[1]) == "punct" {
			// .voice punct [on/off] - keep (on, default) or strip (off) punctuation
			// in spoken audio. "off" strips punctuation from quoted dialogue, which
			// makes Edge TTS read without pauses (legacy run-on behaviour).
			if len(parts) >= 3 {
				if strip, ok := resolveStripPunct(parts[2]); ok {
					s.botParams.VoiceStripPunct = strip
					saveBotParams(s.botCfg.Name, s.botParams)
					if strip {
						responseText = "Punctuation will be stripped from spoken dialogue (run-on mode)."
					} else {
						responseText = "Punctuation will be kept in spoken audio."
					}
				} else {
					responseText = fmt.Sprintf("Punctuation is %s\nUsage: voice punct [on/off]", onOff(!s.botParams.VoiceStripPunct))
				}
			} else {
				responseText = fmt.Sprintf("Punctuation is %s\nUsage: voice punct [on/off]", onOff(!s.botParams.VoiceStripPunct))
			}
		} else if len(parts) >= 2 && strings.ToLower(parts[1]) == "on" {
			s.botParams.Voice = true
			saveBotParams(s.botCfg.Name, s.botParams)
			responseText = "Voice enabled. AI responses will include audio."
		} else if len(parts) >= 2 && strings.ToLower(parts[1]) == "off" {
			s.botParams.Voice = false
			saveBotParams(s.botCfg.Name, s.botParams)
			responseText = "Voice disabled."
		} else if len(parts) >= 2 && strings.ToLower(parts[1]) == "list" {
			// .voice list - show character→voice assignments
			SyncVoiceAssignments(&s.botParams, s.voiceCharAssignments)
			responseText = GetVoiceAssignmentsDisplay(s.voiceCharAssignments)
		} else if len(parts) >= 4 && strings.ToLower(parts[1]) == "change" {
			// .voice change [n] [accent] - ask AI to pick a new voice for a character
			// The index [n] refers to the numbered list shown by .voice list
			SyncVoiceAssignments(&s.botParams, s.voiceCharAssignments)
			if len(s.voiceCharAssignments) == 0 {
				responseText = "No character voice assignments yet. Use .voice char on and generate a response first."
			} else {
				num, err := strconv.Atoi(parts[2])
				if err != nil || num < 1 || num > len(s.voiceCharAssignments) {
					responseText = fmt.Sprintf("Invalid character number. Please choose 1-%d.", len(s.voiceCharAssignments))
				} else {
					// Get the character name at the given index (sorted order matches .voice list)
					var names []string
					for name := range s.voiceCharAssignments {
						names = append(names, name)
					}
					sort.Strings(names)
					characterName := names[num-1]

					// The accent is everything after "voice change [n]"
					accent := strings.TrimSpace(strings.TrimPrefix(msg.Text, parts[0]+" "+parts[1]+" "+parts[2]))
					if accent == "" {
						responseText = "Usage: voice change [n] [accent] - e.g. .voice change 2 deep southern accent"
					} else {
						// Send typing indicator while the AI selects a voice
						s.sendTyping(msg)

						modelEntry := s.ollamaCfg.Models[s.selectedProvider]
						changeMsg, err := ChangeCharacterVoice(s.botCfg.Name, &s.botParams, modelEntry, s.ollamaCfg.APIBase, &s.voiceCharAssignments, characterName, accent)
						if err != nil {
							log.Printf("[%s] Voice change failed: %v", s.botCfg.Name, err)
							responseText = fmt.Sprintf("Failed to change voice for '%s': %v", characterName, err)
						} else {
							responseText = changeMsg
						}
					}
				}
			}
		} else {
			responseText = fmt.Sprintf("Voice is %s, speed %d, char mode %s, punctuation %s\nUsage: voice on/off, voice speed 1-10, voice char on/off, voice punct on/off, voice list, voice change [n] [accent]", onOff(s.botParams.Voice), s.botParams.VoiceSpeed, onOff(s.botParams.VoiceChar), onOff(!s.botParams.VoiceStripPunct))
		}
		return CommandResult{Text: responseText, IsCommand: true}

	case "model", "m":
		if len(parts) >= 2 && parts[1] == "loaded" {
			// Force re-load the model from cached file
			loadFilteredModels()
			filteredModelsMu.Lock()
			models := make([]string, len(filteredModelsCache))
			copy(models, filteredModelsCache)
			filteredModelsMu.Unlock()
			responseText = fmt.Sprintf("Loaded %d filtered models from disk.", len(models))
			return CommandResult{Text: responseText, IsCommand: true}
		}
		if len(parts) == 1 {
			// Just "model" - list available models with numbers
			apiKey := s.ollamaCfg.Models[s.selectedProvider].APIKey
			models, err := listOllamaModels(getEffectiveAPIBase(s.ollamaCfg.Models[s.selectedProvider], s.ollamaCfg.APIBase), apiKey)
			if err != nil {
				responseText = fmt.Sprintf("Error fetching models: %v", err)
			} else {
				// Sort models to match the order displayed in the list command
				sort.Slice(models, func(i, j int) bool {
					return models[i].Name < models[j].Name
				})
				// Determine if we need to check subscription filtering (only for api.ollama.com)
				needsFiltering := isOllamaDotCom(getEffectiveAPIBase(s.ollamaCfg.Models[s.selectedProvider], s.ollamaCfg.APIBase))
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
					if m.Name == s.ollamaCfg.Models[s.selectedProvider].Model {
						sb.WriteString(fmt.Sprintf("  %d)%s %s%s ✅\n", i+1, prefix, m.Name, sizeStr))
					} else {
						sb.WriteString(fmt.Sprintf("  %d)%s %s%s\n", i+1, prefix, m.Name, sizeStr))
					}
				}
				responseText = sb.String()
			}
			return CommandResult{Text: responseText, IsCommand: true}
		} else if len(parts) == 2 && (parts[1] == "+" || parts[1] == "-") {
			// "model +" or "model -" - cycle to next/previous model
			apiKey := s.ollamaCfg.Models[s.selectedProvider].APIKey
			models, err := listOllamaModels(getEffectiveAPIBase(s.ollamaCfg.Models[s.selectedProvider], s.ollamaCfg.APIBase), apiKey)
			if err != nil {
				responseText = fmt.Sprintf("Error fetching models: %v", err)
				return CommandResult{Text: responseText, IsCommand: true}
			}
			{
				// Sort models to match the order displayed in the list command
				sort.Slice(models, func(i, j int) bool {
					return models[i].Name < models[j].Name
				})

				currentModel := s.ollamaCfg.Models[s.selectedProvider].Model
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
						s.ollamaCfg.Models[s.selectedProvider].Model = models[newIdx].Name
						s.botParams.CurrentModel = models[newIdx].Name
						saveBotParams(s.botCfg.Name, s.botParams)
						responseText = fmt.Sprintf("Switched to model: %s", models[newIdx].Name)
					}
				}

				// Send the response and schedule deletion after deleteAfterDuration
				return CommandResult{Text: responseText, IsCommand: true}
			}
		} else if len(parts) >= 2 && parts[1] == "test" {
			// "model test" - test all models for subscription/upgrade requirements
			// This must be checked BEFORE the "model <number>" handler below,
			// since "model test" also has len(parts) == 2.
			// model test - test all models for subscription/upgrade requirements
			apiKey := s.ollamaCfg.Models[s.selectedProvider].APIKey
			models, err := listOllamaModels(getEffectiveAPIBase(s.ollamaCfg.Models[s.selectedProvider], s.ollamaCfg.APIBase), apiKey)
			if err != nil {
				responseText = fmt.Sprintf("Error fetching models: %v", err)
				return CommandResult{Text: responseText, IsCommand: true}
			}

			responseText = fmt.Sprintf("Testing %d models for subscription requirements... This may take a while.", len(models))
			s.trackMessage(msg, responseText)

			modelEntry := s.ollamaCfg.Models[s.selectedProvider]
			var accessible []string
			var blocked []string

			// Track the previous status message ID for deletion
			var prevStatusMsgID int

			for _, m := range models {
				log.Printf("[%s] Testing model: %s", s.botCfg.Name, m.Name)

				// Send a status message showing which model is being tested
				statusText := fmt.Sprintf("testing model %s", m.Name)
				statusID := s.trackMessage(msg, statusText)
				if statusID != 0 {
					// Delete the previous status message if it exists
					if prevStatusMsgID != 0 {
						s.untrackMessage(msg, prevStatusMsgID)
					}
					prevStatusMsgID = statusID
				}

				if testModelAccess(getEffectiveAPIBase(modelEntry, s.ollamaCfg.APIBase), modelEntry, m.Name) {
					accessible = append(accessible, m.Name)
				} else {
					blocked = append(blocked, m.Name)
				}
			}

			// Delete the last status message since we're about to show results
			if prevStatusMsgID != 0 {
				s.untrackMessage(msg, prevStatusMsgID)
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
				apiKey := s.ollamaCfg.Models[s.selectedProvider].APIKey
				models, err := listOllamaModels(getEffectiveAPIBase(s.ollamaCfg.Models[s.selectedProvider], s.ollamaCfg.APIBase), apiKey)
				if err != nil {
					responseText = fmt.Sprintf("Error fetching models: %v", err)
				} else if num < 1 || num > len(models) {
					responseText = fmt.Sprintf("Invalid model number. Please choose 1-%d.", len(models))
				} else {
					// Sort models to match the order displayed in the list command
					sort.Slice(models, func(i, j int) bool {
						return models[i].Name < models[j].Name
					})
					s.ollamaCfg.Models[s.selectedProvider].Model = models[num-1].Name
					s.botParams.CurrentModel = models[num-1].Name
					saveBotParams(s.botCfg.Name, s.botParams)
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
			return CommandResult{Text: responseText, IsCommand: true}
		}

		if len(parts) == 1 {
			// mf - list filtered models
			var sb strings.Builder
			sb.WriteString("Accessible models:\n")
			currentModel := s.ollamaCfg.Models[s.selectedProvider].Model
			for i, name := range models {
				if name == currentModel {
					sb.WriteString(fmt.Sprintf("  %d) %s ✅\n", i+1, name))
				} else {
					sb.WriteString(fmt.Sprintf("  %d) %s\n", i+1, name))
				}
			}
			responseText = sb.String()
			return CommandResult{Text: responseText, IsCommand: true}
		} else if len(parts) == 2 {
			switch parts[1] {
			case "next":
				// Cycle to next filtered model
				currentModel := s.ollamaCfg.Models[s.selectedProvider].Model
				var currentIdx int = -1
				for i, name := range models {
					if name == currentModel {
						currentIdx = i
						break
					}
				}
				nextIdx := (currentIdx + 1) % len(models)
				s.ollamaCfg.Models[s.selectedProvider].Model = models[nextIdx]
				s.botParams.CurrentModel = models[nextIdx]
				saveBotParams(s.botCfg.Name, s.botParams)
				responseText = fmt.Sprintf("Switched to model: %s", models[nextIdx])
			case "prev":
				// Cycle to previous filtered model
				currentModel := s.ollamaCfg.Models[s.selectedProvider].Model
				var currentIdx int = -1
				for i, name := range models {
					if name == currentModel {
						currentIdx = i
						break
					}
				}
				prevIdx := (currentIdx - 1 + len(models)) % len(models)
				s.ollamaCfg.Models[s.selectedProvider].Model = models[prevIdx]
				s.botParams.CurrentModel = models[prevIdx]
				saveBotParams(s.botCfg.Name, s.botParams)
				responseText = fmt.Sprintf("Switched to model: %s", models[prevIdx])
			default:
				// mf [n] - select a model by number
				num, err := strconv.Atoi(parts[1])
				if err != nil || num < 1 || num > len(models) {
					responseText = fmt.Sprintf("Invalid model number. Please choose 1-%d.", len(models))
				} else {
					s.ollamaCfg.Models[s.selectedProvider].Model = models[num-1]
					s.botParams.CurrentModel = models[num-1]
					saveBotParams(s.botCfg.Name, s.botParams)
					responseText = fmt.Sprintf("Selected model: %s", models[num-1])
				}
			}
			return CommandResult{Text: responseText, IsCommand: true}
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
					s.botParams.NumCtx = num * 1024
					saveBotParams(s.botCfg.Name, s.botParams)
					responseText = fmt.Sprintf("Context set to %dK (%d)", num, s.botParams.NumCtx)
				}
			} else {
				responseText = "Invalid format. Use a number ending in k (e.g. 8k, 16k, 32k)."
			}
		} else {
			ctxK := s.botParams.NumCtx / 1024
			responseText = fmt.Sprintf("Current context size: %dK (%d)\nUsage: .llmctx [nk] - set the model context window (e.g. 8k, 16k, 32k)", ctxK, s.botParams.NumCtx)
		}
		return CommandResult{Text: responseText, IsCommand: true}

	case "trace":
		if len(parts) == 2 {
			arg := strings.ToLower(parts[1])
			if arg == "on" {
				logToFileEnabled = true
				responseText = fmt.Sprintf("Trace enabled. LLM request payloads will be written to context/<s.bot>/ (max %d per s.bot).", maxTraceFilesPerBot)
			} else if arg == "off" {
				logToFileEnabled = false
				responseText = "Trace disabled. No more payloads written."
			} else {
				responseText = fmt.Sprintf("Current trace: %v\nUsage: trace [on/off]", logToFileEnabled)
			}
		} else {
			responseText = fmt.Sprintf("Current trace: %v\nUsage: trace [on/off]", logToFileEnabled)
		}
		return CommandResult{Text: responseText, IsCommand: true}

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
					if story == s.currentStory {
						sb.WriteString(fmt.Sprintf("  %d) %s ✅\n", i+1, displayName(story)))
					} else {
						sb.WriteString(fmt.Sprintf("  %d) %s\n", i+1, displayName(story)))
					}
				}
				responseText = sb.String()
			}
			return CommandResult{Text: responseText, IsCommand: true}
		} else if len(parts) >= 2 && parts[1] == "save" {
			name := commandArg(msg.Text, 2)
			if name == "" {
				name = "autosave"
			}
			path := getStorySnapshotPath(s.currentStory, s.botCfg.Name, name)
			if err := rewriteChatLogFile(path, s.conversationContext, s.currentMode); err != nil {
				responseText = fmt.Sprintf("Failed to save story snapshot: %v", err)
			} else {
				responseText = fmt.Sprintf("Saved story snapshot '%s' for %s.", name, displayName(s.currentStory))
			}
			return CommandResult{Text: responseText, IsCommand: true}
		} else if len(parts) >= 2 && parts[1] == "load" {
			name := commandArg(msg.Text, 2)
			if name == "" {
				name = "autosave"
			}
			path := getStorySnapshotPath(s.currentStory, s.botCfg.Name, name)
			if _, err := os.Stat(path); err != nil {
				responseText = fmt.Sprintf("No saved story snapshot named '%s' in %s.", name, displayName(s.currentStory))
			} else {
				loaded := loadChatHistory(path)
				s.conversationContext = loaded
				responseText = fmt.Sprintf("Loaded story snapshot '%s' for %s.", name, displayName(s.currentStory))
			}
			return CommandResult{Text: responseText, IsCommand: true}
		} else if len(parts) >= 2 && parts[1] == "create" {
			// story create [name] - create a new story folder and switch to it
			name := normalizeStoryName(commandArg(msg.Text, 2))
			if name == "" {
				responseText = "Usage: story create [name] - create a new story folder and switch to it"
			} else if existed, err := createStoryFolders(name); err != nil {
				responseText = fmt.Sprintf("Failed to create story '%s': %v", name, err)
			} else {
				loaded := s.selectStory(name)
				if existed {
					responseText = fmt.Sprintf("Story '%s' already exists — switched to it (%d messages loaded).", displayName(name), loaded)
				} else {
					responseText = fmt.Sprintf("Created story '%s' and switched to it.", displayName(name))
				}
			}
			return CommandResult{Text: responseText, IsCommand: true}
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
					loaded := s.selectStory(stories[num-1])
					responseText = fmt.Sprintf("Switched to story: %s (%d messages loaded)", displayName(s.currentStory), loaded)
				}
			}
			return CommandResult{Text: responseText, IsCommand: true}
		} else {
			responseText = "Usage: story [n] | story create [name] | story save [name] | story load [name]"
		}

	case "chapter", "ch":
		if len(parts) == 1 {
			chapters, err := listStorySnapshots(s.currentStory, s.botCfg.Name)
			if err != nil {
				responseText = fmt.Sprintf("Error listing story chapters: %v", err)
			} else if len(chapters) == 0 {
				responseText = fmt.Sprintf("No saved chapters in %s yet.", displayName(s.currentStory))
			} else {
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("Saved chapters in '%s':\n", displayName(s.currentStory)))
				for i, name := range chapters {
					sb.WriteString(fmt.Sprintf("  %d) %s\n", i+1, displayName(name)))
				}
				responseText = sb.String()
			}
			return CommandResult{Text: responseText, IsCommand: true}
		}
		if len(parts) >= 2 && parts[1] == "save" {
			name := commandArg(msg.Text, 2)
			if name == "" {
				name = "autosave"
			}
			path := getStorySnapshotPath(s.currentStory, s.botCfg.Name, name)
			if err := rewriteChatLogFile(path, s.conversationContext, s.currentMode); err != nil {
				responseText = fmt.Sprintf("Failed to save chapter '%s': %v", name, err)
			} else {
				responseText = fmt.Sprintf("Saved chapter '%s' in %s.", name, displayName(s.currentStory))
			}
			return CommandResult{Text: responseText, IsCommand: true}
		}
		if len(parts) >= 2 && parts[1] == "load" {
			arg := commandArg(msg.Text, 2)
			if num, err := strconv.Atoi(arg); err == nil {
				loaded, err := loadChapterSnapshotByIndex(s.currentStory, s.botCfg.Name, num)
				if err != nil {
					responseText = err.Error()
				} else {
					s.conversationContext = loaded
					responseText = fmt.Sprintf("Loaded chapter #%d from %s.", num, displayName(s.currentStory))
				}
				return CommandResult{Text: responseText, IsCommand: true}
			}
			name := arg
			if name == "" {
				name = "autosave"
			}
			path := getStorySnapshotPath(s.currentStory, s.botCfg.Name, name)
			if _, err := os.Stat(path); err != nil {
				responseText = fmt.Sprintf("No saved chapter named '%s' in %s.", name, displayName(s.currentStory))
			} else {
				loaded := loadChatHistory(path)
				s.conversationContext = loaded
				responseText = fmt.Sprintf("Loaded chapter '%s' from %s.", name, displayName(s.currentStory))
			}
			return CommandResult{Text: responseText, IsCommand: true}
		}
		if len(parts) == 2 {
			num, err := strconv.Atoi(parts[1])
			if err != nil {
				responseText = "Usage: chapter [list] | chapter [n] | chapter save [name] | chapter load [number|name]"
			} else {
				loaded, err := loadChapterSnapshotByIndex(s.currentStory, s.botCfg.Name, num)
				if err != nil {
					responseText = err.Error()
				} else {
					s.conversationContext = loaded
					responseText = fmt.Sprintf("Loaded chapter #%d from %s.", num, displayName(s.currentStory))
				}
			}
			return CommandResult{Text: responseText, IsCommand: true}
		}
		responseText = "Usage: chapter [list] | chapter [n] | chapter save [name] | chapter load [number|name]"
		return CommandResult{Text: responseText, IsCommand: true}

	case "scene", "sc":
		if len(parts) == 1 {
			// List scene files in current story
			scenes, err := listSceneFiles(s.currentStory)
			if err != nil {
				responseText = fmt.Sprintf("Error listing scenes: %v", err)
			} else {
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("Scenes in '%s':\n", displayName(s.currentStory)))
				for i, sceneName := range scenes {
					sceneDisp := displayName(sceneName)
					if s.activeScenes[i+1] {
						sb.WriteString(fmt.Sprintf("  %d) %s ✅\n", i+1, sceneDisp))
					} else {
						sb.WriteString(fmt.Sprintf("  %d) %s\n", i+1, sceneDisp))
					}
				}
				responseText = sb.String()
			}
			return CommandResult{Text: responseText, IsCommand: true}
		} else if len(parts) >= 2 && parts[1] == "all" && len(parts) == 3 && parts[2] == "off" {
			// scene all off
			s.activeScenes = make(map[int]bool)
			responseText = "All scenes deactivated."
			return CommandResult{Text: responseText, IsCommand: true}
		} else if len(parts) >= 2 && parts[1] == "edit" {
			// scene edit [n] - show scene content
			if len(parts) == 3 {
				num, err := strconv.Atoi(parts[2])
				if err != nil {
					responseText = "Invalid scene number."
				} else {
					scenes, err := listSceneFiles(s.currentStory)
					if err != nil {
						responseText = fmt.Sprintf("Error listing scenes: %v", err)
					} else if num < 1 || num > len(scenes) {
						responseText = fmt.Sprintf("Invalid scene number. Please choose 1-%d.", len(scenes))
					} else {
						content := loadStoryContent(s.currentStory, "scenes", scenes[num-1])
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
			if len(msg.Text) <= contentStart {
				responseText = "Usage: scene save [name] [text] - provide scene content after the name"
			} else {
				content := msg.Text[contentStart:]
				path := fmt.Sprintf("stories/%s/scenes/%s", s.currentStory, sceneName)
				if err := os.WriteFile(path, []byte(content), 0644); err != nil {
					responseText = fmt.Sprintf("Failed to save scene: %v", err)
				} else {
					responseText = fmt.Sprintf("Scene '%s' saved.", sceneName)
				}
			}
		} else {
			// Multi-toggle: scene 1 -2 3
			scenes, err := listSceneFiles(s.currentStory)
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
							delete(s.activeScenes, idx)
							sceneName := scenes[idx-1]
							toggled = append(toggled, fmt.Sprintf("%s off", sceneName))
						}
					} else {
						// Activate
						if num >= 1 && num <= len(scenes) {
							s.activeScenes[num] = true
							sceneName := scenes[num-1]
							toggled = append(toggled, fmt.Sprintf("%s on", sceneName))
						}
					}
				}
				if len(toggled) > 0 {
					// Persist the changes
					var newActiveScenes []int
					for idx := range s.activeScenes {
						newActiveScenes = append(newActiveScenes, idx)
					}
					s.botParams.ActiveScenes = newActiveScenes
					saveBotParams(s.botCfg.Name, s.botParams)
					responseText = fmt.Sprintf("Scenes toggled:\n%s", strings.Join(toggled, "\n"))
				} else {
					responseText = "No valid scene numbers provided."
				}
			}
		}

	case "char", "c":
		if len(parts) == 1 {
			// List character files in current story
			chars, err := listCharacterFiles(s.currentStory)
			if err != nil {
				responseText = fmt.Sprintf("Error listing characters: %v", err)
			} else {
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("Characters in '%s':\n", displayName(s.currentStory)))
				for i, c := range chars {
					charDisp := displayName(c)
					if s.activeCharacters[i+1] {
						sb.WriteString(fmt.Sprintf("  %d) %s ✅\n", i+1, charDisp))
					} else {
						sb.WriteString(fmt.Sprintf("  %d) %s\n", i+1, charDisp))
					}
				}
				responseText = sb.String()
			}
			return CommandResult{Text: responseText, IsCommand: true}
		} else if len(parts) >= 2 && parts[1] == "all" && len(parts) == 3 && parts[2] == "off" {
			// char all off
			s.activeCharacters = make(map[int]bool)
			responseText = "All characters deactivated."
			return CommandResult{Text: responseText, IsCommand: true}
		} else if len(parts) >= 2 && parts[1] == "edit" {
			// char edit [n] - show character content
			if len(parts) == 3 {
				num, err := strconv.Atoi(parts[2])
				if err != nil {
					responseText = "Invalid character number."
				} else {
					chars, err := listCharacterFiles(s.currentStory)
					if err != nil {
						responseText = fmt.Sprintf("Error listing characters: %v", err)
					} else if num < 1 || num > len(chars) {
						responseText = fmt.Sprintf("Invalid character number. Please choose 1-%d.", len(chars))
					} else {
						content := loadStoryContent(s.currentStory, "characters", chars[num-1])
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
			if len(msg.Text) <= contentStart {
				responseText = "Usage: char save [name] [text] - provide character content after the name"
			} else {
				content := msg.Text[contentStart:]
				path := fmt.Sprintf("stories/%s/characters/%s", s.currentStory, charName)
				if err := os.WriteFile(path, []byte(content), 0644); err != nil {
					responseText = fmt.Sprintf("Failed to save character: %v", err)
				} else {
					responseText = fmt.Sprintf("Character '%s' saved.", charName)
				}
			}
		} else {
			// Multi-toggle: char 1 -2 3
			chars, err := listCharacterFiles(s.currentStory)
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
							delete(s.activeCharacters, idx)
							charName := strings.TrimSuffix(chars[idx-1], ".txt")
							toggled = append(toggled, fmt.Sprintf("%s off", charName))
						}
					} else {
						// Activate
						if num >= 1 && num <= len(chars) {
							s.activeCharacters[num] = true
							charName := strings.TrimSuffix(chars[num-1], ".txt")
							toggled = append(toggled, fmt.Sprintf("%s on", charName))
						}
					}
				}
				if len(toggled) > 0 {
					// Persist the changes
					var newActiveChars []int
					for idx := range s.activeCharacters {
						newActiveChars = append(newActiveChars, idx)
					}
					s.botParams.ActiveCharacters = newActiveChars
					saveBotParams(s.botCfg.Name, s.botParams)
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
			return CommandResult{Text: responseText, IsCommand: true}
		}

		// Extract the question text (everything after ".ask")
		question := strings.TrimSpace(strings.TrimPrefix(msg.Text, parts[0]))
		if question == "" {
			responseText = "Usage: ask [question] - ask a question about the conversation"
			return CommandResult{Text: responseText, IsCommand: true}
		}

		// Send typing indicator
		s.sendTyping(msg)

		// Use neutral system prompt instead of the current role
		systemPrompt := "You are a helpful assistant. Answer any questions based on the provided data."

		// Build injected context from active scenes and characters
		var injectedParts []string

		// Add active scenes
		scenes, _ := listSceneFiles(s.currentStory)
		var activeSceneLines []string
		for idx := range s.activeScenes {
			if idx >= 1 && idx <= len(scenes) {
				content := loadStoryContent(s.currentStory, "scenes", scenes[idx-1])
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
		chars, _ := listCharacterFiles(s.currentStory)
		var activeCharLines []string
		for idx := range s.activeCharacters {
			if idx >= 1 && idx <= len(chars) {
				content := loadStoryContent(s.currentStory, "characters", chars[idx-1])
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
		if s.currentMode == "chat" {
			// Chat mode: include all past user + assistant messages
			apiContext = s.conversationContext
		} else {
			// Story mode: include only past assistant messages
			for _, msg := range s.conversationContext {
				if msg.Role == "assistant" {
					apiContext = append(apiContext, msg)
				}
			}
		}

		// Call the LLM backend (Ollama or Google depending on provider type)
		modelEntry := s.ollamaCfg.Models[s.selectedProvider]
		askResponse, err := chatWithEntry(s.botCfg.Name, modelEntry, systemPrompt, apiContext, enrichedMessage, s.botParams.NumCtx, s.botParams.NoThink)
		if err != nil {
			log.Printf("[%s] Ollama API error (ask): %v", s.botCfg.Name, err)
			askResponse = fmt.Sprintf("Sorry, I encountered an error: %v", err)
		}

		// Do NOT store the ask/response in s.conversationContext

		// Plain, not IsCommand: the answer is LLM content the user explicitly
		// asked for, not a status message. Plain routes it to the Telegram
		// adapter's plain-text branch, which skips MarkdownV2 parsing and the
		// auto-delete timer.
		return CommandResult{Text: askResponse, Plain: true}

	case "resend":
		// Resend the last user message to the AI
		if s.lastUserText == "" {
			responseText = "No previous message to resend."
			return CommandResult{Text: responseText, IsCommand: true}
		}

		// What follows is an AI chat reply, not a system message: clear the
		// parse-time command flag so the Telegram adapter renders it as a chat
		// message instead of scheduling it for auto-deletion.
		isCommand = false

		log.Printf("[%s] Resending last user message: %s", s.botCfg.Name, s.lastUserText)

		// Send a typing indicator while we process
		s.sendTyping(msg)

		// Use the selected model entry for responses
		modelEntry := s.ollamaCfg.Models[s.selectedProvider]

		// Load the current role content as the system prompt
		systemPrompt := loadRoleContent(s.botParams.CurrentRole)

		// Build injected context from active scenes and characters
		var injectedParts []string

		// Add active scenes
		scenes, _ := listSceneFiles(s.currentStory)
		var activeSceneLines []string
		for idx := range s.activeScenes {
			if idx >= 1 && idx <= len(scenes) {
				content := loadStoryContent(s.currentStory, "scenes", scenes[idx-1])
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
		chars, _ := listCharacterFiles(s.currentStory)
		var activeCharLines []string
		for idx := range s.activeCharacters {
			if idx >= 1 && idx <= len(chars) {
				content := loadStoryContent(s.currentStory, "characters", chars[idx-1])
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
		enrichedMessage := buildPrompt(injectedParts, s.lastUserText, defaultExecutionDirective())

		// Do NOT re-store the user message in context - it was already stored the first time

		// Build context for API based on mode
		var apiContext []ContextMessage
		if s.currentMode == "chat" {
			// Chat mode: include all past user + assistant messages
			apiContext = s.conversationContext
		} else {
			// Story mode: include only past assistant messages
			for _, msg := range s.conversationContext {
				if msg.Role == "assistant" {
					apiContext = append(apiContext, msg)
				}
			}
		}

		// Call the LLM backend for a response with context
		var err error
		responseText, err = chatWithEntry(s.botCfg.Name, modelEntry, systemPrompt, apiContext, enrichedMessage, s.botParams.NumCtx, s.botParams.NoThink)
		if err != nil {
			log.Printf("[%s] Ollama API error (resend): %v", s.botCfg.Name, err)
			responseText = fmt.Sprintf("Sorry, I encountered an error contacting the AI: %v\n\nUse `resend` to resend your last message.", err)
		}

		// Store assistant response in context (both modes)
		s.conversationContext = append(s.conversationContext, ContextMessage{Role: "assistant", Content: responseText})

		// Append assistant response to chat log (both modes)
		chatLogPath := getChatLogPath(s.currentStory, s.botCfg.Name)
		appendToChatLog(chatLogPath, "assistant", responseText)

		// Trim context if over limit
		if len(s.conversationContext) > s.contextLimit {
			s.conversationContext = s.conversationContext[len(s.conversationContext)-s.contextLimit:]
		}

		// Flag the context-limit warning so it is sent after the reply
		contextWarn = true
		spoken = true

	default:
		// If the message is a single word that is not a recognized command and
		// not "continue", send a disappearing "[msg] ignored" response to
		// prevent misspelled commands from reaching the LLM.
		//
		// isCommand gates this: on the web transport ordinary single-word chat
		// ("hello") is not a command attempt and must reach the LLM, whereas on
		// Telegram a bare unknown word has always been treated as a typo.
		if isCommand && len(parts) == 1 && command != "continue" {
			ignoredText := fmt.Sprintf("%s ignored", parts[0])
			return CommandResult{Text: ignoredText, IsCommand: true}
		}

		// Not a command - send to the AI using the selected provider.
		// Clear the parse-time command flag first: parseCommand marks every
		// Telegram message as a command attempt (so bare-word commands work
		// and the single-word typo guard above fires), but the LLM reply built
		// here is a chat message. Leaving the flag set would make the Telegram
		// adapter auto-delete every chat reply after deleteAfterDuration.
		isCommand = false
		// Save the user text for potential resend
		s.lastUserText = msg.Text

		// Send a typing indicator while we process
		s.sendTyping(msg)

		// Use the selected model entry for responses
		modelEntry := s.ollamaCfg.Models[s.selectedProvider]

		// Load the current role content as the system prompt
		systemPrompt := loadRoleContent(s.botParams.CurrentRole)

		// Build injected context from active scenes and characters
		var injectedParts []string

		// Add active scenes
		scenes, _ := listSceneFiles(s.currentStory)
		var activeSceneLines []string
		for idx := range s.activeScenes {
			if idx >= 1 && idx <= len(scenes) {
				content := loadStoryContent(s.currentStory, "scenes", scenes[idx-1])
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
		chars, _ := listCharacterFiles(s.currentStory)
		var activeCharLines []string
		for idx := range s.activeCharacters {
			if idx >= 1 && idx <= len(chars) {
				content := loadStoryContent(s.currentStory, "characters", chars[idx-1])
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
		enrichedMessage := buildPrompt(injectedParts, msg.Text, defaultExecutionDirective())

		// Store user message in context (chat mode only)
		if s.currentMode == "chat" {
			s.conversationContext = append(s.conversationContext, ContextMessage{Role: "user", Content: msg.Text})
			// Append user message to chat log
			chatLogPath := getChatLogPath(s.currentStory, s.botCfg.Name)
			appendToChatLog(chatLogPath, "user", msg.Text)
		}

		// Build context for API based on mode
		var apiContext []ContextMessage
		if s.currentMode == "chat" {
			// Chat mode: include all past user + assistant messages
			apiContext = s.conversationContext
		} else {
			// Story mode: include only past assistant messages
			for _, msg := range s.conversationContext {
				if msg.Role == "assistant" {
					apiContext = append(apiContext, msg)
				}
			}
		}

		// Call the LLM backend for a response with context
		var err error
		responseText, err = chatWithEntry(s.botCfg.Name, modelEntry, systemPrompt, apiContext, enrichedMessage, s.botParams.NumCtx, s.botParams.NoThink)
		if err != nil {
			log.Printf("[%s] Ollama API error: %v", s.botCfg.Name, err)
			responseText = fmt.Sprintf("Sorry, I encountered an error contacting the AI: %v\n\nUse `resend` to resend your last message.", err)
		}

		// Store assistant response in context (both modes)
		s.conversationContext = append(s.conversationContext, ContextMessage{Role: "assistant", Content: responseText})

		// Append assistant response to chat log (both modes)
		chatLogPath := getChatLogPath(s.currentStory, s.botCfg.Name)
		appendToChatLog(chatLogPath, "assistant", responseText)

		// Mirror Telegram replies to open web pages (shared session).
		hub.publish(hubMessage{Bot: s.botCfg.Name, Role: "assistant", Content: responseText, Source: string(msg.Source)})

		// Trim context if over limit
		if len(s.conversationContext) > s.contextLimit {
			s.conversationContext = s.conversationContext[len(s.conversationContext)-s.contextLimit:]
		}

		// Flag the context-limit warning so it is sent after the reply
		contextWarn = true
		spoken = true
	}

	// "resend" and the default (AI chat) case fall through to here. Return the
	// reply plus the post-reply side-effect flags for the transport adapter.
	return CommandResult{
		Text:        responseText,
		Spoken:      spoken,
		ContextWarn: contextWarn,
		IsCommand:   isCommand,
	}
}
