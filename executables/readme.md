# Quickstart — Download the Executable for Your Operating System

Pre-built binaries for VividGo are available below. Download the executable that matches your operating system and architecture, then follow the platform-specific instructions.

---

## Windows

| Architecture | File | Download |
|---|---|---|
| Windows x64 (Intel/AMD 64-bit) | `vividgo.exe` | [Download](https://github.com/vividchatapp/vividgo/raw/main/executables/windows_x64/vividgo.exe) |
| Windows x86 (32-bit) | `vividgo.exe` | [Download](https://github.com/vividchatapp/vividgo/raw/main/executables/windows_x86/vividgo.exe) |
| Windows ARM64 | `vividgo.exe` | [Download](https://github.com/vividchatapp/vividgo/raw/main/executables/windows_arm64/vividgo.exe) |

**How to run:** Double-click the `.exe` file, or run from Command Prompt / PowerShell:
```
vividgo.exe
```

---

## Linux

| Architecture | File | Download |
|---|---|---|
| Linux x64 (Intel/AMD 64-bit) | `vividgo` | [Download](https://github.com/vividchatapp/vividgo/raw/main/executables/linux_x64/vividgo) |
| Linux x86 (32-bit) | `vividgo` | [Download](https://github.com/vividchatapp/vividgo/raw/main/executables/linux_x86/vividgo) |
| Linux ARM64 | `vividgo` | [Download](https://github.com/vividchatapp/vividgo/raw/main/executables/linux_arm64/vividgo) |
| Raspberry Pi Zero W / Pi 1 (ARMv6) | `vividgo` | [Download](https://github.com/vividchatapp/vividgo/raw/main/executables/linux_arm_pi_zero_w/vividgo) |
| Raspberry Pi 2 (ARMv7) | `vividgo` | [Download](https://github.com/vividchatapp/vividgo/raw/main/executables/linux_arm_pi2/vividgo) |
| Raspberry Pi 3/4/5 (ARMv7 32-bit) | `vividgo` | [Download](https://github.com/vividchatapp/vividgo/raw/main/executables/linux_arm_pi3_32/vividgo) |

**How to run:**
```bash
chmod +x vividgo
./vividgo
```

---

## macOS

| Architecture | File | Download |
|---|---|---|
| macOS Intel (x64) | `vividgo` | [Download](https://github.com/vividchatapp/vividgo/raw/main/executables/mac_x64/vividgo) |
| macOS Apple Silicon (M1/M2/M3/M4) | `vividgo` | [Download](https://github.com/vividchatapp/vividgo/raw/main/executables/mac_arm64/vividgo) |

**How to run:**
```bash
chmod +x vividgo
./vividgo
```

> **Note:** macOS may block the executable because it's from an unidentified developer. To bypass this:
> - Go to **System Settings > Privacy & Security**
> - Scroll down and click **"Open Anyway"** next to the blocked message
> - Or in Terminal, run: `xattr -d com.apple.quarantine vividgo`

---

## FreeBSD

| Architecture | File | Download |
|---|---|---|
| FreeBSD x64 | `vividgo` | [Download](https://github.com/vividchatapp/vividgo/raw/main/executables/freebsd_x64/vividgo) |

**How to run:**
```bash
chmod +x vividgo
./vividgo
```

---

## First Time Setup

When you run the executable for the first time, it will:
1. Prompt you for your Telegram Bot Token (get this from [@BotFather](https://t.me/BotFather))
2. Ask for your Telegram User ID (get this from [@userinfobot](https://t.me/userinfobot))
3. Request your Ollama API key (get this from [ollama.com/settings/keys](https://ollama.com/settings/keys))
4. Create the necessary directory structure and configuration files

## Building from Source

If you prefer to build from source, see the main project [README.md](../README.md) for instructions, or run `build_all.bat` / `build_all.ps1` in the `executables/` folder.
