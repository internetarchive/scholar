package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"git.archive.org/webgroup/scholar/trawler/spn/spnclient"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var spncfg *spnclient.SPNConfig = nil
var client spnclient.Client = nil
var jobID string
var debug bool

// for holding save flags
var saveReq = spnclient.SaveRequest{}

var rootCmd = &cobra.Command{
	Use:   "spn",
	Short: "Use the WaybackMachine's SavePageNow API",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) (err error) {
		spncfg = &spnclient.SPNConfig{
			AccessKey: viper.GetString("access_key"),
			SecretKey: viper.GetString("secret_key"),
			Endpoint:  viper.GetString("endpoint"),
			Debug:     debug,
		}

		client, err = spnclient.NewDefaultClient(*spncfg)

		return
	},
}

var saveCmd = &cobra.Command{
	Use:   "save [options] <url>",
	Short: "Request a capture from SPN",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		saveReq.URL = args[0]
		res, err := client.Save(saveReq)
		if err != nil {
			return err
		}

		out, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return fmt.Errorf("could not marshal capture result: %w", err)
		}

		fmt.Println(string(out))

		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status <user|system|job>",
	Short: "Call the various /status endpoints",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		statusType := "system"
		if len(args) > 0 {
			if args[0] != "system" && args[0] != "job" && args[0] != "user" {
				return fmt.Errorf("status type should be system, job, or user")
			}
			statusType = args[0]
		}

		var out []byte

		if statusType == "system" {
			res, err := client.StatusSystem()
			if err != nil {
				return err
			}

			out, err = json.MarshalIndent(res, "", "  ")
			if err != nil {
				return fmt.Errorf("could not marshal system status: %w", err)
			}
		} else if statusType == "user" {
			res, err := client.StatusUser()
			if err != nil {
				return err
			}
			out, err = json.MarshalIndent(res, "", "  ")
			if err != nil {
				return fmt.Errorf("could not marshal user status: %w", err)
			}
		} else if statusType == "job" {
			if jobID == "" {
				return errors.New("--job required")
			}
			res, err := client.StatusJob(jobID)
			if err != nil {
				return err
			}
			out, err = json.MarshalIndent(res, "", "  ")
			if err != nil {
				return fmt.Errorf("could not marshal job status: %w", err)
			}
		} else {
			panic("unreachable")
		}

		fmt.Println(string(out))

		return nil
	},
}

func init() {
	viper.SetConfigName("spn")
	viper.SetConfigType("toml")
	viper.AddConfigPath("$HOME/.config/")
	viper.SetEnvPrefix("SPNCLIENT")
	viper.AutomaticEnv()

	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "debug mode")
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(saveCmd)

	statusCmd.Flags().StringVarP(&jobID, "job", "j", "", "job ID")

	saveCmd.Flags().BoolVar(&saveReq.CaptureAll, "capture-all", false, "Capture non-200 responses")
	saveCmd.Flags().BoolVar(&saveReq.CaptureScreenshot, "capture-screenshot", false, "Capture a screenshot")
	saveCmd.Flags().BoolVar(&saveReq.CaptureOutlinks, "capture-outlinks", false, "Capture all outlinks, too")
	saveCmd.Flags().BoolVar(&saveReq.DelayWBAvailability, "delay-wb", false, "Delay appearance of capture in WaybackMachine")
	saveCmd.Flags().BoolVar(&saveReq.ForceGet, "force-get", false, "Use simple GET instead of headless browser")
	saveCmd.Flags().BoolVar(&saveReq.SkipFirstArchive, "skip-first-archive", false, "Skip check for whether or not this is the first capture for a URL")
	saveCmd.Flags().BoolVar(&saveReq.OutlinksAvailability, "outlinks-availability", false, "Include details about outlink captures")
	saveCmd.Flags().BoolVar(&saveReq.EmailResult, "email", false, "Send email with results")
	saveCmd.Flags().StringVar(&saveReq.CaptureCookie, "cookie", "", "Cookie to use when capturing")
	saveCmd.Flags().StringVar(&saveReq.UserAgent, "user-agent", "", "User-Agent to use when capturing")
	saveCmd.Flags().StringVar(&saveReq.TargetUsername, "target-username", "", "Login username to use when capturing")
	saveCmd.Flags().StringVar(&saveReq.TargetPassword, "target-password", "", "Login password to use when capturing")
	saveCmd.Flags().BoolVar(&saveReq.DelayForJavascript, "js-delay", false, "Wait for javascript to settle during a capture")
	saveCmd.Flags().IntVar(&saveReq.JavascriptTimeout, "js-timeout", 5, "How long to wait for javascript to settle")
	saveCmd.Flags().IntVar(&saveReq.IfNotArchivedWithinSecs, "not-archived-within", 0, "seconds within which to consider an existing capture recent enough")
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
