# VividGo2 - FreeBSD x64

**Platform:** FreeBSD 64-bit (x86_64)
**File:** `vividgo2`

## System Requirements

- FreeBSD 12.x or later (64-bit)
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

## Running as a Service (rc.d)

To run the bot as a background service on FreeBSD, create an rc script:

```bash
sudo nano /usr/local/etc/rc.d/vividgo2
```

Add the following (adjust paths as needed):

```sh
#!/bin/sh

# PROVIDE: vividgo2
# REQUIRE: NETWORKING
# KEYWORD: shutdown

. /etc/rc.subr

name="vividgo2"
rcvar="vividgo2_enable"

command="/path/to/vividgo2/vividgo2"
pidfile="/var/run/${name}.pid"
command_interpreter="/nonexistent"

load_rc_config $name
run_rc_command "$1"
```

Make it executable and enable the service:

```bash
sudo chmod +x /usr/local/etc/rc.d/vividgo2
echo 'vividgo2_enable="YES"' | sudo tee -a /etc/rc.conf
sudo service vividgo2 start
```

## Building from Source

If you prefer to build from source, see the main project [README.md](../../README.md) for instructions, or run `build_all.bat` in the `executables/` folder (requires a Windows machine).