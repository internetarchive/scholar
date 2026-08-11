package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// Cobra runs only the innermost persistent pre-run hook it finds walking up
// from the executed command unless EnableTraverseRunHooks is set. rootCmd's
// hook is what reads the config file and initializes sentry, so any subcommand
// that defines its own would silently skip both -- and viper then hands back
// zero values instead of failing (an empty temporal.hostport falls back to
// localhost). Assert the invariant rather than the one known offender, so the
// next subcommand to grow a hook trips this instead of production.
func TestPersistentHooksReachRoot(t *testing.T) {
	var withHooks []string

	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.PersistentPreRunE != nil || sub.PersistentPreRun != nil {
				withHooks = append(withHooks, sub.CommandPath())
			}
			walk(sub)
		}
	}
	walk(rootCmd)

	if len(withHooks) == 0 {
		return
	}

	require.True(t, cobra.EnableTraverseRunHooks,
		"%v define persistent pre-run hooks, which shadow rootCmd's config read and sentry init; set cobra.EnableTraverseRunHooks in init()",
		withHooks)
}
