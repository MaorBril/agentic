package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maorbril/agentic/internal/clauder"
	"github.com/maorbril/agentic/internal/router"
	"github.com/maorbril/agentic/internal/selfupdate"
)

var flagUpdateCheck bool

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update agentic itself to the latest release",
	Long: `Checks GitHub for the latest agentic release and, if newer than the
running binary, downloads it and replaces the current executable in place.

This updates agentic only — Claude Code updates itself independently.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		rel, err := selfupdate.Latest(ctx)
		if err != nil {
			return err
		}

		current := strings.TrimPrefix(router.Version, "v")
		latest := strings.TrimPrefix(rel.TagName, "v")

		if router.Version == "dev" {
			fmt.Fprintln(os.Stderr, "agentic: running a dev build, version comparison skipped")
		} else if current == latest {
			fmt.Printf("agentic %s is already the latest version.\n", router.Version)
			return nil
		}

		fmt.Printf("agentic %s -> %s\n", router.Version, rel.TagName)
		if flagUpdateCheck {
			fmt.Println("Run `agentic update` (without --check) to install.")
			printRestartReport(rel.TagName)
			return nil
		}

		exePath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locating running binary: %w", err)
		}

		tmp := exePath + ".download"
		defer os.Remove(tmp)

		fmt.Println("Downloading...")
		if err := selfupdate.Download(ctx, rel.TagName, tmp); err != nil {
			return err
		}
		if err := selfupdate.Apply(exePath, tmp); err != nil {
			return err
		}

		fmt.Printf("Updated to %s.\n", rel.TagName)
		printRestartReport(rel.TagName)

		// Running sessions host the router in-process; the new binary only
		// takes over when they restart. Tell them via clauder.
		msg := fmt.Sprintf("[agentic] agentic was updated to %s. If this session was launched via `agentic`, "+
			"its router is still running the old version — please tell the user this session should be "+
			"restarted (exit and re-run `agentic`) at a convenient moment to pick up the update.", rel.TagName)
		if n := clauder.Broadcast(msg); n > 0 {
			fmt.Printf("Notified %d running instance(s) via clauder to restart when convenient.\n", n)
		}
		return nil
	},
}

func init() {
	updateCmd.Flags().BoolVar(&flagUpdateCheck, "check", false, "check for an update without installing it")
	rootCmd.AddCommand(updateCmd)
}
