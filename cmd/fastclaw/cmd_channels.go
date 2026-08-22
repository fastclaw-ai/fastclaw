package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/fastclaw/internal/agentcli"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func channelsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "channels", Short: "Manage IM channel bindings"}
	cmd.AddCommand(channelsListCmd(), channelsConnectCmd(), channelsDeleteCmd())
	return cmd
}

func channelsListCmd() *cobra.Command {
	var agentName string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List channel bindings",
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStoreFromEnv()
			if err != nil {
				return err
			}
			defer st.Close()
			ctx := context.Background()
			var rows []store.ChannelRecord
			if agentName != "" {
				ag, err := agentcli.Resolve(ctx, st, agentName)
				if err != nil {
					return err
				}
				rows, err = st.ListChannels(ctx, ag.UserID, ag.ID)
			} else {
				rows, err = st.ListAllChannels(ctx)
			}
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				fmt.Println("No channels.")
				return nil
			}
			fmt.Printf("%-24s %-10s %-20s %-22s %-22s %-8s %s\n", "ID", "TYPE", "ACCOUNT", "USER", "AGENT", "ENABLED", "UPDATED")
			for _, ch := range rows {
				fmt.Printf("%-24s %-10s %-20s %-22s %-22s %-8v %s\n",
					ch.ID, ch.Type, ch.AccountID, ch.UserID, ch.AgentID, ch.Enabled, ch.UpdatedAt.Format("2006-01-02 15:04"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&agentName, "agent", "", "agent name or id")
	return cmd
}

func channelsConnectCmd() *cobra.Command {
	var agentName, typ, accountID, token, baseURL, userID, appSecret, verificationToken, encryptKey string
	var shared bool
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Create or update a channel binding",
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStoreFromEnv()
			if err != nil {
				return err
			}
			defer st.Close()
			ctx := context.Background()
			ag, err := agentcli.Resolve(ctx, st, agentName)
			if err != nil {
				return err
			}
			if typ == "" || accountID == "" {
				return fmt.Errorf("--type and --account are required")
			}
			cc := config.ChannelConfig{
				Enabled: true,
				Accounts: map[string]config.AccountConfig{
					accountID: {
						BotToken: token,
						BaseURL:  baseURL,
						UserID:   userID,
					},
				},
			}
			if typ == "feishu" {
				cc.Accounts[accountID] = config.AccountConfig{
					BotToken:   appSecret,
					UserID:     verificationToken,
					EncryptKey: encryptKey,
				}
			}
			data := channelConfigData(cc)
			ch := &store.ChannelRecord{
				ID:             "ch_" + typ + "_" + uuid.NewString(),
				UserID:         ag.UserID,
				AgentID:        ag.ID,
				Type:           typ,
				AccountID:      accountID,
				Enabled:        true,
				BotToken:       token,
				BaseURL:        baseURL,
				PlatformUserID: userID,
				SharedIdentity: channelSharedIdentityForConnect(typ, shared),
				Data:           data,
			}
			if typ == "feishu" {
				ch.BotToken = appSecret
				ch.PlatformUserID = verificationToken
			}
			if err := st.SaveChannel(ctx, ch); err != nil {
				return err
			}
			fmt.Printf("Saved channel %s:%s for agent %s\n", typ, accountID, ag.ID)
			notifyGatewayReload()
			return nil
		},
	}
	cmd.Flags().StringVar(&agentName, "agent", "", "agent name or id (required)")
	cmd.Flags().StringVar(&typ, "type", "", "channel type: telegram, discord, slack, line, feishu, wecom")
	cmd.Flags().StringVar(&accountID, "account", "", "channel account id / bot username / app id (required)")
	cmd.Flags().StringVar(&token, "token", "", "bot token / access token")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "optional base URL")
	cmd.Flags().StringVar(&userID, "user-id", "", "platform user id or secret field for some adapters")
	cmd.Flags().StringVar(&appSecret, "app-secret", "", "Feishu app secret")
	cmd.Flags().StringVar(&verificationToken, "verification-token", "", "Feishu verification token")
	cmd.Flags().StringVar(&encryptKey, "encrypt-key", "", "Feishu encrypt key")
	cmd.Flags().BoolVar(&shared, "shared-identity", false, "share owner identity across channels")
	_ = cmd.MarkFlagRequired("agent")
	return cmd
}

func channelSharedIdentityForConnect(channelType string, requested bool) bool {
	return channelType != "wecom" && requested
}

func channelsDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"rm"},
		Short:   "Delete a channel binding by id",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStoreFromEnv()
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.DeleteChannel(context.Background(), args[0]); err != nil {
				return err
			}
			fmt.Printf("Deleted channel %s\n", args[0])
			notifyGatewayReload()
			return nil
		},
	}
}

func channelConfigData(c config.ChannelConfig) map[string]interface{} {
	blob, _ := json.Marshal(c)
	var m map[string]interface{}
	_ = json.Unmarshal(blob, &m)
	delete(m, "enabled")
	return m
}
