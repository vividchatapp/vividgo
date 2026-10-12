package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LLMProvider is the backend abstraction every LLM backend implements.
// The webpage and Telegram both go through this — frontend stays thin.
type LLMProvider interface {
	Chat(botName, systemPrompt string, context []ContextMessage, userMessage string, numCtx int, noThink bool) (string, error)
	ListModels() ([]string, error)
	Test() error
}

func providerFor(entry ModelEntry) LLMProvider {
	if providerTypeOf(entry) == ProviderTypeGoogle {
		return &googleProvider{entry: entry}
	}
	return &ollamaProvider{entry: entry}
}

// chatWithEntry routes a chat call to the right backend based on entry.Type.
func chatWithEntry(botName string, entry ModelEntry, systemPrompt string, context []ContextMessage, userMessage string, numCtx int, noThink bool) (string, error) {
	return providerFor(entry).Chat(botName, systemPrompt, context, userMessage, numCtx, noThink)
}

// listModelsForEntry returns model ids for a provider entry (setup dropdowns).
func listModelsForEntry(entry ModelEntry) ([]string, error) {
	return providerFor(entry).ListModels()
}

// ── Ollama (native + OpenAI-compatible) ─────────────────────────────

type ollamaProvider struct{ entry ModelEntry }

func (p *ollamaProvider) Chat(botName, systemPrompt string, context []ContextMessage, userMessage string, numCtx int, noThink bool) (string, error) {
	return callOllamaAPI(botName, getEffectiveAPIBase(p.entry), p.entry, systemPrompt, context, userMessage, numCtx, noThink)
}

func (p *ollamaProvider) ListModels() ([]string, error) {
	base := getEffectiveAPIBase(p.entry)
	if models, err := fetchLocalModels(base); err == nil && len(models) > 0 {
		return models, nil
	}
	if strings.TrimSpace(p.entry.Model) != "" {
		return []string{p.entry.Model}, nil
	}
	return []string{"gemma3:4b", "llama3.2", "mistral"}, nil
}

func (p *ollamaProvider) Test() error {
	models, err := p.ListModels()
	if err != nil {
		return err
	}
	if len(models) == 0 {
		return fmt.Errorf("no models found")
	}
	return nil
}

// ── Google Gemini (OpenAI-compatible endpoint) ──────────────────────

const googleDefaultBase = "https://generativelanguage.googleapis.com/v1beta/openai"

var googleDefaultModels = []string{"gemini-2.5-flash", "gemini-2.5-pro", "gemini-2.0-flash"}

type googleProvider struct{ entry ModelEntry }

func (p *googleProvider) apiBase() string {
	base := strings.TrimRight(strings.TrimSpace(p.entry.APIBase), "/")
	if base == "" {
		return googleDefaultBase
	}
	return base
}

func (p *googleProvider) Chat(botName, systemPrompt string, context []ContextMessage, userMessage string, numCtx int, noThink bool) (string, error) {
	if strings.TrimSpace(p.entry.APIKey) == "" {
		return "", fmt.Errorf("google provider %q needs an API key (https://aistudio.google.com/apikey)", p.entry.Name)
	}
	endpoint := p.apiBase() + "/chat/completions"
	entry := p.entry
	if strings.TrimSpace(entry.Model) == "" {
		entry.Model = googleDefaultModels[0]
	}
	resp, err := callOllamaAPIOpenAI(botName, endpoint, entry, systemPrompt, context, userMessage)
	if err != nil {
		return "", fmt.Errorf("google: %w", err)
	}
	return resp, nil
}

func (p *googleProvider) ListModels() ([]string, error) {
	if strings.TrimSpace(p.entry.APIKey) != "" {
		client := &http.Client{Timeout: 15 * time.Second}
		req, err := http.NewRequest("GET", p.apiBase()+"/models", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+p.entry.APIKey)
			if resp, err := client.Do(req); err == nil {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					var out struct {
						Data []struct {
							ID string `json:"id"`
						} `json:"data"`
					}
					if json.Unmarshal(body, &out) == nil && len(out.Data) > 0 {
						var names []string
						for _, m := range out.Data {
							if m.ID != "" {
								names = append(names, m.ID)
							}
						}
						if len(names) > 0 {
							return names, nil
						}
					}
				}
			}
		}
		if names, err := p.listNativeModels(); err == nil && len(names) > 0 {
			return names, nil
		}
	}
	out := append([]string{}, googleDefaultModels...)
	if strings.TrimSpace(p.entry.Model) != "" {
		found := false
		for _, m := range out {
			if m == p.entry.Model {
				found = true
				break
			}
		}
		if !found {
			out = append([]string{p.entry.Model}, out...)
		}
	}
	return out, nil
}

func (p *googleProvider) listNativeModels() ([]string, error) {
	req, err := http.NewRequest("GET", "https://generativelanguage.googleapis.com/v1beta/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", p.entry.APIKey)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	var names []string
	for _, m := range out.Models {
		name := strings.TrimPrefix(m.Name, "models/")
		if strings.Contains(name, "gemini") || strings.Contains(name, "gemma") {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no models found")
	}
	return names, nil
}

func (p *googleProvider) Test() error {
	if strings.TrimSpace(p.entry.APIKey) == "" {
		return fmt.Errorf("API key is required for Google")
	}
	if _, err := p.ListModels(); err == nil {
		return nil
	} else {
		pingEntry := p.entry
		if strings.TrimSpace(pingEntry.Model) == "" {
			pingEntry.Model = googleDefaultModels[0]
		}
		endpoint := p.apiBase() + "/chat/completions"
		reqBody, _ := json.Marshal(map[string]interface{}{
			"model":    pingEntry.Model,
			"messages": []OllamaMessage{{Role: "user", Content: "ping"}},
		})
		req, _ := http.NewRequest("POST", endpoint, bytes.NewBuffer(reqBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+pingEntry.APIKey)
		client := &http.Client{Timeout: 30 * time.Second}
		resp, rerr := client.Do(req)
		if rerr != nil {
			return fmt.Errorf("google test failed: %v (list: %v)", rerr, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("google test failed: status %d: %s", resp.StatusCode, string(body))
		}
		return nil
	}
}
