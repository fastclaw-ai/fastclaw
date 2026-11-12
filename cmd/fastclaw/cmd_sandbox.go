package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// sandboxCmd handles sandbox management subcommands.
func sandboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage sandboxed execution environments",
	}
	cmd.AddCommand(sandboxCreateCmd())
	cmd.AddCommand(sandboxListCmd())
	cmd.AddCommand(sandboxConnectCmd())
	cmd.AddCommand(sandboxDestroyCmd())
	return cmd
}

func sandboxCreateCmd() *cobra.Command {
	var image string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a standalone sandbox container",
		RunE: func(cmd *cobra.Command, args []string) error {
			sb := sandbox.NewDockerSandbox(image, "", nil)
			if err := sb.Create(); err != nil {
				return fmt.Errorf("create sandbox: %w", err)
			}
			fmt.Printf("Sandbox created: %s (image: %s)\n", sb.ContainerID(), image)
			return nil
		},
	}
	cmd.Flags().StringVar(&image, "image", sandbox.DefaultImage, "Docker image to use")
	return cmd
}

func sandboxListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List running sandbox containers",
		RunE: func(cmd *cobra.Command, args []string) error {
			listCmd := exec.Command("docker", "ps", "--filter", "label=fastclaw=sandbox", "--format",
				"table {{.ID}}\t{{.Image}}\t{{.Status}}\t{{.Names}}")
			listCmd.Stdout = os.Stdout
			listCmd.Stderr = os.Stderr
			return listCmd.Run()
		},
	}
}

func sandboxConnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "connect <container-id>",
		Short: "Exec into a sandbox container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			execCmd := exec.Command("docker", "exec", "-it", args[0], "/bin/sh")
			execCmd.Stdin = os.Stdin
			execCmd.Stdout = os.Stdout
			execCmd.Stderr = os.Stderr
			return execCmd.Run()
		},
	}
}

func sandboxDestroyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "destroy <container-id>",
		Short: "Remove a sandbox container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rmCmd := exec.Command("docker", "rm", "-f", args[0])
			rmCmd.Stdout = os.Stdout
			rmCmd.Stderr = os.Stderr
			if err := rmCmd.Run(); err != nil {
				return err
			}
			fmt.Printf("Sandbox %s destroyed.\n", args[0])
			return nil
		},
	}
}
