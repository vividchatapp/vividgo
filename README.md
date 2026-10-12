# VividGo

🤖 A multi-bot Telegram AI assistant powered by Ollama and Google Gemini, with a built-in web UI (setup + chat), immersive story-writing, roleplay, voice output, and cross-platform builds.

## 🚀 Quickstart — Download the Executable for Your Operating System

Pre-built binaries are available for Windows, Linux, macOS, and FreeBSD. **Download the executable for your operating system here: [executables/readme.md](executables/readme.md)**

For background-launch examples and Unix/Linux/macOS launcher scripts, see the executable guide: [executables/readme.md](executables/readme.md)

## ✨ Features

### Web UI — Setup & Chat
- Built-in web server, **enabled by default** at `http://localhost:8080` (bound to `127.0.0.1` only — not exposed to the network)
- `/setup` — browser-based configuration editor: add and test bot tokens, add Ollama/Google providers, fetch model lists, and save `config.yaml` (alternative to the terminal wizard)
- `/chat` — full browser chat client: bot sidebar, Markdown rendering, hover-to-copy on every message, command replies styled distinctly, and per-bot history loaded on open
- **Live Telegram mirror** — replies sent in Telegram stream into open chat pages over Server-Sent Events, including spoken audio, which plays automatically in the browser
- **Shared state** — the web chat and Telegram funnel through the same command dispatcher against the same session, so `.model`, `.role`, `.status`, etc. work identically on both transports and stay in sync
- Degrades gracefully: if a Telegram token is invalid, that bot still works over the web transport
- Disable with `--no-web`, change the port with `--port` or the `PORT` env var

### Multi-Bot Support
- Run **multiple Telegram bots** simultaneously from a single process
- Each bot has its own persistent configuration (`config/<botname>_bot_params.yaml`)
- Per-bot provider, model, mode, role, story, context size, and voice settings

### AI Providers & Models
- Connect to **Ollama** (ollama.com or any local/remote Ollama instance)
- **Google Gemini** through its OpenAI-compatible endpoint (defaults to `gemini-2.5-flash`, base `https://generativelanguage.googleapis.com/v1beta/openai`)
- Multiple providers with independent API keys and base URLs, selectable per bot (`.provider`)
- List, switch, and cycle models (`model`, `model +`, `model -`)
- Test models for subscription/upgrade requirements (`model test`)
- Filtered model list for quick cycling (`mf`)
- Configurable context window (`llmctx` / `ctxsize`, e.g. `8k`, `16k`, `32k`)
- Think / NoThink toggle for the Ollama API

### Story & Roleplay Mode
- **Story mode** with immersive narrative generation (Story Weaver)
- Multiple story folders under `stories/` with `chat/`, `scenes/`, and `characters/`
- Create and switch stories from chat (`story create [name]`) — the new story folder is created with its `chat/`, `scenes/`, and `characters/` sub-folders
- Switching stories (`.story [n]`) reloads that story's chat history into the context, so every story keeps its own memory
- Active scene and character injection into prompts
- **Chat mode** for regular conversation with full history

### Roles
- Load role profiles from `roles/*.txt` (e.g. Assistant, StoryWriter, AssistantX, etc.)
- Create, edit, save, and reload roles on the fly
- Download roles from the `vividchatapp/vivid-data` GitHub repo during setup
- Older/experimental role files are archived under `roles/roles_old/`

### Voice Output
- Speak AI responses as MP3 audio via Microsoft Edge's online TTS service
- Always uses Edge neural voices (e.g. `en-GB-SoniaNeural`) — there is no local/offline OS voice fallback, so speech always sounds like the configured Edge voice (requires internet access)
- In-memory generation — no temporary files written to disk
- Toggle per bot with `.voice [on/off]` - Speak AI responses as audio
- `.voice speed [1-10]` - Set voice speed (10% increments)
- `.voice char [on/off]` - Multi-voice character narration
- `.voice list` - Show the numbered character → voice assignment list
- `.voice change [n] [accent]` - Ask the AI to pick a new voice for character `n` from an accent description (e.g. `.voice change 2 deep southern accent`)
- `.voice punct [on/off]` - Keep (`on`, default) or strip (`off`) punctuation in spoken audio. `off` removes punctuation from quoted dialogue, which makes Edge TTS read without pauses (legacy run-on mode)
- Spoken replies are generated fully in memory and are also played back in the web chat

### Conversation Management
- Persistent chat history saved to disk (`stories/<story>/chat/<bot>_chat_history.txt`)
- Context limit control (`context`)
- Delete last exchange (`del`), resend last message (`resend`)
- Full cleanup (`clean`) and memory wipe (`clear`)
- History viewer (`history` / `hs`)
- Ask questions outside roleplay context (`ask`)
- Auto-deleting status/command messages to keep the chat UI clean

### Setup Wizard
- Interactive terminal wizard on first run, or any time with `--init`
- Browser-based alternative at `/setup` (add/test bots and providers, fetch model lists, save `config.yaml`)
- Auto-discovers models from local Ollama instances
- Creates directory structure and default role files
- Downloads roles from GitHub

## 🚀 Getting Started

### Prerequisites
- [Go](https://go.dev/dl/) 1.26+
- A Telegram bot token from [@BotFather](https://t.me/BotFather)
- Your Telegram user ID (from [@userinfobot](https://t.me/userinfobot))
- An Ollama API key (https://ollama.com/settings/keys), a local Ollama instance, or a Google AI Studio key (https://aistudio.google.com/apikey)

### Build

```bash
# Build for the current platform
go build -o vividgo.exe .

# Or build for all platforms (Windows, Linux, macOS, FreeBSD, Raspberry Pi)
executables\build_all.bat    # Windows
executables\build_all.ps1    # PowerShell
```

### Run

```bash
# First run: launches the setup wizard, then starts the bots + web UI
./vividgo.exe

# Re-run the setup wizard with existing values
./vividgo.exe --init

# Web UI (default http://localhost:8080 — /setup and /chat)
./vividgo.exe --port 9090     # Serve the web UI on a different port
./vividgo.exe --no-web        # Telegram only, no web server

# Disable ANSI colors in the terminal setup wizard
./vividgo.exe --no-color
```

| Flag | Description |
|------|-------------|
| `--init` | Re-run the configuration wizard with existing values |
| `--port [n]` | Web UI port (default `8080`; `PORT` env var is used when set) |
| `--no-web` | Disable the web UI entirely |
| `--no-color` | Disable ANSI colors in the terminal setup wizard |

### Configuration

Configuration is stored in `config.yaml`. You can also configure via environment variables:

| Variable | Description |
|----------|-------------|
| `TELEGRAM_BOT_TOKEN` | Telegram bot token (single-bot mode) |
| `TELEGRAM_USER_ID` | Your authorized Telegram user ID |
| `TELEGRAM_DEBUG` | Set to `true` for debug mode |
| `PORT` | Web UI port (default `8080`) |
| `OLLAMA_API_BASE` | Ollama API base URL (default: `https://api.ollama.com`) |
| `OLLAMA_API_KEY` | Ollama API key |
| `OLLAMA_MODEL` | Model to use (default: `gemma4:31b`) |

## 🌐 Web UI

The web server starts automatically alongside the bots (skip it with `--no-web`) and binds to `127.0.0.1` only.

| URL | Description |
|-----|-------------|
| `/setup` | Configuration editor — Telegram user ID, bots (add/test tokens), providers (Ollama/Google, test connection, fetch model list), saves `config.yaml` |
| `/chat` | Chat client — pick a bot in the sidebar, send messages or `.commands`, Markdown rendering, copy buttons, voice playback |
| `/api/status` | Configured bot names |
| `/api/config` | GET/POST the active configuration (used by `/setup`) |
| `/api/test/telegram` | Validate a bot token and return its username |
| `/api/test/provider` | Test a provider connection and return available models |
| `/api/models` | List models for a provider entry |
| `/api/chat` | Send a message to a bot; returns the reply (plus voice audio when enabled) |
| `/api/history` | Recent chat history for a bot |
| `/api/stream` | Server-Sent Events stream of live Telegram replies (and audio) |

The web chat is a thin view: every message goes through the same `HandleMessage` dispatcher as Telegram, against the same `BotSession` state, so commands and conversation memory stay in sync across both transports.

## 📚 Commands

All commands start with a dot (`.`) or can be used without it. Type `.help` in Telegram (or the web chat) for the full list.

### Stories & Chapters
| Command | Description |
|---------|-------------|
| `.story` | List story folders |
| `.story [n]` | Select story folder (clears scenes/chars, loads that story's chat history into context) |
| `.story create [name]` | Create a new story folder (`chat/`, `scenes/`, `characters/`) and switch to it |
| `.story save [name]` | Save current conversation as a story snapshot |
| `.story load [name]` | Load a saved story snapshot |
| `.chapter` / `.ch` | List saved chapters in current story |
| `.chapter [n]` / `.ch [n]` | Pick/load listed chapter by number |
| `.chapter save [name]` / `.ch save [name]` | Save current conversation as a chapter |
| `.chapter load [number|name]` / `.ch load [number|name]` | Load a saved chapter |
| `.char` | List characters in current story |
| `.char 1 -2 3` | Multi-toggle characters (prefix `-` to deactivate) |
| `.char all off` | Deactivate all characters |
| `.char edit [n]` | Show character bio |
| `.char save [name] [text]` | Save character bio |
| `.scene` | List scenes in current story |
| `.scene 1 -2 3` | Multi-toggle scenes |
| `.scene all off` | Deactivate all scenes |
| `.scene edit [n]` | Show scene description |
| `.scene save [name] [text]` | Save scene description |

### Roles
| Command | Description |
|---------|-------------|
| `.role` | List available roles |
| `.role [n]` | Switch to role |
| `.role edit [n]` | Get role text to edit |
| `.role save [name] [text]` | Save role profile |
| `.reload` | Reload role from disk |
| `.rs [n]` | Summarize current role |

### AI Providers & Models
| Command | Description |
|---------|-------------|
| `.provider` / `.p` | List providers |
| `.provider [n]` / `.p [n]` | Switch provider |
| `.model` | List available models |
| `.model [n]` | Switch model |
| `.model +` / `.model -` | Next/previous model |
| `.model test` | Test online models for subscription requirements |
| `.mf [n/next/prev]` | List or cycle accessible models |
| `.model loaded` | Sync with RAM |
| `.think` | Toggle Think Mode |
| `.nothink [on/off]` | Toggle NoThink flag for Ollama API |
| `.llmctx [nk]` | Set/show model context window (e.g. `8k`) |

### Conversation & Chats
| Command | Description |
|---------|-------------|
| `.clear` | Wipe bot memory (context) |
| `.clean` | Wipe memory and delete messages from chat UI |
| `.clean cmd` | Delete only command messages (preserve memory) |
| `.mode [chat/story]` | Toggle history behavior |
| `.del` | Delete last message + response |
| `.resend` | Resend last user message |
| `.ask [text]` | Ask a question outside of roleplay context |
| `.context [n]` | Set/show context limit |
| `.history [n] [full] [keep]` | Show last n interactions |

### Status & Shortcuts
| Command | Description |
|---------|-------------|
| `.status` | Show current settings |
| `.verbose` | Toggle Verbose Status |
| `.trace [on/off]` | Write payloads to context folder |
| `.voice [on/off]` | Speak AI responses as audio |
| `.voice speed [1-10]` | Set voice speed (10% increments) |
| `.voice char [on/off]` | Multi-voice character narration |
| `.voice list` | Show character → voice assignments |
| `.voice change [n] [accent]` | Pick a new voice for character n from an accent description |
| `.voice punct [on/off]` | Keep (on, default) or strip (off) punctuation in spoken audio |

**Synonyms:** `r`=role, `rs`=rolesummary, `p`=provider, `m`=model, `s`=status, `h`=help, `ch`=chapter, `c`=chat, `cl`=clean, `mf`=modelsfiltered, `sc`=scene, `hs`=history, `mo`=mode, `mc`=llmctx

## 📁 Project Structure

```
vividgo/
├── main.go                 # Telegram adapter + entry point (flags, bot goroutines)
├── commands.go             # Transport-neutral command dispatcher (HandleMessage)
├── session.go              # BotSession: per-bot state shared by all transports
├── providers.go            # LLMProvider interface (Ollama + Google Gemini backends)
├── web.go                  # Web server, JSON API, SSE stream
├── webpages.go             # /setup and /chat HTML pages
├── setup.go                # Terminal setup wizard
├── voice.go                # Edge TTS voice generation (in-memory MP3)
├── colors_windows.go       # ANSI color support (Windows)
├── colors_unix.go          # ANSI color support (Unix)
├── commands_test.go        # Dispatcher and command parsing tests
├── main_test.go            # Core logic tests
├── config.yaml             # Bot and provider configuration
├── config/                 # Per-bot params and filtered models
│   ├── <bot>_bot_params.yaml
│   └── filtered_models.txt
├── context/                # Trace payloads (.trace on/off)
├── roles/                  # Role profile text files (archives in roles/roles_old/)
├── stories/                # Story folders (chat/scenes/characters)
└── executables/            # Cross-platform build outputs
    ├── windows_x64/        # vividgo.exe
    ├── linux_x64/          # vividgo
    ├── mac_arm64/          # vividgo
    └── ...                 # All supported platforms
```

## 🏗 Architecture

The codebase is **transport-neutral**: every incoming message — whether from the Telegram long-polling loop or the web chat API — funnels through one dispatcher.

- `session.go` — `BotSession` holds all per-bot runtime state (provider, model, mode, story, context, voice assignments) in a registry that is populated at startup *before* any Telegram connection is attempted. An invalid token therefore degrades to "web only" instead of taking everything down.
- `commands.go` — `HandleMessage` parses the command, mutates the session, and returns the reply text. It never touches `tgbotapi`: Telegram-specific rendering (MarkdownV2 escaping, chunking, auto-delete, voice audio) lives in the adapter in `main.go`, and web rendering lives in `web.go`.
- `providers.go` — an `LLMProvider` interface with Ollama and Google backends, so chat, model listing, and connection tests behave the same for both transports.
- Command parsing differs per transport by design: Telegram accepts bare words (`model 3`), while the web chat only treats dot-prefixed (or recognized) words as commands, so ordinary chat text always reaches the LLM.

## 🧪 Testing

```bash
go build ./...   # Compile everything
go test ./...    # Run the test suite (dispatcher, parsing, core logic)
```

## 🔧 Cross-Platform Builds

The `executables/` folder contains pre-built binaries for:

| Platform | Output |
|----------|--------|
| Windows x64 / x86 / ARM64 | `vividgo.exe` |
| Linux x64 / x86 / ARM64 | `vividgo` |
| Raspberry Pi Zero W / Pi 1 (ARMv6) | `vividgo` |
| Raspberry Pi 2 (ARMv7) | `vividgo` |
| Raspberry Pi 3/4/5 (ARMv7 32-bit) | `vividgo` |
| macOS Intel (x64) | `vividgo` |
| macOS Apple Silicon (M1/M2/M3/M4) | `vividgo` |
| FreeBSD x64 | `vividgo` |

## 📝 License

MIT License