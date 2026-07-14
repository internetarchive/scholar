package main

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

var combineOut string

var combineCmd = &cobra.Command{
	Use:   "combine <file1> <file2> [file...]",
	Short: "Concatenate KBART TSV files into one, keeping a single header",
	Long: "combine writes the first file verbatim, then appends every later file with\n" +
		"its header line stripped, producing the combined report submitted to the\n" +
		"Keepers Registry. Bytes (including CRLF line endings) are preserved.",
	Args: cobra.MinimumNArgs(2),
	RunE: runCombine,
}

func init() {
	combineCmd.Flags().StringVarP(&combineOut, "out", "o", "", "write output to this file instead of stdout")
	rootCmd.AddCommand(combineCmd)
}

func runCombine(cmd *cobra.Command, args []string) error {
	out := os.Stdout
	if combineOut != "" {
		f, err := os.Create(combineOut)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	w := bufio.NewWriter(out)
	defer w.Flush()
	return combineFiles(args, w)
}

// combineFiles copies paths[0] in full, then each subsequent file with its first
// line (the header) dropped.
func combineFiles(paths []string, out io.Writer) error {
	for i, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		if i == 0 {
			_, err = io.Copy(out, f)
		} else {
			br := bufio.NewReader(f)
			// Discard the header line; ReadString consumes the trailing \n (and
			// the preceding \r of a CRLF terminator with it).
			if _, e := br.ReadString('\n'); e != nil && e != io.EOF {
				f.Close()
				return e
			}
			_, err = io.Copy(out, br)
		}
		f.Close()
		if err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "combine: merged %d files\n", len(paths))
	return nil
}
