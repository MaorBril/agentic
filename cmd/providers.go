package cmd

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/maorbril/agentic/internal/config"
)

var (
	provType   string
	provBase   string
	provKeyEnv string
	provMaxTok string
)

var providersCmd = &cobra.Command{
	Use:   "providers",
	Short: "Manage upstream providers",
}

var providersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured providers",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _, err := loadConfig()
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tTYPE\tBASE URL\tKEY")
		names := make([]string, 0, len(cfg.Providers))
		for n := range cfg.Providers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p := cfg.Providers[n]
			key := "✓"
			switch {
			case p.APIKey != "":
				key = "✓ (literal)"
			case p.APIKeyEnv == "":
				key = "· (no auth)"
			case p.Key() == "":
				key = "✗ (" + p.APIKeyEnv + " unavailable)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", n, p.Type, p.BaseURL, key)
		}
		return tw.Flush()
	},
}

var providersAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add or update a provider",
	Long: `Add an upstream provider. Examples:

  agentic providers add openai --type openai --base-url https://api.openai.com/v1 \
      --key-env OPENAI_API_KEY --max-tokens-param max_completion_tokens
  agentic providers add xai   --type openai --base-url https://api.x.ai/v1 --key-env XAI_API_KEY
  agentic providers add local --type openai --base-url http://localhost:11434/v1 --key-env ""`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if provType != config.ProviderAnthropic && provType != config.ProviderOpenAI {
			return fmt.Errorf("--type must be %q or %q", config.ProviderAnthropic, config.ProviderOpenAI)
		}
		if provBase == "" {
			return fmt.Errorf("--base-url is required")
		}
		snippet := fmt.Sprintf("type: %s\nbase_url: %s\napi_key_env: %s\n",
			provType, yamlQuote(provBase), yamlQuote(provKeyEnv))
		if provMaxTok != "" {
			snippet += fmt.Sprintf("max_tokens_param: %s\n", provMaxTok)
		}
		return editConfig(func(doc *config.Doc) error {
			return doc.SetSubtree("providers", args[0], snippet)
		}, "provider "+args[0])
	},
}

var providersRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a provider",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return editConfig(func(doc *config.Doc) error {
			return doc.Delete("providers", args[0])
		}, "removed provider "+args[0])
	},
}

func init() {
	providersAddCmd.Flags().StringVar(&provType, "type", "openai", "provider dialect: anthropic | openai")
	providersAddCmd.Flags().StringVar(&provBase, "base-url", "", "API base URL")
	providersAddCmd.Flags().StringVar(&provKeyEnv, "key-env", "", "env var holding the API key (empty = no auth)")
	providersAddCmd.Flags().StringVar(&provMaxTok, "max-tokens-param", "", "max_tokens | max_completion_tokens")
	providersCmd.AddCommand(providersListCmd, providersAddCmd, providersRemoveCmd)
}
