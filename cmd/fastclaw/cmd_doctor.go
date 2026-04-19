package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// doctorCmd checks the config and environment for issues.
func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check FastClaw configuration and environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor()
		},
	}
}

func runDoctor() error {
	fmt.Println("🩺 FastClaw Doctor")
	fmt.Println(strings.Repeat("─", 40))
	issues := 0
	warnings := 0

	// 1. Version
	fmt.Printf("\n📌 Version: %s (%s)\n", version, commit)

	// 2. Config file
	_ = config.MigrateLegacyLayout()
	userDir, err := config.UserDir(config.DefaultUserID)
	if err != nil {
		fmt.Printf("❌ Config dir: cannot determine (%v)\n", err)
		issues++
	} else {
		fmt.Printf("📂 Config dir: %s\n", userDir)

		configPath := filepath.Join(userDir, "fastclaw.json")
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			fmt.Printf("❌ Config file: not found (%s)\n", configPath)
			fmt.Println("   → Run 'fastclaw' to start the setup wizard")
			issues++
		} else {
			fmt.Printf("✅ Config file: %s\n", configPath)

			// Try loading
			cfg, err := config.Load()
			if err != nil {
				fmt.Printf("❌ Config parse: %v\n", err)
				issues++
			} else {
				fmt.Printf("✅ Config parsed OK\n")

				// 3. Check providers
				fmt.Println()
				if len(cfg.Providers) == 0 {
					fmt.Println("❌ Providers: none configured")
					issues++
				} else {
					for name, prov := range cfg.Providers {
						if prov.APIKey == "" {
							fmt.Printf("⚠️  Provider '%s': no API key\n", name)
							warnings++
						} else {
							masked := prov.APIKey[:4] + "..." + prov.APIKey[len(prov.APIKey)-4:]
							base := prov.APIBase
							if base == "" {
								base = "https://api.openai.com/v1"
							}
							fmt.Printf("✅ Provider '%s': %s (key: %s)\n", name, base, masked)
						}
					}
				}

				// 4. Test LLM connection
				fmt.Println()
				fmt.Println("🔌 Testing LLM connection...")
				if testLLMConnection(cfg) {
					fmt.Println("✅ LLM connection: OK")
				} else {
					fmt.Println("❌ LLM connection: failed")
					issues++
				}

				// 5. Check agents
				fmt.Println()
				resolved := config.ResolveAgents(cfg)
				if len(resolved) == 0 {
					fmt.Println("⚠️  Agents: none configured")
					warnings++
				} else {
					for _, rc := range resolved {
						homeExists := false
						if _, err := os.Stat(rc.Home); err == nil {
							homeExists = true
						}
						if homeExists {
							// Check home files
							missing := []string{}
							for _, f := range []string{"SOUL.md", "IDENTITY.md"} {
								if _, err := os.Stat(filepath.Join(rc.Home, f)); err != nil {
									missing = append(missing, f)
								}
							}
							if len(missing) > 0 {
								fmt.Printf("⚠️  Agent '%s': home OK, missing %s\n", rc.ID, strings.Join(missing, ", "))
								warnings++
							} else {
								fmt.Printf("✅ Agent '%s': model=%s, home=%s, workspace=%s\n", rc.ID, rc.Model, rc.Home, rc.Workspace)
							}
						} else {
							fmt.Printf("❌ Agent '%s': home not found (%s)\n", rc.ID, rc.Home)
							issues++
						}
					}
				}

				// 6. Check channels
				fmt.Println()
				if len(cfg.Channels) == 0 {
					fmt.Println("⚠️  Channels: none configured")
					warnings++
				} else {
					for name, ch := range cfg.Channels {
						if !ch.Enabled {
							fmt.Printf("⏸️  Channel '%s': disabled\n", name)
							continue
						}
						tokenCount := 0
						if ch.BotToken != "" {
							tokenCount++
						}
						for _, acct := range ch.Accounts {
							if acct.BotToken != "" {
								tokenCount++
							}
						}
						if tokenCount > 0 {
							fmt.Printf("✅ Channel '%s': enabled, %d bot(s)\n", name, tokenCount)
						} else {
							fmt.Printf("❌ Channel '%s': enabled but no bot tokens\n", name)
							issues++
						}
					}
				}

				// 7. Check bindings
				fmt.Println()
				if len(cfg.Bindings) == 0 {
					fmt.Println("⚠️  Bindings: none configured (messages won't route to agents)")
					warnings++
				} else {
					fmt.Printf("✅ Bindings: %d rule(s)\n", len(cfg.Bindings))
				}
			}
		}
	}

	// Summary
	fmt.Println()
	fmt.Println(strings.Repeat("─", 40))
	if issues == 0 && warnings == 0 {
		fmt.Println("🎉 Everything looks good!")
	} else if issues == 0 {
		fmt.Printf("✅ No errors, %d warning(s)\n", warnings)
	} else {
		fmt.Printf("🔴 %d error(s), %d warning(s)\n", issues, warnings)
	}

	return nil
}

func testLLMConnection(cfg *config.Config) bool {
	var provCfg config.ProviderConfig
	for _, key := range []string{"default", "openai", "openrouter"} {
		if p, ok := cfg.Providers[key]; ok {
			provCfg = p
			break
		}
	}
	if provCfg.APIKey == "" {
		for _, p := range cfg.Providers {
			provCfg = p
			break
		}
	}
	if provCfg.APIKey == "" {
		return false
	}

	apiBase := provCfg.APIBase
	if apiBase == "" {
		apiBase = "https://api.openai.com/v1"
	}
	apiBase = strings.TrimRight(apiBase, "/")

	// Quick models list check
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", apiBase+"/models", nil)
	req.Header.Set("Authorization", "Bearer "+provCfg.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("   (error: %v)\n", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true
	}
	fmt.Printf("   (HTTP %d)\n", resp.StatusCode)
	return resp.StatusCode < 500 // 401/403 means API works but key is bad; 5xx means endpoint down
}
