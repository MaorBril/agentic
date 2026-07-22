package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/maorbril/agentic/internal/store"
	"github.com/maorbril/agentic/internal/tokens"
)

var contextJSON bool

var contextCmd = &cobra.Command{
	Use:   "context [session-id]",
	Short: "Context-fullness trajectory of a session (research view for context scaling)",
	Long: `context shows, request by request, how full the routed model's real
context window was versus what Claude Code's gauge saw. Use it to verify
scaling behavior and to tune context_window / effective_context: sessions
that error or degrade at high fullness argue for a lower effective_context.
Defaults to the most recent session; find ids with 'agentic cost --by session'.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, dataDir, err := loadConfig()
		if err != nil {
			return err
		}
		st, err := store.OpenReadOnly(filepath.Join(dataDir, "agentic.db"))
		if err != nil {
			return fmt.Errorf("no usage recorded yet (%v)", err)
		}
		defer st.Close()

		sessionID := ""
		if len(args) == 1 {
			sessionID = args[0]
		} else {
			if sessionID, err = st.LatestSessionID(); err != nil {
				return err
			}
			if sessionID == "" {
				return fmt.Errorf("no attributed sessions recorded yet")
			}
		}
		traj, err := st.ContextTrajectory(sessionID)
		if err != nil {
			return err
		}
		if len(traj) == 0 {
			return fmt.Errorf("no usage recorded for session %q", sessionID)
		}
		if contextJSON {
			return json.NewEncoder(os.Stdout).Encode(traj)
		}

		fmt.Printf("Session %s — %d requests\n", sessionID, len(traj))
		fmt.Printf("%-6s %-24s %9s %9s %9s  %s\n", "time", "model", "true", "reported", "budget", "fullness")
		var prevTrue int64
		for i, e := range traj {
			marker := ""
			if i > 0 && prevTrue > 0 && e.TrueInput < prevTrue/2 {
				marker = "  ← compacted"
			}
			if e.ErrType != "" {
				marker += fmt.Sprintf("  ✗ %s", e.ErrType)
			}
			fullness := "unscaled"
			if e.CtxBudget > 0 {
				frac := float64(e.TrueInput) / float64(e.CtxBudget)
				fullness = fmt.Sprintf("[%s] %3.0f%%", bar(frac, 12), frac*100)
			}
			fmt.Printf("%-6s %-24s %9s %9s %9s  %s%s\n",
				e.TS.Format("15:04"), e.Model,
				humanTokens(e.TrueInput), humanTokens(e.ReportedInput), humanTokens(int64(e.CtxBudget)),
				fullness, marker)
			prevTrue = e.TrueInput
		}
		fmt.Printf("\nassumed window %s: the client compacts against that; 'true' is what the model really held.\n",
			humanTokens(tokens.AssumedWindow))
		return nil
	},
}

func init() {
	contextCmd.Flags().BoolVar(&contextJSON, "json", false, "machine-readable output")
}
