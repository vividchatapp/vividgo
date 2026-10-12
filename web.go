package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"gopkg.in/yaml.v3"
)

// ── Shared web state ────────────────────────────────────────────────
// Everything lives on the backend; the page is a thin view.

var (
	webCfgMu sync.RWMutex
	webCfg   *Config

	// telegramSenders lets web chat forward replies to Telegram (shared session).
	telegramSendersMu sync.RWMutex
	telegramSenders   = map[string]*tgbotapi.BotAPI{}

	// hub broadcasts assistant replies to open web chat pages (SSE).
	hub = newChatHub()
)

// setWebConfig stores the active config for web handlers.
func setWebConfig(cfg *Config) {
	webCfgMu.Lock()
	defer webCfgMu.Unlock()
	webCfg = cfg
}

func getWebConfig() *Config {
	webCfgMu.RLock()
	defer webCfgMu.RUnlock()
	return webCfg
}

func registerTelegramSender(botName string, api *tgbotapi.BotAPI) {
	telegramSendersMu.Lock()
	defer telegramSendersMu.Unlock()
	telegramSenders[botName] = api
}

func getTelegramSender(botName string) *tgbotapi.BotAPI {
	telegramSendersMu.RLock()
	defer telegramSendersMu.RUnlock()
	return telegramSenders[botName]
}

// ── SSE hub ─────────────────────────────────────────────────────────

type hubMessage struct {
	Bot     string `json:"bot"`
	Role    string `json:"role"`
	Content string `json:"content"`
	Source  string `json:"source"`
	// Audio is a base64 data URL of spoken audio for this reply (voice mode).
	// Empty for ordinary text replies. The chat page plays it automatically.
	Audio string `json:"audio,omitempty"`
}

type chatHub struct {
	mu   sync.Mutex
	subs map[chan hubMessage]struct{}
}

func newChatHub() *chatHub { return &chatHub{subs: map[chan hubMessage]struct{}{}} }

func (h *chatHub) publish(m hubMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- m:
		default:
		}
	}
}

func (h *chatHub) subscribe() (chan hubMessage, func()) {
	ch := make(chan hubMessage, 32)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// voiceDataURL wraps raw audio bytes in a base64 data URL the browser can play
// directly in an <audio> element, using the correct MIME type for the codec.
func voiceDataURL(audio []byte) string {
	if len(audio) == 0 {
		return ""
	}
	mime := "audio/mpeg"
	if detectAudioExtension(audio) == ".wav" {
		mime = "audio/wav"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(audio)
}

// publishVoiceAudio broadcasts spoken audio to open web chat pages so the
// browser plays the same voice the Telegram transport received. Audio-only
// hub messages carry no text, so the page just triggers playback.
func publishVoiceAudio(botName string, audio []byte) {
	url := voiceDataURL(audio)
	if url == "" {
		return
	}
	hub.publish(hubMessage{Bot: botName, Role: "assistant", Audio: url, Source: "telegram"})
}

// ── Shared chat service (web + telegram use the same history) ───────

type chatRequest struct {
	Bot     string `json:"bot"`
	Message string `json:"message"`
}

// chatViaBackend runs one user message through the bot's command dispatcher,
// using the same session the Telegram transport uses, and mirrors the reply to
// the other transport (shared session semantics).
//
// Commands typed on the web page (`.model 3`, `.role 2`, `.status`, ...) are
// handled by exactly the same code path as Telegram, so both transports stay
// in sync. Ordinary text reaches the LLM with the same role, story, scene and
// character context, and the same chat/story history handling.
//
// It returns the reply text and, for spoken AI replies with voice enabled, a
// base64 audio data URL so the web page can play the same voice Telegram gets.
func chatViaBackend(botName, userText, source string) (string, string, error) {
	session := getSession(botName)
	if session == nil {
		return "", "", fmt.Errorf("unknown bot %q", botName)
	}

	result := session.HandleMessage(IncomingMessage{Text: userText, Source: MessageSource(source)})
	if result.Text == "" {
		return "", "", fmt.Errorf("no response")
	}

	// Generate voice audio for spoken AI replies so the web page plays it,
	// mirroring the Telegram adapter. Commands and plain (.ask) replies are
	// never spoken. We keep the raw bytes so a web-originated reply can also be
	// mirrored to Telegram as a voice note (same audio, two transports).
	audio := ""
	var audioBytes []byte
	if result.Spoken && !result.Plain {
		if b, verr := session.generateVoice(result.Text); verr != nil {
			log.Printf("[web->voice %s] generation failed: %v", botName, verr)
		} else {
			audioBytes = b
			audio = voiceDataURL(audioBytes)
		}
	}

	// Mirror web-originated replies to open Telegram/web pages (shared session).
	hub.publish(hubMessage{Bot: botName, Role: "assistant", Content: result.Text, Source: source, Audio: audio})
	if source == "web" {
		if sender := getTelegramSender(botName); sender != nil {
			cfg := getWebConfig()
			if cfg != nil {
				tgMsg := tgbotapi.NewMessage(cfg.UserID, result.Text)
				if _, terr := sender.Send(tgMsg); terr != nil {
					log.Printf("[web->tg %s] forward failed: %v", botName, terr)
				}
				// Forward the spoken audio too, so chatting from the web page
				// reaches Telegram with the same voice the page just played.
				if len(audioBytes) > 0 {
					voice := tgbotapi.NewAudio(cfg.UserID, tgbotapi.FileBytes{
						Name:  "voice" + detectAudioExtension(audioBytes),
						Bytes: audioBytes,
					})
					if _, terr := sender.Send(voice); terr != nil {
						log.Printf("[web->tg %s] voice forward failed: %v", botName, terr)
					}
				}
			}
		}
	}
	return result.Text, audio, nil
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	cfg := getWebConfig()
	configured := cfg != nil && len(cfg.Bots) > 0
	var bots []string
	var providers []map[string]string
	if cfg != nil {
		for _, b := range cfg.Bots {
			bots = append(bots, b.Name)
		}
		for i, m := range cfg.Ollama.Models {
			providers = append(providers, map[string]string{
				"index": strconv.Itoa(i), "name": m.Name,
				"type": providerTypeOf(m), "model": m.Model,
			})
		}
	}
	writeJSON(w, map[string]interface{}{
		"configured": configured, "bots": bots, "providers": providers,
	})
}

func handleAPIConfigGet(w http.ResponseWriter, r *http.Request) {
	cfg := getWebConfig()
	if cfg == nil {
		writeJSON(w, map[string]interface{}{"user_id": 0, "debug": false, "bots": []interface{}{}, "providers": []interface{}{}})
		return
	}
	type botOut struct {
		Name string `json:"name"`
		Mask string `json:"token_mask"`
		Has  bool   `json:"has_token"`
	}
	type provOut struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Key  string `json:"api_key"`
		Mask string `json:"key_mask"`
		Model string `json:"model"`
		Base string `json:"api_base"`
	}
	var bots []botOut
	for _, b := range cfg.Bots {
		bots = append(bots, botOut{b.Name, maskKey(b.Token), b.Token != ""})
	}
	var provs []provOut
	for _, m := range cfg.Ollama.Models {
		provs = append(provs, provOut{m.Name, providerTypeOf(m), m.APIKey, maskKey(m.APIKey), m.Model, effectiveBaseForUI(m)})
	}
	writeJSON(w, map[string]interface{}{
		"user_id": cfg.UserID, "debug": cfg.Debug, "bots": bots, "providers": provs,
	})
}

func effectiveBaseForUI(m ModelEntry) string {
	if strings.TrimSpace(m.APIBase) != "" {
		return strings.TrimRight(m.APIBase, "/")
	}
	if providerTypeOf(m) == ProviderTypeGoogle {
		return googleDefaultBase
	}
	return "http://localhost:11434"
}

func handleAPIConfigSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		UserID int64 `json:"user_id"`
		Debug  bool  `json:"debug"`
		Bots   []struct {
			Name string `json:"name"`
			Token string `json:"token"`
		} `json:"bots"`
		Providers []struct {
			Name string `json:"name"`
			Type string `json:"type"`
			APIKey string `json:"api_key"`
			Model string `json:"model"`
			APIBase string `json:"api_base"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(in.Bots) == 0 || len(in.Providers) == 0 {
		http.Error(w, "at least one bot and one provider required", http.StatusBadRequest)
		return
	}
	cfg := getWebConfig()
	oldBots := map[string]string{}
	oldKeys := map[string]string{}
	if cfg != nil {
		for _, b := range cfg.Bots {
			oldBots[b.Name] = b.Token
		}
		for _, m := range cfg.Ollama.Models {
			oldKeys[m.Name] = m.APIKey
		}
	}
	newCfg := &Config{UserID: in.UserID, Debug: in.Debug}
	for _, b := range in.Bots {
		tok := strings.TrimSpace(b.Token)
		if tok == "" {
			tok = oldBots[strings.TrimSpace(b.Name)]
		}
		newCfg.Bots = append(newCfg.Bots, BotConfig{Name: strings.TrimSpace(b.Name), Token: tok})
	}
	for _, p := range in.Providers {
		t := strings.ToLower(strings.TrimSpace(p.Type))
		if t != ProviderTypeGoogle {
			t = ProviderTypeOllama
		}
		base := strings.TrimSpace(p.APIBase)
		if base == "" {
			if t == ProviderTypeGoogle {
				base = googleDefaultBase
			} else {
				base = "http://localhost:11434"
			}
		}
		key := strings.TrimSpace(p.APIKey)
		if key == "" {
			key = oldKeys[strings.TrimSpace(p.Name)]
		}
		model := strings.TrimSpace(p.Model)
		if model == "" {
			if t == ProviderTypeGoogle {
				model = googleDefaultModels[0]
			} else {
				model = "gemma3:4b"
			}
		}
		newCfg.Ollama.Models = append(newCfg.Ollama.Models, ModelEntry{
			Name: strings.TrimSpace(p.Name), Type: t, APIKey: key, Model: model, APIBase: base,
		})
	}
	if err := writeConfigFile(newCfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := ensureDirectoryStructure(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	setWebConfig(newCfg)
	writeJSON(w, map[string]interface{}{"ok": true})
}

func handleAPITestTelegram(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if r.Method == http.MethodPost {
		var in struct {
			Token string `json:"token"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.Token != "" {
			token = in.Token
		}
	}
	token = strings.TrimSpace(token)
	if token == "" {
		http.Error(w, "token required", http.StatusBadRequest)
		return
	}
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "username": api.Self.UserName})
}

func handleAPITestProvider(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Type string `json:"type"`
		APIKey string `json:"api_key"`
		Model string `json:"model"`
		APIBase string `json:"api_base"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	t := strings.ToLower(strings.TrimSpace(in.Type))
	if t != ProviderTypeGoogle {
		t = ProviderTypeOllama
	}
	entry := ModelEntry{Name: "test", Type: t, APIKey: in.APIKey, Model: in.Model, APIBase: in.APIBase}
	if err := providerFor(entry).Test(); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	models, _ := listModelsForEntry(entry)
	writeJSON(w, map[string]interface{}{"ok": true, "models": models})
}

func handleAPIModels(w http.ResponseWriter, r *http.Request) {
	entry := ModelEntry{
		Type: r.URL.Query().Get("type"), APIKey: r.URL.Query().Get("key"),
		Model: r.URL.Query().Get("model"), APIBase: r.URL.Query().Get("base"),
	}
	if providerTypeOf(entry) == ProviderTypeOllama && strings.TrimSpace(entry.APIBase) == "" {
		entry.APIBase = "http://localhost:11434"
	}
	models, err := listModelsForEntry(entry)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	sort.Strings(models)
	writeJSON(w, map[string]interface{}{"models": models})
}

func handleAPIChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in chatRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(in.Bot) == "" || strings.TrimSpace(in.Message) == "" {
		http.Error(w, "bot and message required", http.StatusBadRequest)
		return
	}
	reply, audio, err := chatViaBackend(in.Bot, in.Message, "web")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// Let the page style command replies differently from chat replies.
	isCommand := strings.HasPrefix(strings.TrimSpace(in.Message), ".")
	writeJSON(w, map[string]interface{}{"bot": in.Bot, "reply": reply, "command": isCommand, "audio": audio})
}

func handleAPIHistory(w http.ResponseWriter, r *http.Request) {
	bot := r.URL.Query().Get("bot")
	story := "general"
	if cfg := getWebConfig(); cfg != nil {
		for _, b := range cfg.Bots {
			if b.Name == bot {
				if p := loadBotParams(bot, ""); p.CurrentStory != "" {
					story = p.CurrentStory
				}
				break
			}
		}
	}
	n := 50
	if v := r.URL.Query().Get("n"); v != "" {
		if iv, err := strconv.Atoi(v); err == nil && iv > 0 && iv <= 200 {
			n = iv
		}
	}
	hist := loadChatHistory(getChatLogPath(story, bot))
	if len(hist) > n {
		hist = hist[len(hist)-n:]
	}
	writeJSON(w, map[string]interface{}{"bot": bot, "story": story, "messages": hist})
}

func handleAPIStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch, unsub := hub.subscribe()
	defer unsub()
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()
	notify := r.Context().Done()
	for {
		select {
		case <-notify:
			return
		case m := <-ch:
			data, _ := json.Marshal(m)
			fmt.Fprintf(w, "data: %s\n\n", string(data))
			flusher.Flush()
		case <-time.After(25 * time.Second):
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// startWebServer boots the backend-first HTTP API + pages.
func startWebServer(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", handleAPIStatus)
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handleAPIConfigSave(w, r)
			return
		}
		handleAPIConfigGet(w, r)
	})
	mux.HandleFunc("/api/test/telegram", handleAPITestTelegram)
	mux.HandleFunc("/api/test/provider", handleAPITestProvider)
	mux.HandleFunc("/api/models", handleAPIModels)
	mux.HandleFunc("/api/chat", handleAPIChat)
	mux.HandleFunc("/api/history", handleAPIHistory)
	mux.HandleFunc("/api/stream", handleAPIStream)
	mux.HandleFunc("/setup", handleSetupPage)
	mux.HandleFunc("/setup.js", handleSetupJS)
	mux.HandleFunc("/chat", handleChatPage)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if cfg := getWebConfig(); cfg == nil || len(cfg.Bots) == 0 {
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/chat", http.StatusFound)
	})
	log.Printf("Web UI on http://%s (setup: /setup, chat: /chat)", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("Web server stopped: %v", err)
	}
}

// ensureWebConfigFile marshals in-memory config back to YAML (API edits).
func ensureWebConfigFile(cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile("config.yaml", data, 0644)
}
