package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/maorbril/agentic/internal/anthropic"
	"github.com/maorbril/agentic/internal/config"
	"github.com/maorbril/agentic/internal/launch"
	"github.com/maorbril/agentic/internal/pricing"
	"github.com/maorbril/agentic/internal/router"
)

var modelsCmd = &cobra.Command{
	Use:   "models",
	Short: "Model and provider inventory",
}

var modelsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured model aliases",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, dataDir, err := loadConfig()
		if err != nil {
			return err
		}
		prices := pricing.Load(dataDir, cfg)
		tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ALIAS\tPROVIDER\tUPSTREAM MODEL\t$IN/MTok\t$OUT/MTok\tKEY")
		aliases := make([]string, 0, len(cfg.Models))
		for a := range cfg.Models {
			aliases = append(aliases, a)
		}
		sort.Strings(aliases)
		for _, a := range aliases {
			m := cfg.Models[a]
			p := cfg.Providers[m.Provider]
			priceIn, priceOut := "?", "?"
			if pr, ok := prices.Get(m.ID); ok {
				priceIn, priceOut = fmt.Sprintf("%.2f", pr.Input), fmt.Sprintf("%.2f", pr.Output)
			}
			key := "✓"
			if p.APIKeyEnv != "" && os.Getenv(p.APIKeyEnv) == "" && p.APIKey == "" {
				key = "✗ (" + p.APIKeyEnv + " unset)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", a, m.Provider, m.ID, priceIn, priceOut, key)
		}
		return tw.Flush()
	},
}

var (
	modelProvider  string
	modelID        string
	modelReasoning string
	modelMaxOutput int
)

var modelsAddCmd = &cobra.Command{
	Use:   "add <alias>",
	Short: "Add or update a model alias",
	Example: `  agentic models add gpt  --provider openai --id gpt-5.2 --reasoning effort
  agentic models add grok --provider xai --id grok-4
  agentic models add qwen --provider local --id qwen3-coder-30b`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if modelProvider == "" || modelID == "" {
			return fmt.Errorf("--provider and --id are required")
		}
		snippet := fmt.Sprintf("provider: %s\nid: %s\n", modelProvider, yamlQuote(modelID))
		if modelReasoning != "" {
			snippet += "reasoning: " + modelReasoning + "\n"
		}
		if modelMaxOutput > 0 {
			snippet += fmt.Sprintf("max_output: %d\n", modelMaxOutput)
		}
		return editConfig(func(doc *config.Doc) error {
			return doc.SetSubtree("models", args[0], snippet)
		}, "model "+args[0]+" -> "+modelProvider+"/"+modelID)
	},
}

var modelsRemoveCmd = &cobra.Command{
	Use:   "remove <alias>",
	Short: "Remove a model alias",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return editConfig(func(doc *config.Doc) error {
			return doc.Delete("models", args[0])
		}, "removed model "+args[0])
	},
}

var modelsTestCmd = &cobra.Command{
	Use:   "test [alias]",
	Short: "Send a 1-token request per model through the router",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, dataDir, err := loadConfig()
		if err != nil {
			return err
		}
		baseURL, token, stop, err := ensureRouter(cmd.Context(), cfg, dataDir)
		if err != nil {
			return err
		}
		defer stop()

		aliases := make([]string, 0, len(cfg.Models))
		if len(args) == 1 {
			aliases = args
		} else {
			for a := range cfg.Models {
				aliases = append(aliases, a)
			}
			sort.Strings(aliases)
		}
		failed := 0
		for _, alias := range aliases {
			start := time.Now()
			err := probeModel(baseURL, token, alias)
			if err != nil {
				failed++
				fmt.Printf("✗ %-12s %v\n", alias, err)
			} else {
				fmt.Printf("✓ %-12s ok (%dms)\n", alias, time.Since(start).Milliseconds())
			}
		}
		if failed > 0 {
			return fmt.Errorf("%d model(s) failed", failed)
		}
		return nil
	},
}

func probeModel(baseURL, token, alias string) error {
	body := fmt.Sprintf(`{"model":%q,"max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`, alias)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", token)
	req.Header.Set("content-type", "application/json")
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var apiErr anthropic.APIError
		json.NewDecoder(resp.Body).Decode(&apiErr)
		return fmt.Errorf("%d %s: %s", resp.StatusCode, apiErr.Error.Type, apiErr.Error.Message)
	}
	return nil
}

// ensureRouter joins the existing leader or runs one in-process for the
// duration of the command.
func ensureRouter(ctx context.Context, cfg *config.Config, dataDir string) (string, string, func(), error) {
	token, err := launch.Token(dataDir)
	if err != nil {
		return "", "", nil, err
	}
	mgr := &router.Manager{Port: cfg.Router.Port, Token: token, DataDir: dataDir, Log: logger()}
	runCtx, cancel := context.WithCancel(context.Background())
	go mgr.Run(runCtx)
	if err := mgr.Ensure(ctx); err != nil {
		cancel()
		return "", "", nil, err
	}
	return mgr.BaseURL(), token, cancel, nil
}

const pricesURL = "https://raw.githubusercontent.com/maorbril/agentic/main/internal/pricing/prices.json"

var modelsUpdatePricesCmd = &cobra.Command{
	Use:   "update-prices",
	Short: "Fetch the latest pricing table (no binary update needed)",
	RunE: func(cmd *cobra.Command, args []string) error {
		_, dataDir, err := loadConfig()
		if err != nil {
			return err
		}
		resp, err := http.Get(pricesURL)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("fetching %s: HTTP %d", pricesURL, resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return err
		}
		var check map[string]json.RawMessage
		if err := json.Unmarshal(data, &check); err != nil {
			return fmt.Errorf("fetched prices are not valid JSON: %w", err)
		}
		path := filepath.Join(dataDir, "prices.json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
		fmt.Printf("✓ wrote %s (%d models)\n", path, len(check))
		reloadRouter()
		return nil
	},
}

func init() {
	modelsAddCmd.Flags().StringVar(&modelProvider, "provider", "", "provider name from config")
	modelsAddCmd.Flags().StringVar(&modelID, "id", "", "upstream model id")
	modelsAddCmd.Flags().StringVar(&modelReasoning, "reasoning", "", "none | effort | passive")
	modelsAddCmd.Flags().IntVar(&modelMaxOutput, "max-output", 0, "clamp max_tokens to this output cap")
	modelsCmd.AddCommand(modelsListCmd, modelsAddCmd, modelsRemoveCmd, modelsTestCmd, modelsUpdatePricesCmd)
}
