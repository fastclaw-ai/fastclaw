package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/plugin"
)

const hubRepo = "fastclaw-ai/fastclaw"

// pluginCmd handles plugin management subcommands.
func pluginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "plugins",
		Aliases: []string{"plugin"},
		Short:   "Manage plugins",
	}
	cmd.AddCommand(pluginListCmd())
	cmd.AddCommand(pluginInstallCmd())
	cmd.AddCommand(pluginRemoveCmd())
	return cmd
}

func pluginListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List discovered plugins and their status",
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, err := config.HomeDir()
			if err != nil {
				return err
			}

			paths := []string{filepath.Join(homeDir, "plugins")}

			// Also check config for extra paths
			cfg, _ := config.Load()
			if cfg != nil && cfg.Plugins.Enabled {
				for _, p := range cfg.Plugins.Paths {
					paths = append(paths, p)
				}
			}

			mgr := plugin.NewManager(nil)
			if err := mgr.Discover(paths); err != nil {
				return err
			}

			plugins := mgr.Plugins()
			if len(plugins) == 0 {
				fmt.Println("No plugins found.")
				fmt.Println("Plugin directories:", paths)
				return nil
			}

			fmt.Printf("%-15s %-20s %-10s %-10s %s\n", "ID", "NAME", "TYPE", "VERSION", "DIR")
			for _, p := range plugins {
				enabledStr := "enabled"
				if cfg != nil {
					if entry, ok := cfg.Plugins.Entries[p.Manifest.ID]; ok && !entry.Enabled {
						enabledStr = "disabled"
					}
				}
				fmt.Printf("%-15s %-20s %-10s %-10s %s [%s]\n",
					p.Manifest.ID,
					p.Manifest.Name,
					p.Manifest.Type,
					p.Manifest.Version,
					p.Manifest.Dir,
					enabledStr,
				)
			}
			return nil
		},
	}
}

func pluginInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install <name|github-url|npm-package|path>",
		Short: "Install a plugin from FastClaw Hub, GitHub, npm, or local path",
		Long: `Install a plugin. The source is auto-detected:

  fastclaw plugins install telegram                        # FastClaw Hub
  fastclaw plugins install github.com/user/repo            # GitHub repo
  fastclaw plugins install @ollama/web-search              # npm plugin (bridged)
  fastclaw plugins install ./my-plugin                     # local directory`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			source := args[0]

			homeDir, err := config.HomeDir()
			if err != nil {
				return err
			}
			pluginsDir := filepath.Join(homeDir, "plugins")

			switch {
			case isLocalPath(source):
				return installFromLocal(source, pluginsDir)
			case isGitHubRef(source):
				return installFromGitHub(source, pluginsDir)
			case isNpmPackage(source):
				return installFromNpm(source, pluginsDir)
			default:
				return installFromHub(source, pluginsDir)
			}
		},
	}
}

func isLocalPath(s string) bool {
	return strings.HasPrefix(s, "./") || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "../")
}

func isGitHubRef(s string) bool {
	return strings.HasPrefix(s, "github.com/") || strings.HasPrefix(s, "https://github.com/")
}

func isNpmPackage(s string) bool {
	// @scope/package is always npm.
	return strings.HasPrefix(s, "@")
}

func installFromLocal(srcDir, pluginsDir string) error {
	manifestPath := filepath.Join(srcDir, "plugin.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", manifestPath, err)
	}

	var manifest struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("invalid plugin.json: %w", err)
	}
	if manifest.ID == "" {
		return fmt.Errorf("plugin.json missing 'id' field")
	}

	destDir := filepath.Join(pluginsDir, manifest.ID)
	os.RemoveAll(destDir)
	if err := os.MkdirAll(filepath.Dir(destDir), 0o755); err != nil {
		return err
	}

	cpCmd := exec.Command("cp", "-r", srcDir, destDir)
	if out, err := cpCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("copy failed: %s: %w", string(out), err)
	}

	fmt.Printf("Plugin %q installed to %s\n", manifest.ID, destDir)
	return nil
}

func installFromGitHub(source, pluginsDir string) error {
	// Normalize URL
	repo := strings.TrimPrefix(source, "https://")
	repo = strings.TrimPrefix(repo, "github.com/")
	repo = strings.TrimSuffix(repo, ".git")
	repoURL := "https://github.com/" + repo

	// Clone to temp dir
	tmpDir, err := os.MkdirTemp("", "fastclaw-plugin-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	fmt.Printf("Cloning %s...\n", repoURL)
	cloneCmd := exec.Command("git", "clone", "--depth=1", repoURL, tmpDir)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone failed: %s: %w", string(out), err)
	}

	return installFromLocal(tmpDir, pluginsDir)
}

func installFromHub(name, pluginsDir string) error {
	tmpDir, err := os.MkdirTemp("", "fastclaw-plugin-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	fmt.Printf("Installing %q from FastClaw Hub...\n", name)

	tarballURL := fmt.Sprintf("https://github.com/%s/archive/refs/heads/main.tar.gz", hubRepo)

	// Download tarball
	tarball := filepath.Join(tmpDir, "repo.tar.gz")
	dlCmd := exec.Command("curl", "-fsSL", "-o", tarball, tarballURL)
	if out, err := dlCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("download failed: %s: %w", string(out), err)
	}

	// Extract full tarball
	extractDir := filepath.Join(tmpDir, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return err
	}
	tarCmd := exec.Command("tar", "-xzf", tarball, "-C", extractDir)
	if out, err := tarCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extract failed: %s: %w", string(out), err)
	}

	// Find top-level dir (name varies: fastclaw-main, fastclaw-v0.16.0, etc.)
	entries, _ := os.ReadDir(extractDir)
	if len(entries) == 0 {
		return fmt.Errorf("extract failed: empty archive")
	}
	pluginDir := filepath.Join(extractDir, entries[0].Name(), "plugins", name)
	if _, err := os.Stat(pluginDir); os.IsNotExist(err) {
		return fmt.Errorf("plugin %q not found in FastClaw Hub", name)
	}

	// Check if it has plugin.json (standard plugin) or is a utility
	if _, err := os.Stat(filepath.Join(pluginDir, "plugin.json")); err == nil {
		return installFromLocal(pluginDir, pluginsDir)
	}

	// No plugin.json — copy as utility (e.g. plugin-bridge)
	toolsDir := filepath.Join(filepath.Dir(pluginsDir), "tools")
	os.MkdirAll(toolsDir, 0o755)
	destDir := filepath.Join(toolsDir, name)
	os.RemoveAll(destDir)
	cpCmd := exec.Command("cp", "-r", pluginDir, destDir)
	if out, err := cpCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("copy failed: %s: %w", string(out), err)
	}
	fmt.Printf("Installed %q to %s\n", name, destDir)
	return nil
}

func installFromNpm(pkg, pluginsDir string) error {
	homeDir := filepath.Dir(pluginsDir)

	// Derive plugin ID from package name
	pluginID := pkg
	if i := strings.LastIndex(pluginID, "/"); i >= 0 {
		pluginID = pluginID[i+1:]
	}
	pluginID = strings.TrimPrefix(pluginID, "fastclaw-")

	// 1. npm install to temp dir to inspect the package
	tmpDir, err := os.MkdirTemp("", "fastclaw-npm-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	fmt.Printf("Downloading %s...\n", pkg)
	npmCmd := exec.Command("npm", "install", "--production", pkg)
	npmCmd.Dir = tmpDir
	if out, err := npmCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("npm install failed: %s: %w", string(out), err)
	}

	// 2. Check if it's a compatible plugin (supports fastclaw or openclaw plugin format)
	pkgDir := filepath.Join(tmpDir, "node_modules", pkg)
	isPlugin := false
	for _, marker := range []string{"fastclaw.plugin.json", "openclaw.plugin.json"} {
		if _, err := os.Stat(filepath.Join(pkgDir, marker)); err == nil {
			isPlugin = true
			break
		}
	}
	// Also check package.json for fastclaw/openclaw field
	if !isPlugin {
		if data, err := os.ReadFile(filepath.Join(pkgDir, "package.json")); err == nil {
			var pj map[string]json.RawMessage
			if json.Unmarshal(data, &pj) == nil {
				for _, key := range []string{"fastclaw", "openclaw"} {
					if _, ok := pj[key]; ok {
						isPlugin = true
						break
					}
				}
			}
		}
	}
	if !isPlugin {
		return fmt.Errorf("%s is not a compatible plugin", pkg)
	}

	// 3. Find entry file
	entryFile := ""
	for _, name := range []string{"index.ts", "index.js"} {
		if _, err := os.Stat(filepath.Join(pkgDir, name)); err == nil {
			entryFile = fmt.Sprintf("./node_modules/%s/%s", pkg, name)
			break
		}
	}
	if entryFile == "" {
		return fmt.Errorf("cannot find entry file for %s", pkg)
	}

	// 4. Test bridgeability — run proxy and check if tools are registered
	proxyDir := filepath.Join(homeDir, "tools", "plugin-bridge")
	proxyJS := filepath.Join(proxyDir, "proxy.js")
	if _, err := os.Stat(proxyJS); os.IsNotExist(err) {
		fmt.Println("Installing plugin-bridge from FastClaw Hub...")
		if err := installFromHub("plugin-bridge", pluginsDir); err != nil {
			return fmt.Errorf("failed to install plugin-bridge: %w", err)
		}
		depCmd := exec.Command("npm", "install", "--production")
		depCmd.Dir = proxyDir
		if out, err := depCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("npm install proxy deps failed: %s: %w", string(out), err)
		}
	}

	fmt.Println("Checking compatibility...")
	absEntry := filepath.Join(pkgDir, filepath.Base(entryFile))
	testInput := `{"jsonrpc":"2.0","method":"initialize","params":{"config":{}},"id":1}
{"jsonrpc":"2.0","method":"tool.list","id":2}
{"jsonrpc":"2.0","method":"shutdown","id":3}
`
	testCmd := exec.Command("npx", "tsx", proxyJS, absEntry)
	testCmd.Stdin = strings.NewReader(testInput)
	testCmd.Dir = tmpDir
	testOut, testErr := testCmd.CombinedOutput()

	toolCount := 0
	hasChannel := false
	for _, line := range strings.Split(string(testOut), "\n") {
		if !strings.HasPrefix(line, "{") {
			// Check stderr for channel registration
			if strings.Contains(line, "registered channel") {
				hasChannel = true
			}
			continue
		}
		var resp map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &resp) == nil {
			if result, ok := resp["result"]; ok {
				var toolList struct {
					Tools []json.RawMessage `json:"tools"`
				}
				if json.Unmarshal(result, &toolList) == nil && len(toolList.Tools) > 0 {
					toolCount = len(toolList.Tools)
				}
			}
		}
	}

	if testErr != nil && toolCount == 0 {
		if hasChannel {
			return fmt.Errorf("cannot install %s: this is a channel plugin that requires a separate runtime. Consider writing a native FastClaw plugin instead", pkg)
		}
		return fmt.Errorf("cannot install %s: plugin is not compatible with FastClaw bridge", pkg)
	}

	if toolCount == 0 {
		return fmt.Errorf("cannot install %s: no tools detected. Only plugins that register tools can be bridged", pkg)
	}

	// 5. Compatible! Move to plugins dir
	destDir := filepath.Join(pluginsDir, pluginID)
	os.RemoveAll(destDir)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	// npm install in final location
	npmCmd2 := exec.Command("npm", "install", "--production", pkg)
	npmCmd2.Dir = destDir
	if out, err := npmCmd2.CombinedOutput(); err != nil {
		os.RemoveAll(destDir)
		return fmt.Errorf("npm install failed: %s: %w", string(out), err)
	}

	// Generate plugin.json
	manifest := map[string]any{
		"id":           pluginID,
		"name":         fmt.Sprintf("Bridged: %s", pkg),
		"version":      "0.1.0",
		"description":  fmt.Sprintf("Plugin %s (bridged via plugin-bridge)", pkg),
		"type":         "tool",
		"command":      fmt.Sprintf("npx tsx %s %s", proxyJS, entryFile),
		"capabilities": []string{"tool"},
		"config":       map[string]any{},
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(destDir, "plugin.json"), data, 0o644); err != nil {
		return err
	}

	fmt.Printf("Plugin %q installed (%d tools bridged)\n", pluginID, toolCount)
	return nil
}

func pluginRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "Remove an installed plugin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			homeDir, err := config.HomeDir()
			if err != nil {
				return err
			}

			pluginDir := filepath.Join(homeDir, "plugins", id)
			if _, err := os.Stat(pluginDir); os.IsNotExist(err) {
				return fmt.Errorf("plugin %q not found at %s", id, pluginDir)
			}

			if err := os.RemoveAll(pluginDir); err != nil {
				return fmt.Errorf("remove plugin: %w", err)
			}

			fmt.Printf("Plugin %q removed.\n", id)
			return nil
		},
	}
}
