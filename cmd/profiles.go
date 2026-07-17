package cmd

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/maorbril/agentic/internal/config"
	"gopkg.in/yaml.v3"
)

var profilesCmd = &cobra.Command{
	Use:   "profiles",
	Short: "Inspect launch profiles",
}

var profilesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List profiles",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _, err := loadConfig()
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tMODEL\tSMALL/FAST\tBUDGET\tMODE")
		names := make([]string, 0, len(cfg.Profiles))
		for n := range cfg.Profiles {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p := cfg.Profiles[n]
			mode := "router"
			if p.Passthrough {
				mode = "passthrough (subscription)"
			}
			budget := "—"
			if p.Budget != nil && p.Budget.Daily > 0 {
				budget = fmt.Sprintf("$%.2f/day", p.Budget.Daily)
			}
			def := ""
			if n == cfg.DefaultProfile {
				def = " (default)"
			}
			fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\t%s\n", n, def, orDefault(p.Model, "—"), orDefault(p.SmallFast, "—"), budget, mode)
		}
		return tw.Flush()
	},
}

var profilesShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show one profile as YAML",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _, err := loadConfig()
		if err != nil {
			return err
		}
		p, ok := cfg.Profiles[args[0]]
		if !ok {
			return fmt.Errorf("profile %q not found", args[0])
		}
		out, err := yaml.Marshal(map[string]config.Profile{args[0]: p})
		if err != nil {
			return err
		}
		fmt.Print(string(out))
		return nil
	},
}

var budgetProfile string

var budgetCmd = &cobra.Command{
	Use:   "budget",
	Short: "Manage spend budgets",
}

var budgetSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set budget caps (global, or per profile with --profile)",
	Example: `  agentic budget set --daily 25 --monthly 400
  agentic budget set --profile cheap --daily 5`,
	RunE: func(cmd *cobra.Command, args []string) error {
		base := "budgets"
		if budgetProfile != "" {
			base = "profiles." + budgetProfile + ".budget"
		}
		changed := false
		return editConfig(func(doc *config.Doc) error {
			for _, f := range []string{"daily", "weekly", "monthly", "warn_at"} {
				if cmd.Flags().Changed(f) {
					v, _ := cmd.Flags().GetFloat64(f)
					if err := doc.Set(base+"."+f, fmt.Sprintf("%g", v)); err != nil {
						return err
					}
					changed = true
				}
			}
			if cmd.Flags().Changed("hard-stop") {
				v, _ := cmd.Flags().GetBool("hard-stop")
				if err := doc.Set(base+".hard_stop", fmt.Sprint(v)); err != nil {
					return err
				}
				changed = true
			}
			if !changed {
				return fmt.Errorf("nothing to set — pass --daily / --weekly / --monthly / --warn-at / --hard-stop")
			}
			return nil
		}, "budget updated ("+base+")")
	},
}

func init() {
	budgetSetCmd.Flags().StringVar(&budgetProfile, "profile", "", "set the budget on a profile instead of globally")
	budgetSetCmd.Flags().Float64("daily", 0, "daily cap in USD")
	budgetSetCmd.Flags().Float64("weekly", 0, "weekly cap in USD")
	budgetSetCmd.Flags().Float64("monthly", 0, "monthly cap in USD")
	budgetSetCmd.Flags().Float64("warn_at", 0, "warn threshold as a fraction (default 0.8)")
	budgetSetCmd.Flags().Bool("hard-stop", true, "block requests when over cap (false = warn only)")
	budgetCmd.AddCommand(budgetSetCmd)
	profilesCmd.AddCommand(profilesListCmd, profilesShowCmd)
}
