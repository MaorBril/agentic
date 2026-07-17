// Package cmd is the agentic CLI.
package cmd

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/maorbril/agentic/internal/config"
	"github.com/maorbril/agentic/internal/launch"
	"github.com/maorbril/agentic/internal/router"
)

var (
	flagProfile     string
	flagModel       string
	flagName        string
	flagNoClauder   bool
	flagPassthrough bool
)

var rootCmd = &cobra.Command{
	Use:   "agentic [flags] [-- claude args...]",
	Short: "Multi-model, cost-controlled harness wrapping Claude Code",
	Long: `agentic launches Claude Code through a local router that can serve
Anthropic, OpenAI, xAI, and open-weight models, with budgets and spend
tracking. Everything after -- is passed to claude verbatim.`,
	Version:       router.Version,
	SilenceUsage:  true,
	SilenceErrors: true,
	Args:          cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, dataDir, err := loadConfig()
		if err != nil {
			return err
		}
		claudeArgs := args
		if at := cmd.ArgsLenAtDash(); at >= 0 {
			claudeArgs = args[at:]
		}
		return launch.Run(cmd.Context(), cfg, dataDir, launch.Options{
			Profile:      flagProfile,
			ModelFlag:    flagModel,
			InstanceName: flagName,
			NoClauder:    flagNoClauder,
			Passthrough:  flagPassthrough,
			ClaudeArgs:   claudeArgs,
		}, logger())
	},
}

func init() {
	rootCmd.Flags().StringVarP(&flagProfile, "profile", "p", "", "profile from ~/.agentic/config.yaml")
	rootCmd.Flags().StringVar(&flagModel, "model", "", "one-shot main-model alias override")
	rootCmd.Flags().StringVar(&flagName, "name", "", "instance name (forwarded to clauder)")
	rootCmd.Flags().BoolVar(&flagNoClauder, "no-clauder", false, "launch bare claude even if clauder is installed")
	rootCmd.Flags().BoolVar(&flagPassthrough, "passthrough", false, "skip the router (subscription billing, no tracking)")
	rootCmd.AddCommand(routerCmd, costCmd, modelsCmd, setupCmd)
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "agentic:", err)
		os.Exit(1)
	}
}

func loadConfig() (*config.Config, string, error) {
	dataDir, err := config.DataDir()
	if err != nil {
		return nil, "", err
	}
	cfg, err := config.Load()
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("no config found — run `agentic setup` first")
	}
	if err != nil {
		return nil, "", err
	}
	return cfg, dataDir, nil
}

func logger() *slog.Logger {
	dataDir, err := config.DataDir()
	if err == nil {
		if f, err := os.OpenFile(filepath.Join(dataDir, "router.log"),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			return slog.New(slog.NewTextHandler(f, nil))
		}
	}
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}
