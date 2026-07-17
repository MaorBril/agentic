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
	routeClassifier string
	routeDefault    string
	routeDeep       string
	routeStandard   string
	routeLight      string
)

var routingCmd = &cobra.Command{
	Use:   "routing",
	Short: "Dynamic tier routing (an LLM assigns each task to a model tier)",
}

var routingSetCmd = &cobra.Command{
	Use:   "set <alias>",
	Short: "Create or update a dynamic routing alias",
	Long: `A routing alias classifies each new user turn with a cheap model and
dispatches it to a tier. Use it like any model: /model auto, or
profiles: {model: auto}.`,
	Example: `  agentic routing set auto --classifier haiku \
      --deep opus --standard sonnet --light qwen`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if routeClassifier == "" {
			return fmt.Errorf("--classifier is required (a cheap model alias)")
		}
		tiers := map[string]string{"deep": routeDeep, "standard": routeStandard, "light": routeLight}
		snippet := "classifier: " + routeClassifier + "\n"
		if routeDefault != "" {
			snippet += "default: " + routeDefault + "\n"
		}
		snippet += "tiers:\n"
		any := false
		for _, tier := range []string{"deep", "standard", "light"} {
			if tiers[tier] != "" {
				snippet += fmt.Sprintf("  %s: %s\n", tier, tiers[tier])
				any = true
			}
		}
		if !any {
			return fmt.Errorf("at least one of --deep / --standard / --light is required")
		}
		return editConfig(func(doc *config.Doc) error {
			return doc.SetSubtree("routing", args[0], snippet)
		}, "routing "+args[0])
	},
}

var routingListCmd = &cobra.Command{
	Use:   "list",
	Short: "List routing aliases",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _, err := loadConfig()
		if err != nil {
			return err
		}
		if len(cfg.Routing) == 0 {
			fmt.Println("no routing aliases — create one with `agentic routing set`")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ALIAS\tCLASSIFIER\tDEEP\tSTANDARD\tLIGHT\tDEFAULT")
		names := make([]string, 0, len(cfg.Routing))
		for n := range cfg.Routing {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			r := cfg.Routing[n]
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", n, r.Classifier,
				orDefault(r.Tiers["deep"], "—"), orDefault(r.Tiers["standard"], "—"),
				orDefault(r.Tiers["light"], "—"), orDefault(r.Default, "standard"))
		}
		return tw.Flush()
	},
}

var routingRemoveCmd = &cobra.Command{
	Use:   "remove <alias>",
	Short: "Remove a routing alias",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return editConfig(func(doc *config.Doc) error {
			return doc.Delete("routing", args[0])
		}, "removed routing "+args[0])
	},
}

func init() {
	routingSetCmd.Flags().StringVar(&routeClassifier, "classifier", "", "cheap model alias that assesses complexity")
	routingSetCmd.Flags().StringVar(&routeDefault, "default", "", "tier when classification fails (default: standard)")
	routingSetCmd.Flags().StringVar(&routeDeep, "deep", "", "model alias for planning/architecture/hard reasoning")
	routingSetCmd.Flags().StringVar(&routeStandard, "standard", "", "model alias for ordinary coding/tool work")
	routingSetCmd.Flags().StringVar(&routeLight, "light", "", "model alias for mechanical edits/verification")
	routingCmd.AddCommand(routingSetCmd, routingListCmd, routingRemoveCmd)
	rootCmd.AddCommand(routingCmd)
}
