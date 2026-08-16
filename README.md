# VividGo

🤖 A multi-bot Telegram AI assistant powered by Ollama, with immersive story-writing, roleplay, voice output, and cross-platform builds.

## 🚀 Quickstart — Download the Executable for Your Operating System

Pre-built binaries are available for Windows, Linux, macOS, and FreeBSD. **Download the executable for your operating system here: [executables/readme.md](executables/readme.md)**

## ✨ Features

### Multi-Bot Support
- Run **multiple Telegram bots** simultaneously from a single process
- Each bot has its own persistent configuration (`config/<botname>_bot_params.yaml`)
- Per-bot provider, model, mode, role, story, context size, and voice settings

### AI Providers & Models
- Connect to **Ollama** (ollama.com or any local/remote Ollama instance)
- Multiple providers with independent API keys and base URLs
- List, switch, and cycle models (`model`, `model +`, `model -`)
- Test models for subscription/upgrade requirements (`model test`)
- Filtered model list for quick cycling (`mf`)
- Configurable context window (`llmctx` / `ctxsize`, e.g. `8k`, `16k`, `32k`)
- Think / NoThink toggle for the Ollama API

### Story & Roleplay Mode
- **Story mode** with immersive narrative generation (Story Weaver)
- Multiple story folders under `stories/` with `chat/`, `scenes/`, and `characters/`
- Active scene and character injection into prompts
- **Chat mode** for regular conversation with full history

### Roles
- Load role profiles from `roles/*.txt` (e.g. Assistant, StoryWriter, AssistantX, etc.)
- Create, edit, save, and reload roles on the fly
- Download roles from the `vividchatapp/vivid-data` GitHub repo during setup

### Voice Output
- Speak AI responses as MP3 audio via Microsoft Edge's online TTS service
- In-memory generation — no temporary files written to disk
- Toggle per bot with `.voice [on/off]` - Speak AI responses as audio
- `.voice speed [1-10]` - Set voice speed (10% increments)
- `.voice char [on/off]` - Multi-voice character narration

### Conversation Management
- Persistent chat history saved to disk (`stories/<story>/chat/<bot>_chat_history.txt`)
- Context limit control (`context`)
- Delete last exchange (`del`), resend last message (`resend`)
- Full cleanup (`clean`) and memory wipe (`clear`)
- History viewer (`history` / `hs`)
- Ask questions outside roleplay context (`ask`)
- Auto-deleting status/command messages to keep the chat UI clean

### Setup Wizard
- Interactive `--init` configuration wizard
- Auto-discovers models from local Ollama instances
- Creates directory structure and default role files
- Downloads roles from GitHub

## 🚀 Getting Started

### Prerequisites
- [Go](https://go.dev/dl/) 1.26+
- A Telegram bot token from [@BotFather](https://t.me/BotFather)
- Your Telegram user ID (from [@userinfobot](https://t.me/userinfobot))
- An Ollama API key (https://ollama.com/settings/keys) or a local Ollama instance

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
# First run: launches the setup wizard automatically
./vividgo.exe

# Re-run the setup wizard with existing values
./vividgo.exe --init

# Disable ANSI colors in the setup wizard
./vividgo.exe --no-color
```

### Configuration

Configuration is stored in `config.yaml`. You can also configure via environment variables:

| Variable | Description |
|----------|-------------|
| `TELEGRAM_BOT_TOKEN` | Telegram bot token (single-bot mode) |
| `TELEGRAM_USER_ID` | Your authorized Telegram user ID |
| `TELEGRAM_DEBUG` | Set to `true` for debug mode |
| `OLLAMA_API_BASE` | Ollama API base URL (default: `https://api.ollama.com`) |
| `OLLAMA_API_KEY` | Ollama API key |
| `OLLAMA_MODEL` | Model to use (default: `gemma4:31b`) |

## 📚 Commands

All commands start with a dot (`.`) or can be used without it. Type `.help` in Telegram for the full list.

### Stories & Characters
| Command | Description |
|---------|-------------|
| `.story` | List story folders |
| `.story [n]` | Select story folder (clears scenes/chars) |
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

**Synonyms:** `r`=role, `rs`=rolesummary, `p`=provider, `m`=model, `s`=status, `h`=help, `c`=chat, `cl`=clean, `mf`=modelsfiltered, `sc`=scene, `hs`=history, `mo`=mode, `mc`=llmctx

## 📁 Project Structure

```
vividgo/
├── main.go                 # Main bot logic and command handlers
├── setup.go                # Interactive setup wizard
├── voice.go                # Edge TTS voice generation (in-memory MP3)
├── colors_windows.go       # ANSI color support (Windows)
├── colors_unix.go          # ANSI color support (Unix)
├── config.yaml             # Bot and provider configuration
├── config/                 # Per-bot params and filtered models
│   ├── <bot>_bot_params.yaml
│   └── filtered_models.txt
├── roles/                  # Role profile text files
├── stories/                # Story folders (chat/scenes/characters)
└── executables/            # Cross-platform build outputs
    ├── windows_x64/        # vividgo.exe
    ├── linux_x64/          # vividgo
    ├── mac_arm64/          # vividgo
    └── ...                 # All supported platforms
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