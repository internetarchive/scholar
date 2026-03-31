package main

import (
	"fmt"
	"log"
	"os"

	"git.archive.org/webgroup/scholar/trawler/cmd/arxivcmd"
	"git.archive.org/webgroup/scholar/trawler/cmd/crossrefcmd"
	"git.archive.org/webgroup/scholar/trawler/cmd/datacitecmd"
	"git.archive.org/webgroup/scholar/trawler/cmd/doajcmd"
	"git.archive.org/webgroup/scholar/trawler/cmd/indexcmd"
	"git.archive.org/webgroup/scholar/trawler/cmd/pubmedcmd"
	"github.com/getsentry/sentry-go"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var version = "dev"
var cfgFile string

func main() {
	rootCmd.Execute()
}

var rootCmd = &cobra.Command{
	Use:   "trawler",
	Short: "control CLI for scholar trawler",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if err := viper.ReadInConfig(); err != nil {
			if _, ok := err.(viper.ConfigFileNotFoundError); ok {
				return fmt.Errorf("config file not found")
			} else {
				if err != nil { // Handle errors reading the config file
					return fmt.Errorf("fatal error config file: %w", err)
				}
			}
		}
		log.Printf("trawler version %s", version)
		err := sentry.Init(sentry.ClientOptions{
			Dsn: viper.GetString("sentry.dsn"),
		})

		if err != nil {
			return fmt.Errorf("could not initialize sentry: %w", err)
		}

		return nil
	},
	Version: version,
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file override")

	rootCmd.AddCommand(arxivcmd.Cmd)
	rootCmd.AddCommand(crossrefcmd.Cmd)
	rootCmd.AddCommand(datacitecmd.Cmd)
	rootCmd.AddCommand(doajcmd.Cmd)
	rootCmd.AddCommand(indexcmd.IndexCmd)
	rootCmd.AddCommand(pubmedcmd.Cmd)
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.SetConfigName("config")
		viper.SetConfigType("toml")
		viper.AddConfigPath("/etc/trawler/")
		viper.AddConfigPath("$HOME/.config/trawler")
		viper.AddConfigPath(".")
	}
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

/*

 How should this be structured? Need to be able to start workers as well as cronjobs
 upstream feed readers are cronjobs which means we need two minimum
 processes: a starter and a worker. The starter executes a workflow according
 to a cron schedule and that workflow in turn executes a child workflow on a
 given chunk of upstream metadata.
 so to start with crossref, we want a worker process on svc263. The starter
 process can be anywhere. I think I want to limit the things on 263 to only
 just that which needs internet access. I can consolidate all the starters on
 a single host. So from a CLI perspective that means different affordances
 for starter vs. worker.

 my first thought is to have a domain specific invocation pattern:

 svc519: trawler crossref --starter
 svc263: trawler crossref --worker

 or to generalize

 svc519: trawler --upstream=crossref --type=starter --cron="..."
 svc519: trawler --upstream=pubmed --type=starter --cron="..."
 svc263: trawler --upstream=pubmed --type=worker
 svc263: trawler --upstream=pubmed --type=worker

 what else might trawler need to do? look at the drawing. all of its
 behaviors are downstream of an API read so let's assume that trawler is for
 the invocation of a single process at a time -- either a starter or a
 worker, to start.
 we *absolutely* want to be able to do one off things but I think that all
 can go into subcommands. Things like "one-off journal crawl" or "ingest this
 metadata on disk"

 so for right now I can just do a single root command influenced by flags.

 I feel somewhat emboldened by my pleasant viper experience so can imagine
 also a config file like:

 [upstream.crossref]
 cron = * 1 * * *
 apikey = "..."

 [upstream.pubmed]
 cron = "..."
 creds = "..."

 I like that such a config could be written out with ansible instead of
 having to rely on templatized systemd unit files.

 Alternative thought: flags are bad for discoverability; could have a command per upstream with a flavor switch:

 svc519: trawler crossref --flavor=starter
 svc263: trawler crossref --flavor-worker

 I like this because it allows for better documentation of each upstream flavor. Of course, though, I next want to bucket these all under a "crawl" or "trawl" namespace:

 svc519: trawler trawl crossref --flavor=starter
 svc263: trawler trawl crossref --flavor=worker

 ooh, how about "start"?

 svc519: trawler start crossref --flavor=starter
 svc263: trawler start crossref --flavor=worker

 some other ideas

 upstream -> verb

 svc519: trawler crossref start --flavor=starter
 svc263: trawler crossref start --flavor=worker

 this opens the door to other commands, perhaps things like:

 - trawler crossref stats
 - trawler crossref schema
 - trawler crossref ingest

 this feels best to be probably because it mirrors the github CLI. i never
 really had an issue with this type of setup and felt it extended well and
 allowed for good docs.


*/
