# VividGo2 - Linux ARM64

**Platform:** Linux ARM64 (aarch64) - Raspberry Pi 4/5 (64-bit OS), AWS Graviton, Oracle ARM, etc.
**File:** `vividgo2`

## System Requirements

- Any Linux distribution running on ARM64 hardware
- Examples: Raspberry Pi OS (64-bit), Ubuntu Server for ARM, Debian ARM64
- No additional dependencies required

## How to Run

1. **Download** `vividgo2` from this folder
2. **Make it executable** and run:
   ```bash
   chmod +x vividgo2
   ./vividgo2
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

## Running as a Service (systemd)

To run the bot as a background service on your Pi or server, create a systemd service file:

```bash
sudo nano /etc/systemd/system/vividgo2.service
```

Add the following (adjust paths as needed):

```ini
[Unit]
Description=VividGo2 Telegram Bot
After=network.target

[Service]
Type=simple
User=pi
WorkingDirectory=/home/pi/vividgo2
ExecStart=/home/pi/vividgo2/vividgo2
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
```

Then enable and start the service:

```bash
sudo systemctl daemon-reload
sudo systemctl enable vividgo2
sudo systemctl start vividgo2
```

## Building from Source

If you prefer to build from source, see the main project [README.md](../../README.md) for instructions, or run `build_all.bat` in the `executables/` folder (requires a Windows machine).