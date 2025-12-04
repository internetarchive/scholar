package main

import (
	"fmt"
	"os"
	"time"

	"git.archive.org/webgroup/scholar/trawler/cdx/cdxclient"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfg *cdxclient.Config = nil
var client cdxclient.Client
var debug bool
var from string
var to string

const timeFormat = "20060102150405"

// for holding query flags
var queryP = cdxclient.QueryParams{
	Output: "json",
}

var rootCmd = &cobra.Command{
	Use:   "cdx",
	Short: "Use the WaybackMachine's CDX API",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) (err error) {
		cfg = &cdxclient.Config{
			Endpoint:  viper.GetString("endpoint"),
			Auth:      viper.GetString("auth"),
			UserAgent: viper.GetString("user_agent"),
			Retries:   viper.GetInt("retries"),
			Backoff:   viper.GetDuration("backoff"),
			Debug:     debug,
		}
		client = cdxclient.NewClient(*cfg)
		return nil
	},
}

var queryCmd = &cobra.Command{
	Use:   "query [options] <url>",
	Short: "send a query to the CDX API",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		queryP.URL = args[0]

		if from != "" {
			ft, err := time.Parse(timeFormat, from)
			if err != nil {
				return fmt.Errorf("expected from in format %s, got '%s'", timeFormat, from)
			}
			queryP.From = &ft
		}

		if to != "" {
			tt, err := time.Parse(timeFormat, to)
			if err != nil {
				return fmt.Errorf("expected to in format %s, got '%s'", timeFormat, to)
			}
			queryP.To = &tt
		}

		rows, err := client.Query(queryP)
		if err != nil {
			return err
		}

		if !cdxclient.ValidMatchType(queryP.MatchType) {
			return fmt.Errorf("invalid match type '%s'", queryP.MatchType)
		}

		// TODO gracefully handle unauthed case

		if len(rows) > 0 {
			fmt.Println("surt\ttimestamp\turl\tmimetype\tstatus\tdigest\twarc csize\twarc offset\twarc path")
		}

		for _, row := range rows {
			dt := row.Datetime.Format(timeFormat)
			fmt.Printf("%s\t%s\t%s\t%s\t%d\t%s\t%d\t%d\t%s\n",
				row.Surt, dt, row.URL, row.Mimetype, row.StatusCode, row.SHA1b32, row.WarcCsize,
				row.WarcOffset, row.WarcPath)
		}

		return nil
	},
}

func init() {
	viper.SetConfigName("cdx")
	viper.SetConfigType("toml")
	viper.AddConfigPath("$HOME/.config/")
	viper.SetEnvPrefix("CDXCLIENT")
	viper.AutomaticEnv()

	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "debug mode")
	rootCmd.AddCommand(queryCmd)

	queryCmd.Flags().StringVar(&from, "from", "", fmt.Sprintf("from timestamp in format %s", timeFormat))
	queryCmd.Flags().StringVar(&to, "to", "", fmt.Sprintf("to timestamp in format %s", timeFormat))
	queryCmd.Flags().StringVarP(&queryP.MatchType, "match-type", "t", "exact",
		fmt.Sprintf("match type: %v", cdxclient.MatchTypes))
	queryCmd.Flags().IntVarP(&queryP.Limit, "limit", "l", 10, "limit on result rows")
	queryCmd.Flags().StringSliceVarP(&queryP.Filters, "filter", "f", []string{}, "filters to apply")
}

func main() {
	if err := viper.ReadInConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "could not read config: %s", err.Error())
		os.Exit(2)
	}
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

/*

expose as flags etc

type CDXParams struct {
	URL       string
	From      *time.Time
	To        *time.Time
	MatchType string
	Limit     int
	Output    string
	Filters   []string
}
*/
