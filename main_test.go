package main

import (
	"os"
	"strings"
	"testing"
)

func TestGetVoiceAssignmentsDisplay(t *testing.T) {
	assignments := map[string]string{
		"Dax":  "en-US-JennyNeural",
		"Ren":  "en-GB-SoniaNeural",
		"Sora": "en-US-GuyNeural",
	}

	got := GetVoiceAssignmentsDisplay(assignments)
	if !strings.Contains(got, "Dax") || !strings.Contains(got, "en-US-JennyNeural") {
		t.Fatalf("voice assignment list did not include character and selected voice: %q", got)
	}
	if !strings.Contains(got, "Ren") || !strings.Contains(got, "en-GB-SoniaNeural") {
		t.Fatalf("voice assignment list did not include character and selected voice: %q", got)
	}
	// Verify indexed listing format (1-based index in front of each voice)
	if !strings.Contains(got, "1)") || !strings.Contains(got, "2)") || !strings.Contains(got, "3)") {
		t.Fatalf("voice assignment list did not include index numbers in front of each entry: %q", got)
	}
	// Verify sorted order: Dax, Ren, Sora
	if strings.Index(got, "Dax") > strings.Index(got, "Ren") ||
		strings.Index(got, "Ren") > strings.Index(got, "Sora") {
		t.Fatalf("voice assignment list is not sorted alphabetically: %q", got)
	}
}

func TestLoadBotParams(t *testing.T) {
	// Check if config dir exists
	configExists := false
	if _, err := os.Stat("config"); err == nil {
		configExists = true
		// Rename config to config_backup
		err = os.Rename("config", "config_backup")
		if err != nil {
			t.Fatalf("failed to backup config: %v", err)
		}
		defer func() {
			os.RemoveAll("config")
			err := os.Rename("config_backup", "config")
			if err != nil {
				t.Logf("Warning: failed to restore config backup: %v", err)
			}
		}()
	} else {
		defer os.RemoveAll("config")
	}

	// Recreate a clean config dir
	err := os.Mkdir("config", 0755)
	if err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	// Test case 1: Neither exists -> should return defaultModel
	params := loadBotParams("testbot", "llama-default")
	if params.CurrentModel != "llama-default" {
		t.Errorf("expected llama-default, got %s", params.CurrentModel)
	}

	// Test case 2: Only fallback config/bot_params.yaml exists -> should load from it
	fallbackContent := []byte("current_model: llama-fallback\nselected_provider: 1\n")
	err = os.WriteFile("config/bot_params.yaml", fallbackContent, 0644)
	if err != nil {
		t.Fatalf("failed to write fallback bot params: %v", err)
	}

	params = loadBotParams("testbot", "llama-default")
	if params.CurrentModel != "llama-fallback" {
		t.Errorf("expected llama-fallback, got %s", params.CurrentModel)
	}
	if params.SelectedProvider != 1 {
		t.Errorf("expected selected provider 1, got %d", params.SelectedProvider)
	}

	// Test case 3: Both config/testbot_bot_params.yaml and config/bot_params.yaml exist -> should prioritize config/testbot_bot_params.yaml
	botSpecificContent := []byte("current_model: llama-specific\nselected_provider: 2\n")
	err = os.WriteFile("config/testbot_bot_params.yaml", botSpecificContent, 0644)
	if err != nil {
		t.Fatalf("failed to write bot-specific params: %v", err)
	}

	params = loadBotParams("testbot", "llama-default")
	if params.CurrentModel != "llama-specific" {
		t.Errorf("expected llama-specific, got %s", params.CurrentModel)
	}
	if params.SelectedProvider != 2 {
		t.Errorf("expected selected provider 2, got %d", params.SelectedProvider)
	}

	// Restore original config folder if it existed, handled by defer
	_ = configExists
}
