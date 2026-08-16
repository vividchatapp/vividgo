package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Global flag: set to true to disable all ANSI color output
var noColor bool

// ANSI color codes for styled output
// These are populated at init time based on terminal support
var (
	colorReset  = ""
	colorBold   = ""
	colorCyan   = ""
	colorGreen  = ""
	colorYellow = ""
	colorRed    = ""
	colorDim    = ""
)

// setColorCodes populates the ANSI color constants
func setColorCodes() {
	colorReset = "\033[0m"
	colorBold = "\033[1m"
	colorCyan = "\033[36m"
	colorGreen = "\033[32m"
	colorYellow = "\033[33m"
	colorRed = "\033[31m"
	colorDim = "\033[2m"
}

// readLine prints a prompt and reads a line of input from stdin.
// Returns the trimmed input.
func readLine(prompt, defaultValue string) string {
	if defaultValue != "" {
		fmt.Printf("%s%s%s [%s%s%s]: ", colorCyan, prompt, colorReset, colorYellow, defaultValue, colorReset)
	} else {
		fmt.Printf("%s%s%s: ", colorCyan, prompt, colorReset)
	}

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return defaultValue
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return defaultValue
	}
	return line
}

// readLinePassword prints a prompt and reads input without echoing the default.
// Returns the trimmed input or the default (if blank).
func readLinePassword(prompt, defaultValue string) string {
	masked := ""
	if defaultValue != "" {
		masked = maskKey(defaultValue)
	}
	fmt.Printf("%s%s%s [%s%s%s]: ", colorCyan, prompt, colorReset, colorYellow, masked, colorReset)

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return defaultValue
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return defaultValue
	}
	return line
}

// printBanner prints the styled welcome banner
func printBanner() {
	fmt.Println()
	fmt.Printf("%s╔══════════════════════════════════════════════════╗%s\n", colorCyan, colorReset)
	fmt.Printf("%s║            %s🤖 VividGo Setup Wizard%s%s             ║%s\n", colorCyan, colorBold, colorReset, colorCyan, colorReset)
	fmt.Printf("%s╠══════════════════════════════════════════════════╣%s\n", colorCyan, colorReset)
	fmt.Printf("%s║  This wizard will help you configure your        ║%s\n", colorCyan, colorReset)
	fmt.Printf("%s║  Telegram AI bot. You'll need a few things:      ║%s\n", colorCyan, colorReset)
	fmt.Printf("%s║                                                  ║%s\n", colorCyan, colorReset)
	fmt.Printf("%s║  %s•%s Telegram Bot Token (from @BotFather)          %s║%s\n", colorCyan, colorYellow, colorReset, colorCyan, colorReset)
	fmt.Printf("%s║  %s•%s Your Telegram User ID (from @userinfobot)     %s║%s\n", colorCyan, colorYellow, colorReset, colorCyan, colorReset)
	fmt.Printf("%s║  %s•%s Ollama API Key (ollama.com/settings/keys)     %s║%s\n", colorCyan, colorYellow, colorReset, colorCyan, colorReset)
	fmt.Printf("%s╚══════════════════════════════════════════════════╝%s\n", colorCyan, colorReset)
	fmt.Println()
}

// printSection prints a section header
func printSection(title string) {
	fmt.Println()
	fmt.Printf("%s━━━ %s%s%s ━━━%s\n", colorBold, colorGreen, title, colorReset, colorBold)
	fmt.Println()
}

// printStep prints a step indicator
func printStep(step, total int, description string) {
	fmt.Printf("  %s[%d/%d]%s %s%s%s\n", colorDim, step, total, colorReset, colorYellow, description, colorReset)
	fmt.Println()
}

// OllamaLocalModel represents a model from a local Ollama /api/tags response
type OllamaLocalModel struct {
	Name string `json:"name"`
}

// OllamaLocalTagsResponse represents the response from Ollama /api/tags
type OllamaLocalTagsResponse struct {
	Models []OllamaLocalModel `json:"models"`
}

// fetchLocalModels calls {apiBase}/api/tags to list models from a local Ollama instance.
// Returns a sorted list of model names, or an error if the request fails.
func fetchLocalModels(apiBase string) ([]string, error) {
	baseURL := strings.TrimRight(apiBase, "/")
	endpoint := baseURL + "/api/tags"

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var tagsResp OllamaLocalTagsResponse
	if err := json.Unmarshal(body, &tagsResp); err != nil {
		return nil, fmt.Errorf("failed to parse models: %w", err)
	}

	if len(tagsResp.Models) == 0 {
		return nil, fmt.Errorf("no models found at %s", apiBase)
	}

	var modelNames []string
	for _, m := range tagsResp.Models {
		modelNames = append(modelNames, m.Name)
	}
	sort.Strings(modelNames)
	return modelNames, nil
}

// GitHubContentEntry represents a file entry from the GitHub API
type GitHubContentEntry struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	DownloadURL string `json:"download_url"`
}

// downloadRolesFromGitHub fetches role files from the vivid-data GitHub repo
// and saves them to the local roles/ directory if they don't already exist.
func downloadRolesFromGitHub() {
	fmt.Println()
	fmt.Printf("%s━━━ Downloading Roles from GitHub %s━━━%s\n", colorBold, colorGreen, colorReset)
	fmt.Println()

	// URL to list the contents of the roles directory in the vivid-data repo
	apiURL := "https://api.github.com/repos/vividchatapp/vivid-data/contents/roles"

	client := &http.Client{}
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		fmt.Printf("  %s✗ Failed to create request: %v%s\n", colorRed, err, colorReset)
		return
	}

	// Add a User-Agent header as required by GitHub API
	req.Header.Set("User-Agent", "VividGo-Setup/1.0")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("  %s✗ Failed to fetch roles list: %v%s\n", colorRed, err, colorReset)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("  %s✗ GitHub API returned status %d: %s%s\n", colorRed, resp.StatusCode, string(body), colorReset)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("  %s✗ Failed to read response: %v%s\n", colorRed, err, colorReset)
		return
	}

	var entries []GitHubContentEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		fmt.Printf("  %s✗ Failed to parse GitHub response: %v%s\n", colorRed, err, colorReset)
		return
	}

	downloaded := 0
	skipped := 0

	for _, entry := range entries {
		// Only process .txt files
		if entry.Type != "file" || !strings.HasSuffix(entry.Name, ".txt") {
			continue
		}

		localPath := fmt.Sprintf("roles/%s", entry.Name)

		// Check if the file already exists locally
		if _, err := os.Stat(localPath); err == nil {
			fmt.Printf("  %s• %s already exists (skipped)%s\n", colorDim, entry.Name, colorReset)
			skipped++
			continue
		}

		// Download the raw file
		fmt.Printf("  %s↓ Downloading %s...%s", colorYellow, entry.Name, colorReset)

		fileResp, err := client.Get(entry.DownloadURL)
		if err != nil {
			fmt.Printf(" %s✗ failed: %v%s\n", colorRed, err, colorReset)
			continue
		}

		fileContent, err := io.ReadAll(fileResp.Body)
		fileResp.Body.Close()
		if err != nil {
			fmt.Printf(" %s✗ failed to read: %v%s\n", colorRed, err, colorReset)
			continue
		}

		if err := os.WriteFile(localPath, fileContent, 0644); err != nil {
			fmt.Printf(" %s✗ failed to save: %v%s\n", colorRed, err, colorReset)
			continue
		}

		fmt.Printf(" %s✓%s\n", colorGreen, colorReset)
		downloaded++
	}

	fmt.Println()
	fmt.Printf("  %s✓ Downloaded %d new role(s), skipped %d existing%s\n", colorGreen, downloaded, skipped, colorReset)
}

// runSetupWizard runs the interactive configuration setup.
// If existingCfg is non-nil, its values are used as defaults (for --init re-run).
// Returns a populated Config and whether to continue (false means user aborted).
func runSetupWizard(existingCfg *Config) *Config {
	printBanner()

	// Extract defaults from existing config
	var defaultBots []BotConfig
	var defaultUserID int64
	var defaultDebug bool
	var defaultAPIBase string
	var defaultModels []ModelEntry

	if existingCfg != nil {
		defaultBots = existingCfg.Bots
		defaultUserID = existingCfg.UserID
		defaultDebug = existingCfg.Debug
		defaultAPIBase = existingCfg.Ollama.APIBase
		defaultModels = existingCfg.Ollama.Models
	}

	if defaultAPIBase == "" {
		defaultAPIBase = "https://api.ollama.com"
	}

	totalSteps := 5

	// ── Step 1: Telegram User ID ──
	printSection("Telegram Configuration")
	printStep(1, totalSteps, "Telegram User ID")

	fmt.Printf("  %sFind your user ID by messaging @userinfobot on Telegram.%s\n", colorDim, colorReset)
	fmt.Println()

	var defaultUIDStr string
	if defaultUserID != 0 {
		defaultUIDStr = strconv.FormatInt(defaultUserID, 10)
	}

	uidInput := readLine("Enter your Telegram User ID", defaultUIDStr)
	if uidInput == "" {
		fmt.Printf("\n  %s✗ User ID is required. Aborting.%s\n", colorRed, colorReset)
		return nil
	}

	userID, err := strconv.ParseInt(uidInput, 10, 64)
	if err != nil {
		fmt.Printf("\n  %s✗ Invalid user ID '%s'. Must be a numeric ID. Aborting.%s\n", colorRed, uidInput, colorReset)
		return nil
	}

	// ── Step 2: Bot Tokens (loop) ──
	printStep(2, totalSteps, "Telegram Bot Tokens")

	fmt.Printf("  %sGet your bot token from @BotFather (%s/newbot) on Telegram.%s\n", colorDim, colorReset, colorReset)
	fmt.Printf("  %sEnter a name and token for each bot. Leave token blank to finish.%s\n", colorDim, colorReset)
	fmt.Println()

	var botCfgs []BotConfig
	botIndex := 0

	for {
		botIndex++
		defaultName := fmt.Sprintf("vivid%d", botIndex)

		// Pre-fill name from existing config if available
		if botIndex-1 < len(defaultBots) && defaultBots[botIndex-1].Name != "" {
			defaultName = defaultBots[botIndex-1].Name
		}

		// Prompt for bot name
		namePrompt := fmt.Sprintf("Bot %d name", botIndex)
		botName := readLine(namePrompt, defaultName)
		if botName == "" {
			botName = defaultName
		}

		// Prompt for bot token
		var defaultToken string
		if botIndex-1 < len(defaultBots) {
			defaultToken = defaultBots[botIndex-1].Token
		}

		tokenPrompt := fmt.Sprintf("Bot %d token (blank to finish)", botIndex)
		botToken := readLinePassword(tokenPrompt, defaultToken)

		if botToken == "" {
			if botIndex == 1 {
				// First bot token is required
				fmt.Printf("\n  %s✗ At least one bot token is required. Aborting.%s\n", colorRed, colorReset)
				return nil
			}
			// Subsequent bots: blank means done
			fmt.Printf("  %sNo more bots configured.%s\n", colorDim, colorReset)
			break
		}

		botCfgs = append(botCfgs, BotConfig{Name: botName, Token: botToken})
		fmt.Printf("  %s✓ Bot '%s' configured%s\n", colorGreen, botName, colorReset)
	}

	if len(botCfgs) == 0 {
		fmt.Printf("\n  %s✗ At least one valid bot token is required. Aborting.%s\n", colorRed, colorReset)
		return nil
	}

	fmt.Printf("  %s✓ Configured %d bot(s)%s\n", colorGreen, len(botCfgs), colorReset)

	// ── Step 3: Debug mode ──
	printStep(3, totalSteps, "Debug Mode")

	defaultDebugStr := "n"
	if defaultDebug {
		defaultDebugStr = "y"
	}

	debugInput := readLine("Enable debug mode? (y/N)", defaultDebugStr)
	debug := strings.ToLower(debugInput) == "y" || strings.ToLower(debugInput) == "yes"
	if debug {
		fmt.Printf("  %s✓ Debug mode enabled%s\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  %sDebug mode disabled%s\n", colorDim, colorReset)
	}

	// ── Step 4: Ollama API Base URL ──
	printSection("Ollama API Configuration")
	printStep(4, totalSteps, "Ollama API Base URL")

	apiBase := readLine("Ollama API Base URL", defaultAPIBase)
	if apiBase == "" {
		apiBase = "https://api.ollama.com"
	}
	fmt.Printf("  %s✓ API Base: %s%s\n", colorGreen, apiBase, colorReset)

	// ── Step 5: Ollama API Keys (loop) ──
	printStep(5, totalSteps, "Ollama API Keys")

	fmt.Printf("  %sGet your API key from https://ollama.com/settings/keys%s\n", colorDim, colorReset)
	fmt.Printf("  %sEnter a name and API key for each provider. Leave API key blank to finish.%s\n", colorDim, colorReset)
	fmt.Println()

	var models []ModelEntry
	providerIndex := 0

	for {
		providerIndex++
		defaultProviderName := fmt.Sprintf("provider%d", providerIndex)
		if providerIndex == 1 {
			defaultProviderName = "primary"
		}

		// Pre-fill name from existing config if available
		if providerIndex-1 < len(defaultModels) && defaultModels[providerIndex-1].Name != "" {
			defaultProviderName = defaultModels[providerIndex-1].Name
		}

		// Prompt for provider name
		namePrompt := fmt.Sprintf("Provider %d name", providerIndex)
		providerName := readLine(namePrompt, defaultProviderName)
		if providerName == "" {
			providerName = defaultProviderName
		}

		// Prompt for API key
		var defaultAPIKey string
		if providerIndex-1 < len(defaultModels) {
			defaultAPIKey = defaultModels[providerIndex-1].APIKey
		}

		keyPrompt := fmt.Sprintf("Provider %d API key (blank to finish)", providerIndex)
		apiKey := readLinePassword(keyPrompt, defaultAPIKey)

		if apiKey == "" {
			if providerIndex == 1 {
				// First API key is required
				fmt.Printf("\n  %s✗ At least one API key is required. Aborting.%s\n", colorRed, colorReset)
				return nil
			}
			// Subsequent providers: blank means done
			fmt.Printf("  %sNo more providers configured.%s\n", colorDim, colorReset)
			break
		}

		// Prompt for API base URL for this provider
		var defaultProviderAPIBase string
		if providerIndex-1 < len(defaultModels) {
			defaultProviderAPIBase = defaultModels[providerIndex-1].APIBase
		}
		if defaultProviderAPIBase == "" {
			defaultProviderAPIBase = apiBase
		}

		apiBasePrompt := fmt.Sprintf("API Base URL for '%s'", providerName)
		providerAPIBase := readLine(apiBasePrompt, defaultProviderAPIBase)
		if providerAPIBase == "" {
			providerAPIBase = defaultProviderAPIBase
		}

		// Try to auto-discover models from this API base URL
		var discoveredModels []string
		var fetchErr error
		discoveredModels, fetchErr = fetchLocalModels(providerAPIBase)
		var modelName string

		if fetchErr == nil && len(discoveredModels) > 0 {
			fmt.Printf("  %s✓ Discovered %d model(s) from %s%s\n", colorGreen, len(discoveredModels), providerAPIBase, colorReset)
			fmt.Printf("  %sAvailable models:%s\n", colorYellow, colorReset)
			for i, m := range discoveredModels {
				fmt.Printf("    %d) %s\n", i+1, m)
			}
			fmt.Println()

			// Use first model as default
			defaultModelName := discoveredModels[0]
			// Check if there's an existing model name to use as default
			if providerIndex-1 < len(defaultModels) && defaultModels[providerIndex-1].Model != "" {
				defaultModelName = defaultModels[providerIndex-1].Model
			}

			modelPrompt := fmt.Sprintf("Select model number or enter custom name for '%s'", providerName)
			modelInput := readLine(modelPrompt, defaultModelName)

			// Try parsing as number first
			if num, err := strconv.Atoi(modelInput); err == nil && num >= 1 && num <= len(discoveredModels) {
				modelName = discoveredModels[num-1]
			} else if modelInput == "" {
				modelName = defaultModelName
			} else {
				modelName = modelInput
			}
		} else {
			if fetchErr != nil {
				fmt.Printf("  %sNote: Could not auto-discover models: %v%s\n", colorDim, fetchErr, colorReset)
			}

			// Prompt for model name manually
			var defaultModelName string
			if providerIndex-1 < len(defaultModels) {
				defaultModelName = defaultModels[providerIndex-1].Model
			}
			if defaultModelName == "" {
				defaultModelName = "gemma4:31b"
			}

			modelPrompt := fmt.Sprintf("Model name for '%s'", providerName)
			modelName = readLine(modelPrompt, defaultModelName)
			if modelName == "" {
				modelName = "gemma4:31b"
			}
		}

		models = append(models, ModelEntry{
			Name:    providerName,
			APIKey:  apiKey,
			Model:   modelName,
			APIBase: providerAPIBase,
		})
		fmt.Printf("  %s✓ Provider '%s' configured (model: %s, api_base: %s)%s\n", colorGreen, providerName, modelName, providerAPIBase, colorReset)
	}

	if len(models) == 0 {
		fmt.Printf("\n  %s✗ At least one API key is required. Aborting.%s\n", colorRed, colorReset)
		return nil
	}

	fmt.Printf("  %s✓ Configured %d provider(s)%s\n", colorGreen, len(models), colorReset)

	// ── Build the config ──
	cfg := &Config{
		Bots:   botCfgs,
		UserID: userID,
		Debug:  debug,
		Ollama: OllamaConfig{
			APIBase: apiBase,
			Models:  models,
		},
	}

	return cfg
}

// writeConfigFile writes the config.yaml file with inline comments.
func writeConfigFile(cfg *Config) error {
	fmt.Println()
	fmt.Printf("%s━━━ Writing Configuration %s━━━%s\n", colorBold, colorGreen, colorReset)
	fmt.Println()

	// Build the config file content manually for better readability and comments
	var sb strings.Builder

	sb.WriteString("# Telegram Bot Configuration\n")
	sb.WriteString("# Each bot has a name, token, and authorized user ID\n")
	sb.WriteString("bots:\n")
	for _, bot := range cfg.Bots {
		sb.WriteString(fmt.Sprintf("  - name: %q\n", bot.Name))
		sb.WriteString(fmt.Sprintf("    token: %q\n", bot.Token))
	}
	sb.WriteString(fmt.Sprintf("user_id: %d\n", cfg.UserID))
	sb.WriteString(fmt.Sprintf("debug: %t\n", cfg.Debug))
	sb.WriteString("\n")
	sb.WriteString("# Ollama API Configuration\n")
	sb.WriteString("# Used to talk to ollama.com for online models\n")
	sb.WriteString("ollama:\n")
	sb.WriteString(fmt.Sprintf("  api_base: %q  # ollama.com API endpoint\n", cfg.Ollama.APIBase))
	sb.WriteString("  models:\n")
	for _, model := range cfg.Ollama.Models {
		sb.WriteString(fmt.Sprintf("    - name: %q\n", model.Name))
		sb.WriteString(fmt.Sprintf("      api_key: %q  # Your ollama.com API key\n", model.APIKey))
		sb.WriteString(fmt.Sprintf("      model: %q  # Model to use (e.g., llama3.2, mistral, etc.)\n", model.Model))
		if model.APIBase != "" {
			sb.WriteString(fmt.Sprintf("      api_base: %q  # API base URL for this model\n", model.APIBase))
		}
	}

	data := []byte(sb.String())
	if err := os.WriteFile("config.yaml", data, 0644); err != nil {
		return fmt.Errorf("failed to write config.yaml: %w", err)
	}

	fmt.Printf("  %s✓ config.yaml written successfully%s\n", colorGreen, colorReset)
	return nil
}

// ensureDirectoryStructure creates the required directories and default role files.
func ensureDirectoryStructure() error {
	fmt.Println()
	fmt.Printf("%s━━━ Creating Directory Structure %s━━━%s\n", colorBold, colorGreen, colorReset)
	fmt.Println()

	dirs := []string{
		"config",
		"roles",
		"stories",
		"stories/general",
		"stories/general/chat",
		"stories/general/scenes",
		"stories/general/characters",
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
		fmt.Printf("  %s✓ %s/%s\n", colorGreen, dir, colorReset)
	}

	// Write default role files if they don't exist
	defaultRoles := map[string]string{
		"Assistant": `You are a highly attentive, thoughtful, and efficient AI assistant.
Your core goal is to deliver clear, practical, technically accurate, and respectful guidance while staying concise and meaningful. You avoid filler and get straight to the point, but never at the expense of clarity or emotional intelligence.

Key Traits:

- Thoughtful & Attentive: You listen carefully and ask relevant follow-up questions when discussing personal, relational, or sensitive topics.
- Mature & Non-Judgmental: You are comfortable discussing sexual health, relationships, intimacy, communication, consent, confidence, and emotional connection in a grounded, respectful way. You focus on real-world, helpful advice rather than explicit or graphic descriptions.
- Efficient & Direct: You prioritize clarity and usefulness. For technical tasks (especially code, Ollama, or local LLMs), you provide clean, well-documented solutions with emphasis on performance and resource efficiency.
- Tone: Calm, grounded, and slightly witty when appropriate. You are honest and direct without being blunt or cold.
- Reasoning Style: For complex tasks, you think step-by-step internally before delivering a polished final response.

You balance depth with brevity — offering meaningful insight without unnecessary length. You adapt naturally: empathetic and supportive on human/relational topics, precise and technical on practical or coding tasks.`,
		"StoryWriter": `# Role: Story Weaver (Immersive Adventure & Character Narrator)

You are Story Weaver, an AI collaborator that acts as an immersive storyteller, multi-character narrator, and world-builder. Your purpose is to take the user's prompt as the immediate continuation of a story, embellish it with cinematic detail, and progress the narrative.

## Core Directives (Strict Enforcement)
1. **Never Break Character:** Do not include introductions, out-of-character (OOC) commentary, meta-talk, or explanations.
2. **Never Prompt the User:** Do not ask the user what happens next, do not offer choices, and do not ask questions. End your response naturally with the final sentence of the narrative.
3. **User Character Autonomy:** Never speak, write dialogue, or make decisions for the user's main character unless explicitly invited. Focus entirely on NPCs, enemies, the environment, atmospheric reactions, and consequences.

## Writing Style & Genre Balance
- **Genre:** Epic Action, High-Stakes Adventure, Thrilling Discovery, and Intense Character Dynamics.
- **Tone:** Gritty yet vibrant, atmospheric, and emotionally resonant. Seamlessly balance the adrenaline of danger with quiet, meaningful character moments.
- **Pacing & Action:** Write kinetic, visceral combat and action scenes. Focus on momentum, environmental interaction, tactical tension, and the physical toll of adventure.
- **Sensory Details:** Emphasize the environment—the ring of clashing steel, the smell of ozone or smoke, the sting of adrenaline, the roar of a storm, or the heavy silence of an unexplored ruin.
- **Dialogue & Bonds:** Craft sharp, natural dialogue for NPCs. Highlight the growing trust, fierce loyalty, or gripping tension forged through shared danger.

## Response Formatting Rules
- **Perspective:** Write exclusively in close third-person limited perspective, centered on the user's character.
- **Dialogue:** Use standard quotation marks ("...") for spoken words.
- **Internal Thoughts:** Use *italics* for the internal thoughts of NPCs to deepen the stakes and emotional weight.
- **Scene Breaks:** Use '---' only for significant scene transitions or time jumps.

## Execution Instruction
The user will provide a prompt containing actions, plot points, or ideas. Immediately absorb their input as the absolute reality of the current moment, embellish it into a vivid, cinematic narrative, and hand the story back to them seamlessly.`,
	}

	for name, content := range defaultRoles {
		path := fmt.Sprintf("roles/%s.txt", name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return fmt.Errorf("failed to write role file %s: %w", path, err)
			}
			fmt.Printf("  %s✓ roles/%s.txt created%s\n", colorGreen, name, colorReset)
		} else {
			fmt.Printf("  %s• roles/%s.txt already exists (skipped)%s\n", colorDim, name, colorReset)
		}
	}

	// Write README for stories directory
	storiesReadme := "# Stories\n\nThis directory contains story folders for the Story Writer mode.\nEach folder can contain:\n- `chat/` - Chat history logs\n- `scenes/` - Scene description files\n- `characters/` - Character bio files\n\nThe default story is `general`.\n"
	storiesReadmePath := "stories/README.md"
	if _, err := os.Stat(storiesReadmePath); os.IsNotExist(err) {
		if err := os.WriteFile(storiesReadmePath, []byte(storiesReadme), 0644); err != nil {
			return fmt.Errorf("failed to write stories README: %w", err)
		}
		fmt.Printf("  %s✓ stories/README.md created%s\n", colorGreen, colorReset)
	}

	fmt.Println()
	fmt.Printf("  %s✓ Directory structure ready%s\n", colorGreen, colorReset)
	return nil
}

// printSummary prints a summary of the configured values
func printSummary(cfg *Config) {
	fmt.Println()
	fmt.Printf("%s━━━ Configuration Summary %s━━━%s\n", colorBold, colorGreen, colorReset)
	fmt.Println()
	fmt.Printf("  %sUser ID:%s %d\n", colorYellow, colorReset, cfg.UserID)
	fmt.Printf("  %sBots:%s\n", colorYellow, colorReset)
	for _, bot := range cfg.Bots {
		fmt.Printf("    - %s: %s\n", bot.Name, maskKey(bot.Token))
	}
	fmt.Printf("  %sDebug:%s %t\n", colorYellow, colorReset, cfg.Debug)
	fmt.Printf("  %sOllama API Base:%s %s\n", colorYellow, colorReset, cfg.Ollama.APIBase)
	fmt.Printf("  %sProviders:%s\n", colorYellow, colorReset)
	for _, model := range cfg.Ollama.Models {
		apiBaseDisplay := model.APIBase
		if apiBaseDisplay == "" {
			apiBaseDisplay = cfg.Ollama.APIBase
		}
		fmt.Printf("    - %s: %s (api_base: %s, key: %s)\n", model.Name, model.Model, apiBaseDisplay, maskKey(model.APIKey))
	}
	fmt.Println()
}

// confirmYesNo asks for a yes/no confirmation.
func confirmYesNo(prompt string, defaultYes bool) bool {
	var defaultStr string
	if defaultYes {
		defaultStr = "Y/n"
	} else {
		defaultStr = "y/N"
	}
	fmt.Printf("%s%s%s [%s] ", colorCyan, prompt, colorReset, defaultStr)

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return defaultYes
	}
	line = strings.TrimRight(line, "\r\n")
	lower := strings.ToLower(line)
	if lower == "" {
		return defaultYes
	}
	return lower == "y" || lower == "yes"
}

// initializeSetup runs the full setup workflow: wizard, confirm, write, scaffold.
// Returns the configured Config or nil if aborted.
func initializeSetup(existingCfg *Config) *Config {
	// Initialize color support (respects the noColor flag)
	initColors()

	cfg := runSetupWizard(existingCfg)
	if cfg == nil {
		fmt.Println()
		fmt.Printf("  %sSetup aborted.%s\n", colorRed, colorReset)
		return nil
	}

	printSummary(cfg)

	if !confirmYesNo("Write this configuration and continue?", true) {
		fmt.Println()
		fmt.Printf("  %sSetup cancelled.%s\n", colorRed, colorReset)
		return nil
	}

	if err := writeConfigFile(cfg); err != nil {
		fmt.Printf("\n  %s✗ Failed to write config: %v%s\n", colorRed, err, colorReset)
		return nil
	}

	if err := ensureDirectoryStructure(); err != nil {
		fmt.Printf("\n  %s✗ Failed to create directories: %v%s\n", colorRed, err, colorReset)
		return nil
	}

	// Ask if user wants to download roles from GitHub
	if confirmYesNo("Download roles from GitHub?", false) {
		downloadRolesFromGitHub()
	} else {
		fmt.Printf("  %s• Skipped downloading roles from GitHub%s\n", colorDim, colorReset)
	}

	fmt.Println()
	fmt.Printf("%s╔══════════════════════════════════════════════════╗%s\n", colorGreen, colorReset)
	fmt.Printf("%s║  %s✓ Setup complete! Starting bots...%s%s               ║%s\n", colorGreen, colorBold, colorReset, colorGreen, colorReset)
	fmt.Printf("%s╚══════════════════════════════════════════════════╝%s\n", colorGreen, colorReset)
	fmt.Println()

	return cfg
}
