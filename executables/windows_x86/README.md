# VividGo2 - Windows x86 (32-bit)

**Platform:** Windows 32-bit (x86)
**File:** `vividgo2.exe`

## System Requirements

- Windows 7 or later (32-bit)
- No additional dependencies required

## How to Run

1. **Download** `vividgo2.exe` from this folder
2. **Double-click** the executable, or run from Command Prompt:
   ```
   vividgo2.exe
   ```
3. On first run, the setup wizard will guide you through configuration

## Configuration

The bot requires a `config.yaml` file in the same directory as the executable. You can:
- Run the interactive setup wizard (launched automatically on first run)
- Or manually create `config.yaml` based on the [project documentation](https://github.com/NightTrek/Ollama-mcp)

## First Time Setup

When you run the executable for the first time, it will:
1. Prompt you for your Telegram Bot Token (get this from [@BotFather](https://t.me/BotFather))
2. Ask for your Telegram User ID (get this from [@userinfobot](https://t.me/userinfobot))
3. Request your Ollama API key (get this from [ollama.com/settings/keys](https://ollama.com/settings/keys))
4. Create the necessary directory structure and configuration files

## Building from Source

If you prefer to build from source, see the main project [README.md](../../README.md) for instructions, or run `build_all.bat` in the `executables/` folder.