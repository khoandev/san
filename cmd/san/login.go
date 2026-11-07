package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/proc"
)

var loginCmd = &cobra.Command{
	Use:   "login [provider]",
	Short: "Sign in to an interactive provider (antigravity, chatgpt, copilot)",
	Long: `Sign in to an interactive provider using browser OAuth.

Providers:
  antigravity (default): Google Antigravity OAuth (higher rate limits, Gemini 3 & Claude)
  chatgpt, openai:      OpenAI ChatGPT subscription
  copilot, github:      GitHub Copilot subscription

Examples:
  san login
  san login antigravity
  san login chatgpt
  san login copilot`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		providerName := "antigravity"
		if len(args) > 0 {
			providerName = strings.ToLower(strings.TrimSpace(args[0]))
		}

		var provider llm.ProviderID
		var authMethod llm.AuthMethod

		switch providerName {
		case "antigravity", "google":
			provider = llm.Google
			authMethod = llm.AuthAntigravity
		case "chatgpt", "openai":
			provider = llm.OpenAI
			authMethod = llm.AuthSubscription
		case "copilot", "github":
			provider = llm.Copilot
			authMethod = llm.AuthSubscription
		default:
			return fmt.Errorf("unknown interactive provider %q (expected: antigravity, chatgpt, copilot)", providerName)
		}

		fmt.Printf("Signing in to %s ...\n", providerName)

		onPrompt := func(p llm.LoginPrompt) {
			if p.UserCode != "" {
				fmt.Printf("\nEnter code: %s\nat: %s\n\n", p.UserCode, p.URL)
			} else {
				fmt.Printf("\nOpening browser for authorization:\n  %s\n\nWaiting for callback on http://localhost:51121/oauth-callback ...\n", p.URL)
			}
			_ = proc.OpenURL(p.URL)
		}

		if err := llm.Login(cmd.Context(), provider, authMethod, onPrompt); err != nil {
			return fmt.Errorf("login failed: %w", err)
		}

		fmt.Printf("\n✓ Successfully signed in to %s!\n", providerName)

		if st, err := llm.NewStore(); err == nil {
			_ = st.Connect(provider, authMethod)
			if provider == llm.Google && authMethod == llm.AuthAntigravity {
				if st.CurrentModel() == nil {
					_ = st.SetCurrentModel("gemini-3-flash", llm.Google, llm.AuthAntigravity)
					fmt.Println("Active model set to gemini-3-flash (Antigravity).")
				}
			}
		}

		return nil
	},
}

var logoutCmd = &cobra.Command{
	Use:   "logout [provider]",
	Short: "Log out from an interactive provider",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		providerName := "antigravity"
		if len(args) > 0 {
			providerName = strings.ToLower(strings.TrimSpace(args[0]))
		}

		var provider llm.ProviderID
		var authMethod llm.AuthMethod

		switch providerName {
		case "antigravity", "google":
			provider = llm.Google
			authMethod = llm.AuthAntigravity
		case "chatgpt", "openai":
			provider = llm.OpenAI
			authMethod = llm.AuthSubscription
		case "copilot", "github":
			provider = llm.Copilot
			authMethod = llm.AuthSubscription
		default:
			return fmt.Errorf("unknown interactive provider %q", providerName)
		}

		if err := llm.Logout(provider, authMethod); err != nil {
			return err
		}

		if st, err := llm.NewStore(); err == nil {
			_ = st.Disconnect(provider)
			if cur := st.CurrentModel(); cur != nil && cur.Provider == provider {
				_ = st.ClearCurrentModel()
			}
		}

		fmt.Printf("Logged out from %s.\n", providerName)
		return nil
	},
}
