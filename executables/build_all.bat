@echo off
setlocal enabledelayedexpansion

:: ============================================================
:: VividGo2 - Cross-Platform Build Script
:: ============================================================
:: Run this batch file from the project root (where main.go is)
:: It will compile for all supported platforms and place the
:: executables into organized folders under executables/
:: ============================================================

echo.
echo ============================================================
echo  VividGo2 - Building for all platforms...
echo ============================================================
echo.

:: Get the directory where this batch file is located
set "SCRIPT_DIR=%~dp0"
:: Remove trailing backslash
set "SCRIPT_DIR=%SCRIPT_DIR:~0,-1%"

:: Change to the project root (parent of executables folder)
cd /d "%SCRIPT_DIR%\.."

echo Current directory: %cd%
echo.
echo ============================================================
echo.

:: Check if Go is installed
where go >nul 2>nul
if %ERRORLEVEL% neq 0 (
    echo [ERROR] Go is not installed or not in PATH.
    echo Please install Go from https://go.dev/dl/
    pause
    exit /b 1
)

echo Go version:
go version
echo.

:: Create all target directories
echo Creating target directories...
for %%d in (
    "windows_x64"
    "windows_x86"
    "windows_arm64"
    "linux_x64"
    "linux_x86"
    "linux_arm64"
    "linux_arm_pi_zero_w"
    "linux_arm_pi2"
    "linux_arm_pi3_32"
    "mac_x64"
    "mac_arm64"
    "freebsd_x64"
) do (
    if not exist "executables\%%~d" mkdir "executables\%%~d"
)
echo Done.
echo.

:: ============================================================
:: Windows Targets
:: ============================================================
echo [1/12] Building for Windows x64...
set GOOS=windows
set GOARCH=amd64
set GOARM=
go build -ldflags="-s -w" -o executables\windows_x64\vividgo2.exe main.go setup.go
if %ERRORLEVEL% equ 0 (echo   OK) else (echo   FAILED)
echo.

echo [2/12] Building for Windows x86 (32-bit)...
set GOOS=windows
set GOARCH=386
set GOARM=
go build -ldflags="-s -w" -o executables\windows_x86\vividgo2.exe main.go setup.go
if %ERRORLEVEL% equ 0 (echo   OK) else (echo   FAILED)
echo.

echo [3/12] Building for Windows ARM64...
set GOOS=windows
set GOARCH=arm64
set GOARM=
go build -ldflags="-s -w" -o executables\windows_arm64\vividgo2.exe main.go setup.go
if %ERRORLEVEL% equ 0 (echo   OK) else (echo   FAILED)
echo.

:: ============================================================
:: Linux Targets
:: ============================================================
echo [4/12] Building for Linux x64...
set GOOS=linux
set GOARCH=amd64
set GOARM=
go build -ldflags="-s -w" -o executables\linux_x64\vividgo2 main.go setup.go
if %ERRORLEVEL% equ 0 (echo   OK) else (echo   FAILED)
echo.

echo [5/12] Building for Linux x86 (32-bit)...
set GOOS=linux
set GOARCH=386
set GOARM=
go build -ldflags="-s -w" -o executables\linux_x86\vividgo2 main.go setup.go
if %ERRORLEVEL% equ 0 (echo   OK) else (echo   FAILED)
echo.

echo [6/12] Building for Linux ARM64 (e.g. Pi 4/5 64-bit OS)...
set GOOS=linux
set GOARCH=arm64
set GOARM=
go build -ldflags="-s -w" -o executables\linux_arm64\vividgo2 main.go setup.go
if %ERRORLEVEL% equ 0 (echo   OK) else (echo   FAILED)
echo.

echo [7/12] Building for Raspberry Pi Zero W / Pi 1 (ARMv6)...
set GOOS=linux
set GOARCH=arm
set GOARM=6
go build -ldflags="-s -w" -o executables\linux_arm_pi_zero_w\vividgo2 main.go setup.go
if %ERRORLEVEL% equ 0 (echo   OK) else (echo   FAILED)
echo.

echo [8/12] Building for Raspberry Pi 2 (ARMv7 32-bit)...
set GOOS=linux
set GOARCH=arm
set GOARM=7
go build -ldflags="-s -w" -o executables\linux_arm_pi2\vividgo2 main.go setup.go
if %ERRORLEVEL% equ 0 (echo   OK) else (echo   FAILED)
echo.

echo [9/12] Building for Raspberry Pi 3/4/5 (32-bit OS, ARMv7)...
set GOOS=linux
set GOARCH=arm
set GOARM=7
go build -ldflags="-s -w" -o executables\linux_arm_pi3_32\vividgo2 main.go setup.go
if %ERRORLEVEL% equ 0 (echo   OK) else (echo   FAILED)
echo.

:: ============================================================
:: macOS Targets
:: ============================================================
echo [10/12] Building for macOS Intel (x64)...
set GOOS=darwin
set GOARCH=amd64
set GOARM=
go build -ldflags="-s -w" -o executables\mac_x64\vividgo2 main.go setup.go
if %ERRORLEVEL% equ 0 (echo   OK) else (echo   FAILED)
echo.

echo [11/12] Building for macOS Apple Silicon (M1/M2/M3/M4)...
set GOOS=darwin
set GOARCH=arm64
set GOARM=
go build -ldflags="-s -w" -o executables\mac_arm64\vividgo2 main.go setup.go
if %ERRORLEVEL% equ 0 (echo   OK) else (echo   FAILED)
echo.

:: ============================================================
:: FreeBSD Targets
:: ============================================================
echo [12/12] Building for FreeBSD x64...
set GOOS=freebsd
set GOARCH=amd64
set GOARM=
go build -ldflags="-s -w" -o executables\freebsd_x64\vividgo2 main.go setup.go
if %ERRORLEVEL% equ 0 (echo   OK) else (echo   FAILED)
echo.

:: ============================================================
:: Summary
:: ============================================================
echo ============================================================
echo  Build complete!
echo ============================================================
echo.
echo Output sizes:
echo.

for %%d in (
    "windows_x64\vividgo2.exe"
    "windows_x86\vividgo2.exe"
    "windows_arm64\vividgo2.exe"
    "linux_x64\vividgo2"
    "linux_x86\vividgo2"
    "linux_arm64\vividgo2"
    "linux_arm_pi_zero_w\vividgo2"
    "linux_arm_pi2\vividgo2"
    "linux_arm_pi3_32\vividgo2"
    "mac_x64\vividgo2"
    "mac_arm64\vividgo2"
    "freebsd_x64\vividgo2"
) do (
    if exist "executables\%%~d" (
        for %%f in ("executables\%%~d") do (
            set "fname=%%~d"
            set "fsize=%%~zf"
            :: Pad the filename to align columns
            set "fname=!fname!                     "
            echo   !fname:~0,35! !fsize! bytes
        )
    ) else (
        set "fname=%%~d"
        set "fname=!fname!                     "
        echo   !fname:~0,35! [NOT BUILT]
    )
)

echo.
echo ============================================================
echo  All builds completed. Executables are in the executables/
echo  folder, organized by platform.
echo ============================================================
echo.

pause