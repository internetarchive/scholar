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
var jobID string

var rootCmd = &cobra.Command{
	Use:   "spn",
	Short: "Use the WaybackMachine's SavePageNow API",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		spncfg = &spnclient.SPNConfig{
			AccessKey: viper.GetString("access_key"),
			SecretKey: viper.GetString("secret_key"),
			Endpoint:  viper.GetString("endpoint"),
		}
	},
}

var statusCmd = &cobra.Command{
	Use:   "status <user|system|job>",
	Short: "Call the various /status endpoints",
	RunE: func(cmd *cobra.Command, args []string) error {
		statusType := "system"
		if len(args) > 0 {
			if args[0] != "system" && args[0] != "job" && args[0] != "user" {
				return fmt.Errorf("status type should be system, job, or user")
			}
			statusType = args[0]
		}

		c, err := spnclient.NewDefaultClient(*spncfg)
		if err != nil {
			return err
		}

		var out []byte

		if statusType == "system" {
			res, err := c.StatusSystem()
			if err != nil {
				return err
			}

			out, err = json.MarshalIndent(res, "", "  ")
			if err != nil {
				return fmt.Errorf("could not marshal system status: %w", err)
			}
		} else if statusType == "user" {
			res, err := c.StatusUser()
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
			res, err := c.StatusJob(jobID)
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
	rootCmd.AddCommand(statusCmd)
	statusCmd.Flags().StringVarP(&jobID, "job", "j", "", "job ID")
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
