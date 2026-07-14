// kbart generates the KBART holdings report submitted to the Keepers Registry.
// It consolidates what used to be three scripts (search_fatcat_containers.sh,
// fatcat_kbart.py, convert_sim_kbart.py) into one program, talking to the
// fatcat v2 API and the scholar Elasticsearch endpoint.
package main

import (
	"fmt"
	"os"

	"github.com/internetarchive/scholar/kbart/internal/es"
	"github.com/internetarchive/scholar/kbart/internal/fatcat"
	"github.com/spf13/cobra"
)

// Shared endpoint/concurrency configuration, settable on any subcommand.
var (
	apiHost     string
	esHost      string
	concurrency int
)

var rootCmd = &cobra.Command{
	Use:   appName,
	Short: "Generate the KBART holdings report for the Keepers Registry",
	Long: "kbart selects preserved fatcat containers via Elasticsearch, checks their\n" +
		"eligibility against the fatcat v2 API, converts the IA SIM serials KBART\n" +
		"file, and combines both into the report submitted to the Keepers Registry.",
	SilenceUsage: true,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version and exit",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("%s %s\n", appName, version)
	},
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&apiHost, "api-host", fatcat.DefaultAPIHost, "fatcat v2 API base URL")
	pf.StringVar(&esHost, "es-host", es.DefaultHost, "scholar Elasticsearch base URL")
	pf.IntVar(&concurrency, "concurrency", 12, "max concurrent container fetches")
	rootCmd.AddCommand(versionCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
