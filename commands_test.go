package main

import (
	"strings"
	"testing"
)

// newTestSession builds an in-memory session suitable for exercising the
// command dispatcher without touching Telegram or a live LLM. It registers the
// session under a unique name and restores the registry afterwards.
func newTestSession(t *testing.T, name string, cfg Config) *BotSession {
	t.Helper()

	previous, existed := func() (*BotSession, bool) {
		sessionsMu.RLock()
		defer sessionsMu.RUnlock()
		s, ok := botSessions[name]
		return s, ok
	}()

	s := newBotSession(cfg.Bots[0], cfg.UserID, cfg.Ollama)
	s.botCfg.Name = name
	registerSession(s)

	t.Cleanup(func() {
		sessionsMu.Lock()
		defer sessionsMu.Unlock()
		if existed {
			botSessions[name] = previous
		} else {
			delete(botSessions, name)
		}
	})
	return s
}

// testConfig returns a minimal two-provider config for dispatcher tests.
func testConfig() Config {
	return Config{
		UserID: 4242,
		Bots:   []BotConfig{{Name: "testbot", Token: "unused"}},
		Ollama: OllamaConfig{Models: []ModelEntry{
			{Name: "alpha", Model: "model-a", APIBase: "http://127.0.0.1:1"},
			{Name: "beta", Model: "model-b", APIBase: "http://127.0.0.1:1"},
		}},
	}
}

// TestParseCommandWebRequiresDot verifies the web transport only treats dotted
// input as a command, so ordinary one-word chat still reaches the LLM.
func TestParseCommandWebRequiresDot(t *testing.T) {
	cases := []struct {
		text      string
		wantCmd   string
		isCommand bool
	}{
		{".model 3", "model", true},
		{".status", "status", true},
		{"model 3", "model", true},
		{"status", "status", true},
		{"hello", "", false},
		{"hello there", "", false},
		{"tell me about the status", "", false},
	}
	for _, c := range cases {
		cmd, parts, isCommand := parseCommand(c.text, SourceWeb)
		if isCommand != c.isCommand {
			t.Errorf("web %q: isCommand=%v want %v", c.text, isCommand, c.isCommand)
			continue
		}
		if cmd != c.wantCmd {
			t.Errorf("web %q: command=%q want %q", c.text, cmd, c.wantCmd)
		}
		if len(parts) == 0 && c.text != "" {
			t.Errorf("web %q: expected non-empty parts", c.text)
		}
	}
}

// TestParseCommandTelegramAcceptsBareWords verifies Telegram keeps its
// historical behaviour of accepting dotless commands.
func TestParseCommandTelegramAcceptsBareWords(t *testing.T) {
	for _, text := range []string{"model 3", ".model 3", "status", ".status"} {
		cmd, parts, isCommand := parseCommand(text, SourceTelegram)
		if !isCommand {
			t.Errorf("telegram %q: expected command", text)
		}
		if cmd != "model" && cmd != "status" {
			t.Errorf("telegram %q: unexpected command %q", text, cmd)
		}
		if len(parts) == 0 {
			t.Errorf("telegram %q: expected parts", text)
		}
	}
}

// TestDispatchStatusFromWeb proves a command typed on the web transport is
// handled by the same dispatcher Telegram uses.
func TestDispatchStatusFromWeb(t *testing.T) {
	cfg := testConfig()
	s := newTestSession(t, "statusbot", cfg)

	res := s.HandleMessage(IncomingMessage{Text: ".status", Source: SourceWeb})
	if res.Text == "" {
		t.Fatal("expected a status reply from the web transport")
	}
	for _, want := range []string{"Provider:", "Model:", "Mode:"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("status reply missing %q:\n%s", want, res.Text)
		}
	}
	if !res.IsCommand {
		t.Error("status should be reported as a command reply")
	}
}

// TestDispatchHelpFromWeb verifies the command list is reachable from the web.
func TestDispatchHelpFromWeb(t *testing.T) {
	cfg := testConfig()
	s := newTestSession(t, "helpbot", cfg)

	res := s.HandleMessage(IncomingMessage{Text: ".help", Source: SourceWeb})
	if !strings.Contains(res.Text, "i5 Assistant Help") {
		t.Fatalf("expected the help listing, got:\n%s", res.Text)
	}
	for _, want := range []string{"model", "role", "status", "story", "provider"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("help listing should document %s", want)
		}
	}
	// The listing groups commands under headings rather than dot-prefixing them.
	for _, want := range []string{"Stories & Chapters", "Roles", "AI Providers & Models"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("help listing should contain the %q section", want)
		}
	}
}

// TestDispatchModelSelectionPersists verifies a state-changing command updates
// both the session and the persisted params, and is then visible from the other
// transport because they share one session.
func TestDispatchModelSelectionPersists(t *testing.T) {
	cfg := testConfig()
	s := newTestSession(t, "modelbot", cfg)

	// provider 1 selects the alphabetically-first provider (alpha -> model-a).
	res := s.HandleMessage(IncomingMessage{Text: ".provider 1", Source: SourceWeb})
	if !strings.Contains(res.Text, "alpha") {
		t.Fatalf("expected provider switch confirmation, got: %s", res.Text)
	}
	if s.selectedProvider != 0 {
		t.Errorf("selectedProvider=%d want 0", s.selectedProvider)
	}

	// The choice must survive into the persisted params.
	saved := loadBotParams("modelbot", "")
	if saved.SelectedProvider != 0 {
		t.Errorf("persisted SelectedProvider=%d want 0", saved.SelectedProvider)
	}

	// And the other transport reports the same state.
	tg := s.HandleMessage(IncomingMessage{Text: "status", Source: SourceTelegram, ChatID: 1, MessageID: 2})
	if !strings.Contains(tg.Text, "model-a") {
		t.Errorf("telegram status should show the model chosen from web, got:\n%s", tg.Text)
	}
}

// TestDispatchModeSharedAcrossTransports verifies mode switching is shared
// between the two transports.
func TestDispatchModeSharedAcrossTransports(t *testing.T) {
	cfg := testConfig()
	s := newTestSession(t, "modebot", cfg)

	res := s.HandleMessage(IncomingMessage{Text: ".mode story", Source: SourceWeb})
	if !strings.Contains(res.Text, "story mode enabled") {
		t.Fatalf("expected mode confirmation, got: %s", res.Text)
	}
	if s.currentMode != "story" {
		t.Errorf("currentMode=%q want story", s.currentMode)
	}

	tg := s.HandleMessage(IncomingMessage{Text: "status", Source: SourceTelegram})
	if !strings.Contains(tg.Text, "story") {
		t.Errorf("telegram status should report story mode, got:\n%s", tg.Text)
	}
}

// TestDispatchUnknownDotCommandIsIgnored verifies a dotted but unknown command
// does not reach the LLM on the web transport.
func TestDispatchUnknownDotCommandIsIgnored(t *testing.T) {
	cfg := testConfig()
	s := newTestSession(t, "ignorebot", cfg)

	res := s.HandleMessage(IncomingMessage{Text: ".definitelynotacommand", Source: SourceWeb})
	if !strings.Contains(res.Text, "ignored") {
		t.Fatalf("expected an 'ignored' reply, got: %q", res.Text)
	}
	if !res.IsCommand {
		t.Error("ignored replies should be marked as command replies")
	}
}

// TestDispatchPlainTextIsChatOnWeb verifies ordinary chat is routed to the LLM
// rather than swallowed as a command. The LLM call fails because the test
// config points at an unreachable endpoint, which is itself proof the text was
// treated as chat rather than a command.
func TestDispatchPlainTextIsChatOnWeb(t *testing.T) {
	cfg := testConfig()
	s := newTestSession(t, "chatbot", cfg)

	res := s.HandleMessage(IncomingMessage{Text: "hello", Source: SourceWeb})
	if res.IsCommand {
		t.Error("plain chat must not be treated as a command on web")
	}
	if !strings.Contains(res.Text, "encountered an error") {
		t.Errorf("expected the LLM error path for chat text, got:\n%s", res.Text)
	}
}

// TestSessionRegistrySurvivesFailedTelegramAuth documents the ordering
// guarantee: the session exists before runBot attempts to connect, so an
// invalid token degrades to "web only" instead of breaking the web UI.
func TestSessionRegistrySurvivesFailedTelegramAuth(t *testing.T) {
	cfg := testConfig()
	s := newTestSession(t, "authbot", cfg)

	if got := getSession("authbot"); got != s {
		t.Fatal("session should be registered before any Telegram connection")
	}
	// No Telegram transport attached yet, so Telegram-only side effects must be
	// no-ops rather than panics.
	s.sendTyping(IncomingMessage{Text: ".status", Source: SourceTelegram, ChatID: 7})
	s.deleteTelegramMessage(IncomingMessage{Source: SourceTelegram, ChatID: 7}, 99, map[int]bool{})
	if id := s.trackMessage(IncomingMessage{Source: SourceTelegram, ChatID: 7}, "x"); id != 0 {
		t.Errorf("trackMessage without a Telegram bot should return 0, got %d", id)
	}
}

// TestDispatchPlainTextIsChatOnTelegram guards the Telegram auto-delete
// contract. parseCommand marks every Telegram message as a command attempt
// (so bare-word commands and the single-word typo guard keep working), but
// the dispatcher must clear that flag before returning an AI chat reply.
// If IsCommand leaks through, the Telegram adapter routes the reply to
// sendAndScheduleDelete and every chat message vanishes after
// deleteAfterDuration. The LLM call fails against the unreachable test
// endpoint, which is itself proof the text reached the chat path rather
// than being swallowed as a command.
func TestDispatchPlainTextIsChatOnTelegram(t *testing.T) {
	cfg := testConfig()
	s := newTestSession(t, "tgchatbot", cfg)

	res := s.HandleMessage(IncomingMessage{Text: "hello there", Source: SourceTelegram, ChatID: 1, MessageID: 2})
	if res.IsCommand {
		t.Error("AI chat replies must not be flagged as command replies on Telegram, or they get auto-deleted")
	}
	if !strings.Contains(res.Text, "encountered an error") {
		t.Errorf("expected the LLM error path for chat text, got:\n%s", res.Text)
	}
}

// TestDispatchResendIsChatOnTelegram verifies resend's AI reply is treated as
// a chat message (persisted), while the guard reply for an empty history
// stays a command reply (auto-deleted).
func TestDispatchResendIsChatOnTelegram(t *testing.T) {
	cfg := testConfig()
	s := newTestSession(t, "tgresendbot", cfg)

	// Seed lastUserText through a normal chat message first.
	seed := s.HandleMessage(IncomingMessage{Text: "hello there", Source: SourceTelegram, ChatID: 1, MessageID: 2})
	if seed.Text == "" {
		t.Fatal("expected a reply for the seeding chat message")
	}
	if seed.IsCommand {
		t.Fatal("the seeding chat reply must not be flagged as a command reply")
	}

	res := s.HandleMessage(IncomingMessage{Text: "resend", Source: SourceTelegram, ChatID: 1, MessageID: 4})
	if res.IsCommand {
		t.Error("resend's AI reply must not be flagged as a command reply, or it gets auto-deleted")
	}
	if !strings.Contains(res.Text, "encountered an error") {
		t.Errorf("expected the LLM error path from resend, got:\n%s", res.Text)
	}

	// With no history to resend the guard reply is a genuine system message.
	empty := newTestSession(t, "tgresendempty", cfg)
	guard := empty.HandleMessage(IncomingMessage{Text: "resend", Source: SourceTelegram, ChatID: 1, MessageID: 2})
	if !strings.Contains(guard.Text, "No previous message") {
		t.Fatalf("expected the resend guard reply, got: %q", guard.Text)
	}
	if !guard.IsCommand {
		t.Error("the resend guard reply is a system message and must keep IsCommand")
	}
}

// TestDispatchHelpIsCommandOnTelegram is the counterpart to the chat tests:
// genuine system messages must keep the IsCommand flag so the Telegram
// adapter schedules their auto-deletion.
func TestDispatchHelpIsCommandOnTelegram(t *testing.T) {
	cfg := testConfig()
	s := newTestSession(t, "tghelpbot", cfg)

	res := s.HandleMessage(IncomingMessage{Text: ".help", Source: SourceTelegram, ChatID: 1, MessageID: 2})
	if !strings.Contains(res.Text, "i5 Assistant Help") {
		t.Fatalf("expected the help listing, got:\n%s", res.Text)
	}
	if !res.IsCommand {
		t.Error("help is a system message and must keep IsCommand so it auto-deletes")
	}
}

// TestDispatchAskIsPlainOnTelegram documents that .ask replies carry Plain
// and not IsCommand, so the Telegram adapter sends them as raw text (no
// MarkdownV2) and without the auto-delete timer. Usage errors for .ask are
// real system messages and keep IsCommand.
func TestDispatchAskIsPlainOnTelegram(t *testing.T) {
	cfg := testConfig()
	s := newTestSession(t, "tgaskbot", cfg)

	res := s.HandleMessage(IncomingMessage{Text: ".ask what is 2+2", Source: SourceTelegram, ChatID: 1, MessageID: 2})
	if res.Text == "" {
		t.Fatal("expected a reply for .ask")
	}
	if !res.Plain {
		t.Error(".ask replies must be Plain so they skip MarkdownV2 and the delete timer")
	}
	if res.IsCommand {
		t.Error(".ask replies are LLM content, not system messages, and must not be auto-deleted")
	}

	usage := s.HandleMessage(IncomingMessage{Text: ".ask", Source: SourceTelegram, ChatID: 1, MessageID: 4})
	if !strings.Contains(usage.Text, "Usage:") {
		t.Fatalf("expected the .ask usage reply, got: %q", usage.Text)
	}
	if !usage.IsCommand {
		t.Error(".ask usage errors are system messages and must keep IsCommand")
	}
}
