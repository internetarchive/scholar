package scrape

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/viper"
)

const urlBlocklistKey = "crawling.url_blocklist"

// loadBlocklist reads the crawl URL blocklist fresh from the config file, so a
// pattern added part way through a scrape takes effect on the next crawl
// without restarting the worker or the run. A scrape can be walking millions of
// URLs for days; noticing a host we should not be hitting and having to restart
// to act on it would be the wrong trade.
//
// It parses the file into a throwaway viper rather than re-reading the process
// global, which only loads the file at startup and would race with the other
// activities calling viper.Get. Same approach as periodic.loadPdfSkipRules.
//
// On a read error the values loaded at startup are returned along with the
// error, so catching the file mid-save cannot stop a scrape.
func loadBlocklist() ([]string, error) {
	fallback := viper.GetStringSlice(urlBlocklistKey)

	path := viper.ConfigFileUsed()
	if path == "" {
		return fallback, errors.New("no config file was loaded at startup")
	}

	v := viper.New()
	v.SetConfigFile(path)
	if filepath.Ext(path) == "" {
		// the deployed config file is named "config" with no extension, so viper
		// cannot sniff a format from it; main.go tells the global the same thing.
		v.SetConfigType("toml")
	}
	if err := v.ReadInConfig(); err != nil {
		return fallback, fmt.Errorf("failed to re-read config %s: %w", path, err)
	}

	fresh := v.GetStringSlice(urlBlocklistKey)

	// A file that parses but has lost the whole list is far more likely to be a
	// bad edit than a decision to block nothing: the list carries hosts that
	// asked us not to crawl them, so quietly dropping all of it would start
	// hitting them again. Keep what we had and say so. Emptying the list
	// deliberately still works, it just needs a worker restart.
	if len(fresh) == 0 && len(fallback) > 0 {
		return fallback, fmt.Errorf(
			"%s is empty in %s but had %d patterns at startup; keeping the startup list",
			urlBlocklistKey, path, len(fallback))
	}

	return fresh, nil
}
