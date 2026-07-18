package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/maorbril/agentic/internal/config"
	"github.com/maorbril/agentic/internal/pricing"
	"github.com/maorbril/agentic/internal/router"
	"github.com/maorbril/agentic/internal/store"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose the agentic installation",
	RunE: func(cmd *cobra.Command, args []string) error {
		fail := 0
		check := func(ok bool, good, bad string) {
			if ok {
				fmt.Println("✓", good)
			} else {
				fail++
				fmt.Println("✗", bad)
			}
		}

		if !ensureClaude() {
			fail++
		}

		cfg, cfgErr := config.Load()
		if errors.Is(cfgErr, os.ErrNotExist) {
			check(false, "", "no config — run `agentic setup`")
			return fmt.Errorf("%d problem(s)", fail)
		}
		check(cfgErr == nil, "config parses", fmt.Sprintf("config invalid: %v", cfgErr))
		if cfgErr != nil {
			return fmt.Errorf("%d problem(s)", fail)
		}
		dataDir, _ := config.DataDir()

		for name, p := range cfg.Providers {
			if p.APIKeyEnv == "" || p.APIKey != "" {
				continue
			}
			check(p.Key() != "",
				fmt.Sprintf("provider %s: %s available", name, p.APIKeyEnv),
				fmt.Sprintf("provider %s: %s not in the environment or ~/.agentic/env", name, p.APIKeyEnv))
		}

		st, err := store.Open(filepath.Join(dataDir, "agentic.db"))
		check(err == nil, "spend database writable", fmt.Sprintf("spend database: %v", err))
		if err == nil {
			st.Close()
		}

		prices := pricing.Load(dataDir, cfg)
		for alias, m := range cfg.Models {
			check(prices.Has(m.ID),
				fmt.Sprintf("model %s (%s) priced", alias, m.ID),
				fmt.Sprintf("model %s (%s) has no pricing — spend will be untracked; add `pricing:` in config or run `agentic models update-prices`", alias, m.ID))
		}

		if d, err := router.ReadDiscovery(dataDir); err == nil {
			fmt.Printf("· router leader: pid %d on port %d (version %s)\n", d.PID, d.Port, d.Version)
		} else {
			fmt.Println("· no router running (starts automatically with the next session)")
		}

		ensureClauder()

		home, _ := os.UserHomeDir()
		if data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json")); err == nil {
			var s struct {
				StatusLine struct {
					Command string `json:"command"`
				} `json:"statusLine"`
			}
			json.Unmarshal(data, &s)
			check(s.StatusLine.Command == "agentic statusline",
				"statusline registered",
				"statusline not registered — run `agentic setup`")
		}

		if os.Getenv("ANTHROPIC_API_KEY") != "" {
			hasPassthrough := false
			for _, p := range cfg.Profiles {
				if p.Passthrough {
					hasPassthrough = true
				}
			}
			if hasPassthrough {
				fmt.Println("· note: ANTHROPIC_API_KEY is set globally — passthrough profiles will bill the key, not your subscription")
			}
		}

		if fail > 0 {
			return fmt.Errorf("%d problem(s) found", fail)
		}
		fmt.Println("\nAll checks passed.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
