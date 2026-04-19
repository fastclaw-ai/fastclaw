package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// userCmd manages API keys.
func userCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "apikey",
		Aliases: []string{"user"},
		Short:   "Manage API keys for accessing FastClaw agents",
		Long:    `Manage API keys stored in ~/.fastclaw/apikeys.json. API keys grant access to the FastClaw HTTP API (chat, agents, config).`,
	}
	cmd.AddCommand(apikeyAddCmd())
	cmd.AddCommand(apikeyListCmd())
	cmd.AddCommand(apikeyRemoveCmd())
	cmd.AddCommand(apikeyRotateCmd())
	return cmd
}

func apikeyAddCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "add [id]",
		Short: "Create a new API key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := users.Load()
			if err != nil {
				return err
			}
			ak, key, err := reg.Add(args[0], name)
			if err != nil {
				return err
			}
			if err := reg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "API key created: %s\n", ak.ID)
			fmt.Println(key)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "display name for this key")
	return cmd
}

func apikeyListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all API keys",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := users.Load()
			if err != nil {
				return err
			}
			list := reg.List()
			if len(list) == 0 {
				fmt.Println("No API keys configured.")
				return nil
			}
			for _, ak := range list {
				masked := ak.Key
				if len(masked) > 10 {
					masked = masked[:6] + "****" + masked[len(masked)-4:]
				}
				name := ak.Name
				if name == "" {
					name = ak.ID
				}
				fmt.Printf("%-20s %-20s %s\n", ak.ID, name, masked)
			}
			return nil
		},
	}
}

func apikeyRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove [id]",
		Short: "Remove an API key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := users.Load()
			if err != nil {
				return err
			}
			if err := reg.Remove(args[0]); err != nil {
				return err
			}
			return reg.Save()
		},
	}
}

func apikeyRotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rotate [id]",
		Aliases: []string{"token"},
		Short:   "Rotate (regenerate) an API key",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := users.Load()
			if err != nil {
				return err
			}
			key, err := reg.IssueToken(args[0])
			if err != nil {
				return err
			}
			if err := reg.Save(); err != nil {
				return err
			}
			fmt.Println(key)
			return nil
		},
	}
}
