package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/internetarchive/scholar/kbart/internal/kbart"
	"github.com/spf13/cobra"
)

var simOut string

var simCmd = &cobra.Command{
	Use:   "sim <sim-kbart-file> <issn-to-issnl-file>",
	Short: "Convert an IA SIM serials KBART file to the Keepers report format",
	Long: "sim projects an IA SIM (scanned serials) KBART file onto the report's\n" +
		"columns and fills in linking_issn via the given ISSN-to-ISSN-L table. It\n" +
		"replaces convert_sim_kbart.py.",
	Args: cobra.ExactArgs(2),
	RunE: runSim,
}

func init() {
	simCmd.Flags().StringVarP(&simOut, "out", "o", "", "write output to this file instead of stdout")
	rootCmd.AddCommand(simCmd)
}

func runSim(cmd *cobra.Command, args []string) error {
	simPath, issnMapPath := args[0], args[1]

	fmt.Fprintln(os.Stderr, "Loading ISSN map file...")
	m, err := kbart.LoadISSNMap(issnMapPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Got %d ISSN-L mappings.\n", len(m))

	in, err := os.Open(simPath)
	if err != nil {
		return err
	}
	defer in.Close()

	out := os.Stdout
	if simOut != "" {
		f, err := os.Create(simOut)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	w := bufio.NewWriter(out)
	defer w.Flush()

	written, skipped, err := kbart.ConvertSIM(in, m, w)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "sim: %d rows written, %d skipped\n", written, skipped)
	return nil
}
