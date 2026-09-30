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

func TestDetectAudioExtension(t *testing.T) {
	// MP3 with an ID3 tag header
	if got := detectAudioExtension([]byte{'I', 'D', '3', 0x04, 0x00, 0x00}); got != ".mp3" {
		t.Fatalf("expected .mp3 for ID3 header, got %s", got)
	}
	// MP3 without an ID3 tag: raw MPEG frame sync (0xFF 0xEx)
	if got := detectAudioExtension([]byte{0xFF, 0xFB, 0x90, 0x00}); got != ".mp3" {
		t.Fatalf("expected .mp3 for MPEG frame sync, got %s", got)
	}
	// RIFF....WAVE header
	if got := detectAudioExtension([]byte("RIFF\x00\x00\x00\x00WAVE")); got != ".wav" {
		t.Fatalf("expected .wav for RIFF/WAVE header, got %s", got)
	}
	// Unknown or empty data falls back to .mp3 (the Edge TTS output format)
	if got := detectAudioExtension(nil); got != ".mp3" {
		t.Fatalf("expected .mp3 fallback, got %s", got)
	}
}

func TestDefaultVoiceIsEdgeNeural(t *testing.T) {
	// Regression guard: speech must use the Edge neural voice, never a local
	// operating-system voice.
	if defaultVoice != "en-GB-SoniaNeural" {
		t.Fatalf("expected default Edge TTS voice en-GB-SoniaNeural, got %s", defaultVoice)
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

func TestChapterSnapshotListAndLoad(t *testing.T) {
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

	chapters, err := listStorySnapshots("my_story", "bot1")
	if err != nil {
		t.Fatalf("listStorySnapshots returned error: %v", err)
	}
	if len(chapters) < 2 {
		t.Fatalf("expected at least 2 chapter snapshots, got %d: %#v", len(chapters), chapters)
	}

	loaded, err := loadChapterSnapshotByIndex("my_story", "bot1", 2)
	if err != nil {
		t.Fatalf("loadChapterSnapshotByIndex returned error: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Content != "chapter two" {
		t.Fatalf("chapter load by index returned unexpected content: %#v", loaded)
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

func TestCommandArg(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		skip int
		want string
	}{
		{"multi word chapter name", ".chapter save The Awakening of Sakura", 2, "The Awakening of Sakura"},
		{"double quoted chapter name", ".ch save \"The Awakening of Sakura\"", 2, "The Awakening of Sakura"},
		{"single quoted chapter name", ".ch save 'My Chapter'", 2, "My Chapter"},
		{"typographic quotes", ".chapter save \u201cChapter One\u201d", 2, "Chapter One"},
		{"extra surrounding whitespace trimmed", ".chapter save   Spaced  Name  ", 2, "Spaced  Name"},
		{"multi word story snapshot name", ".story save Night Run", 2, "Night Run"},
		{"quoted story snapshot name", ".story load \"Night Run\"", 2, "Night Run"},
		{"numeric argument preserved", ".chapter load 3", 2, "3"},
		{"quoted numeric argument unwrapped", ".chapter load \"3\"", 2, "3"},
		{"missing argument", ".chapter save", 2, ""},
		{"command without argument", ".chapter", 2, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commandArg(tt.raw, tt.skip); got != tt.want {
				t.Fatalf("commandArg(%q, %d) = %q, want %q", tt.raw, tt.skip, got, tt.want)
			}
		})
	}
}

func TestTrimQuotes(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"\"quoted\"", "quoted"},
		{"'quoted'", "quoted"},
		{"\u201cquoted\u201d", "quoted"},
		{"\u2018quoted\u2019", "quoted"},
		{"unquoted", "unquoted"},
		{"\"unbalanced", "\"unbalanced"},
		{"\"", "\""},
		{"\"\"", ""},
	}
	for _, tt := range tests {
		if got := trimQuotes(tt.input); got != tt.want {
			t.Errorf("trimQuotes(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMultiWordChapterNameRoundTrip(t *testing.T) {
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

	const chapterName = "The Awakening of Sakura"
	name := commandArg(".chapter save "+chapterName, 2)
	if name != chapterName {
		t.Fatalf("chapter save parsed name %q, want %q", name, chapterName)
	}

	context := []ContextMessage{{Role: "assistant", Content: "sakura awakens"}}
	if err := rewriteChatLogFile(getStorySnapshotPath("my_story", "bot1", name), context, "story"); err != nil {
		t.Fatalf("saving chapter snapshot returned error: %v", err)
	}

	chapters, err := listStorySnapshots("my_story", "bot1")
	if err != nil {
		t.Fatalf("listStorySnapshots returned error: %v", err)
	}
	if len(chapters) != 1 || chapters[0] != chapterName {
		t.Fatalf("listStorySnapshots returned %#v, want a single entry %q", chapters, chapterName)
	}

	loaded, err := loadChapterSnapshotByIndex("my_story", "bot1", 1)
	if err != nil {
		t.Fatalf("loadChapterSnapshotByIndex returned error: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Content != "sakura awakens" {
		t.Fatalf("loaded chapter content = %#v, want the saved exchange", loaded)
	}

	// A quoted name must resolve to the same snapshot file as the unquoted one.
	quoted := commandArg(".chapter save \""+chapterName+"\"", 2)
	if quoted != name {
		t.Fatalf("quoted name parsed as %q, want %q", quoted, name)
	}
	if qp, p := getStorySnapshotPath("my_story", "bot1", quoted), getStorySnapshotPath("my_story", "bot1", name); qp != p {
		t.Fatalf("quoted name resolved to %q, want %q", qp, p)
	}
}

func TestResolveStripPunct(t *testing.T) {
	tests := []struct {
		arg       string
		wantStrip bool
		wantOK    bool
	}{
		{"on", false, true},
		{"ON", false, true},
		{"on ", false, true},
		{"keep", false, true},
		{"off", true, true},
		{"Off", true, true},
		{"strip", true, true},
		{"banana", false, false},
		{"", false, false},
	}
	for _, tt := range tests {
		strip, ok := resolveStripPunct(tt.arg)
		if strip != tt.wantStrip || ok != tt.wantOK {
			t.Errorf("resolveStripPunct(%q) = (%v, %v), want (%v, %v)", tt.arg, strip, ok, tt.wantStrip, tt.wantOK)
		}
	}
}

func TestVoicePunctSettingPersists(t *testing.T) {
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

	if err := os.MkdirAll("config", 0755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	// ".voice punct off" strips punctuation (VoiceStripPunct=true) and must
	// survive a save/load round trip so the toggle sticks across restarts.
	strip, ok := resolveStripPunct("off")
	if !ok || !strip {
		t.Fatalf("resolveStripPunct(\"off\") = (%v, %v), want (true, true)", strip, ok)
	}
	saveBotParams("punctbot", BotParams{
		SelectedProvider: 0,
		CurrentModel:     "test-model",
		CurrentMode:      "chat",
		VoiceSpeed:       2,
		VoiceStripPunct:  strip,
	})
	if loaded := loadBotParams("punctbot", "test-model"); !loaded.VoiceStripPunct {
		t.Fatalf("VoiceStripPunct did not persist after \".voice punct off\": got %v, want true", loaded.VoiceStripPunct)
	}

	// ".voice punct on" keeps punctuation (VoiceStripPunct=false) and must
	// persist the opposite state.
	strip, ok = resolveStripPunct("on")
	if !ok || strip {
		t.Fatalf("resolveStripPunct(\"on\") = (%v, %v), want (false, true)", strip, ok)
	}
	saveBotParams("punctbot", BotParams{
		SelectedProvider: 0,
		CurrentModel:     "test-model",
		CurrentMode:      "chat",
		VoiceSpeed:       2,
		VoiceStripPunct:  strip,
	})
	if loaded := loadBotParams("punctbot", "test-model"); loaded.VoiceStripPunct {
		t.Fatalf("VoiceStripPunct did not persist after \".voice punct on\": got %v, want false", loaded.VoiceStripPunct)
	}
}

func TestNormalizeStoryName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"general", "general"},
		{"Night Run", "night_run"},
		{"My New Story", "my_new_story"},
		{"  spaced   name  ", "spaced_name"},
		{"Dax Dr Ren", "dax_dr_ren"},
		{"My Story: Part 2", "my_story_part_2"},
		{"trailing dots...", "trailing_dots"},
		{"", ""},
		{"   ", ""},
		{"...", ""},
	}
	for _, tt := range tests {
		if got := normalizeStoryName(tt.in); got != tt.want {
			t.Errorf("normalizeStoryName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCreateStoryFolders(t *testing.T) {
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

	// ".story create Night Run" must land in a folder-safe "night_run" story.
	name := normalizeStoryName("Night Run")
	if name != "night_run" {
		t.Fatalf("normalizeStoryName(%q) = %q, want %q", "Night Run", name, "night_run")
	}

	existed, err := createStoryFolders(name)
	if err != nil {
		t.Fatalf("createStoryFolders returned error: %v", err)
	}
	if existed {
		t.Fatalf("createStoryFolders reported the brand new story %q as already existing", name)
	}

	for _, sub := range []string{"chat", "scenes", "characters"} {
		path := filepath.Join("stories", name, sub)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
		if !info.IsDir() {
			t.Fatalf("expected %s to be a directory", path)
		}
	}

	// Creating an existing story again must be idempotent and report existence.
	existed, err = createStoryFolders(name)
	if err != nil {
		t.Fatalf("second createStoryFolders call returned error: %v", err)
	}
	if !existed {
		t.Fatalf("second createStoryFolders call reported the existing story %q as new", name)
	}

	// The new story must appear in the ".story" listing shown to the user.
	stories, err := listStoryFolders()
	if err != nil {
		t.Fatalf("listStoryFolders returned error: %v", err)
	}
	found := false
	for _, s := range stories {
		if s == name {
			found = true
		}
	}
	if !found {
		t.Fatalf("listStoryFolders() = %#v, want it to contain %q", stories, name)
	}
}

func TestLoadStoryContext(t *testing.T) {
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

	// "story_a" already has history, "story_b" is brand new.
	appendToChatLog(getChatLogPath("story_a", "bot1"), "user", "hello from a")
	appendToChatLog(getChatLogPath("story_a", "bot1"), "assistant", "reply from a")

	loaded := loadStoryContext("story_a", "bot1")
	if len(loaded) != 2 {
		t.Fatalf("loadStoryContext(story_a, bot1) returned %d messages, want 2: %#v", len(loaded), loaded)
	}
	if loaded[0].Role != "user" || loaded[0].Content != "hello from a" {
		t.Fatalf("first loaded message = %#v, want the user message saved to story_a", loaded[0])
	}
	if loaded[1].Role != "assistant" || loaded[1].Content != "reply from a" {
		t.Fatalf("second loaded message = %#v, want the assistant message saved to story_a", loaded[1])
	}

	// Switching to a story with no chat log must yield an empty context instead
	// of keeping the previous story's history in memory.
	if fresh := loadStoryContext("story_b", "bot1"); len(fresh) != 0 {
		t.Fatalf("loadStoryContext(story_b, bot1) = %#v, want an empty context", fresh)
	}
}

