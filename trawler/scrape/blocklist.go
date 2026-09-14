package scrape

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

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

// blocklistReloader keeps the last blocklist read from disk so a batch of
// hundreds of URLs re-reads the config file on a TTL rather than once per
// crawl.
//
// Re-reading at all is the point -- see loadBlocklist -- but "on the next
// crawl" used to mean "on the next activity", and an activity was one URL. A
// batch activity runs for hours, so the property has to be kept by something
// other than the activity boundary. A TTL gives it back without putting a file
// read in front of every URL.
type blocklistReloader struct {
	mu       sync.Mutex
	patterns []string
	at       time.Time
	loaded   bool
}

// blocklist is shared by every batch in the worker; the file it reads is too.
var blocklist = &blocklistReloader{}

// get returns the blocklist, re-reading it when the cached copy is older than
// ttl.
//
// The error is only ever from a read that just happened, so a caller that logs
// it hears about a bad config file once per ttl rather than once per URL. The
// patterns are still returned alongside it: loadBlocklist falls back to what
// was loaded at startup, and crawling with a stale blocklist beats crawling
// with none.
func (b *blocklistReloader) get(ttl time.Duration, now time.Time) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.loaded && now.Sub(b.at) < ttl {
		return b.patterns, nil
	}

	patterns, err := loadBlocklist()
	b.patterns = patterns
	b.at = now
	b.loaded = true
	return patterns, err
}
