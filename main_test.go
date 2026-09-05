package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestStripPausePunctuation(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Punctuation inside double quotes is stripped
		{`"You're a mess, Sarah?"`, `"You're a mess Sarah"`},
		{`"Hello, world!"`, `"Hello world"`},
		{`"What? Really?!"`, `"What Really"`},
		{`"One, two; three: four."`, `"One two three four"`},
		// Punctuation outside double quotes is preserved
		{`He said, "Hello, world!" and left.`, `He said, "Hello world" and left.`},
		{`"Stop!" she yelled.`, `"Stop" she yelled.`},
		// No quotes - nothing changes
		{`Hello, world!`, `Hello, world!`},
		{`What? Really?!`, `What? Really?!`},
		{`One, two; three: four.`, `One, two; three: four.`},
		// Apostrophes preserved inside quotes
		{`"You're a mess, Sarah?"`, `"You're a mess Sarah"`},
		// Empty string
		{"", ""},
	}

	for _, tt := range tests {
		got := stripPausePunctuation(tt.input)
		if got != tt.expected {
			t.Errorf("stripPausePunctuation(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestPruneTraceFiles(t *testing.T) {
	// Create a temp directory to simulate a bot's trace folder
	dir := t.TempDir()

	// Write 12 files with monotonically increasing modification times
	for i := 0; i < 12; i++ {
		name := filepath.Join(dir, fmt.Sprintf("trace_%02d.json", i))
		if err := os.WriteFile(name, []byte("{}"), 0644); err != nil {
			t.Fatalf("failed to create trace file %s: %v", name, err)
		}
		// Set mod time 1 minute apart so ordering is deterministic
		modTime := time.Now().Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(name, modTime, modTime); err != nil {
			t.Fatalf("failed to set mod time on %s: %v", name, err)
		}
	}

	// Prune should keep only the 10 newest files
	pruneTraceFiles(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read temp dir: %v", err)
	}
	if len(entries) != maxTraceFilesPerBot {
		t.Fatalf("expected %d files after pruning, got %d", maxTraceFilesPerBot, len(entries))
	}

	// Verify the two oldest files (trace_00.json, trace_01.json) were removed
	for _, name := range []string{"trace_00.json", "trace_01.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("expected %s to be pruned (oldest), but it still exists", name)
		}
	}

	// Verify the newer files still exist
	for _, name := range []string{"trace_10.json", "trace_11.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s to be kept (newest), but it was removed: %v", name, err)
		}
	}
}

func TestLocalTTSRateConversion(t *testing.T) {
	if got := parseLocalTTSRate("+0%"); got != 0 {
		t.Fatalf("expected 0 for +0%%, got %d", got)
	}
	if got := parseLocalTTSRate("+50%"); got != 5 {
		t.Fatalf("expected 5 for +50%%, got %d", got)
	}
	if got := parseLocalTTSRate("-20%"); got != -2 {
		t.Fatalf("expected -2 for -20%%, got %d", got)
	}
	if got := parseLocalTTSRate("invalid"); got != 0 {
		t.Fatalf("expected 0 for invalid rate, got %d", got)
	}
}

func TestSaveAndLoadStorySnapshot(t *testing.T) {
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir to temp dir: %v", err)
	}
	defer func() {
		_ = os.Chdir(oldWD)
	}()

	context := []ContextMessage{
		{Role: "user", Content: "Tell me a story."},
		{Role: "assistant", Content: "Once upon a time..."},
	}
	storyOnly := []ContextMessage{{Role: "assistant", Content: "Once upon a time..."}}

	if err := saveStorySnapshot("midnight_run", "bot1", context, "story"); err != nil {
		t.Fatalf("saveStorySnapshot returned error: %v", err)
	}

	loaded, err := loadStorySnapshot("midnight_run", "bot1", "story")
	if err != nil {
		t.Fatalf("loadStorySnapshot returned error: %v", err)
	}
	if !reflect.DeepEqual(storyOnly, loaded) {
		t.Fatalf("loaded snapshot mismatch\nwant: %#v\ngot:  %#v", storyOnly, loaded)
	}

	if _, err := os.Stat(filepath.Join("stories", "midnight_run", "chat", "bot1_story_autosave.txt")); err != nil {
		t.Fatalf("expected saved story snapshot to exist: %v", err)
	}
}

func TestThreadSnapshotListAndLoad(t *testing.T) {
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir to temp dir: %v", err)
	}
	defer func() {
		_ = os.Chdir(oldWD)
	}()

	contextA := []ContextMessage{{Role: "assistant", Content: "chapter one"}}
	contextB := []ContextMessage{{Role: "assistant", Content: "chapter two"}}

	if err := rewriteChatLogFile(getStorySnapshotPath("my_story", "bot1", "chapter_one"), contextA, "story"); err != nil {
		t.Fatalf("save chapter one snapshot returned error: %v", err)
	}
	if err := rewriteChatLogFile(getStorySnapshotPath("my_story", "bot1", "chapter_two"), contextB, "story"); err != nil {
		t.Fatalf("save chapter two snapshot returned error: %v", err)
	}

	threads, err := listStorySnapshots("my_story", "bot1")
	if err != nil {
		t.Fatalf("listStorySnapshots returned error: %v", err)
	}
	if len(threads) < 2 {
		t.Fatalf("expected at least 2 thread snapshots, got %d: %#v", len(threads), threads)
	}

	loaded, err := loadThreadSnapshotByIndex("my_story", "bot1", 2)
	if err != nil {
		t.Fatalf("loadThreadSnapshotByIndex returned error: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Content != "chapter two" {
		t.Fatalf("thread load by index returned unexpected content: %#v", loaded)
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
