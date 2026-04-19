package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// RegisterSkillInstall registers tools for searching and installing skills from
// ClawHub (clawhub.ai) and GitHub (skills.sh ecosystem).
// These run on the host (not in sandbox) via FastClaw's own API.
func RegisterSkillInstall(r *Registry, gatewayPort int) {
	if gatewayPort <= 0 {
		gatewayPort = 18953
	}
	base := fmt.Sprintf("http://localhost:%d", gatewayPort)

	r.Register(
		"search_skills",
		"Search for skills on ClawHub (clawhub.ai) registry. Returns available skills matching the query.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Search query (e.g. 'translation', 'code review', 'data analysis')",
				},
			},
			"required": []string{"query"},
		},
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var params struct {
				Query string `json:"query"`
			}
			json.Unmarshal(args, &params)

			resp, err := http.Get(fmt.Sprintf("%s/api/skills/search?q=%s&source=clawhub", base, params.Query))
			if err != nil {
				return "", fmt.Errorf("search failed: %w", err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)

			var result struct {
				Results []struct {
					Package struct {
						Name        string `json:"name"`
						Summary     string `json:"summary"`
						DisplayName string `json:"displayName"`
					} `json:"package"`
				} `json:"results"`
			}
			if json.Unmarshal(body, &result) == nil && len(result.Results) > 0 {
				var lines []string
				for _, r := range result.Results {
					name := r.Package.Name
					desc := r.Package.Summary
					if desc == "" {
						desc = r.Package.DisplayName
					}
					lines = append(lines, fmt.Sprintf("- %s: %s", name, desc))
				}
				return fmt.Sprintf("Found %d skills:\n%s", len(result.Results), strings.Join(lines, "\n")), nil
			}
			return string(body), nil
		},
	)

	r.Register(
		"install_skill",
		"Install a skill from ClawHub or GitHub to the global skills directory (available to all agents).",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{
					"type":        "string",
					"description": "Skill name/slug for ClawHub, or GitHub repo (owner/repo) for GitHub",
				},
				"source": map[string]interface{}{
					"type":        "string",
					"description": "Source: 'clawhub' (default) or 'github'",
				},
			},
			"required": []string{"name"},
		},
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var params struct {
				Name   string `json:"name"`
				Source string `json:"source"`
			}
			json.Unmarshal(args, &params)

			if params.Source == "" {
				if strings.Contains(params.Name, "/") {
					params.Source = "github"
				} else {
					params.Source = "clawhub"
				}
			}

			var reqBody map[string]string
			if params.Source == "github" {
				reqBody = map[string]string{"source": "github", "repo": params.Name}
			} else {
				reqBody = map[string]string{"source": "clawhub", "skill": params.Name}
			}

			bodyBytes, _ := json.Marshal(reqBody)
			resp, err := http.Post(fmt.Sprintf("%s/api/skills/install", base), "application/json", strings.NewReader(string(bodyBytes)))
			if err != nil {
				return "", fmt.Errorf("install failed: %w", err)
			}
			defer resp.Body.Close()
			result, _ := io.ReadAll(resp.Body)

			var res struct {
				OK      bool   `json:"ok"`
				Name    string `json:"name"`
				Version string `json:"version"`
				Error   string `json:"error"`
			}
			json.Unmarshal(result, &res)

			if res.OK {
				msg := fmt.Sprintf("Successfully installed skill '%s'", res.Name)
				if res.Version != "" {
					msg += fmt.Sprintf(" (version %s)", res.Version)
				}
				msg += " to the global skills directory. All agents can now use it."
				return msg, nil
			}
			return "", fmt.Errorf("install failed: %s", res.Error)
		},
	)
}
