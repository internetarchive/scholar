package daily

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/viper"
)

const (
	urlBlocklistKey        = "crawling.url_blocklist"
	doiPrefixBlocklistKey  = "crawling.doi_prefix_blocklist"
	defaultRulesReloadFreq = 60 * time.Second
)

// crawlRules are the denylists consulted before a release's fulltext is
// crawled: URL substrings for hosts we must not hit, and DOI prefixes for
// publishers whose records are never worth a crawl.
type crawlRules struct {
	URLBlocklist       []string
	DOIPrefixBlocklist []string
}

// loadCrawlRules reads the denylists fresh from the config file, so a pattern
// added part way through a day takes effect on the next crawl without
// restarting the worker or the run. A day of crossref can be hours of
// crawling; noticing a host we should not be hitting and having to restart to
// act on it would be the wrong trade.
//
// It parses the file into a throwaway viper rather than re-reading the process
// global, which only loads the file at startup and would race with the other
// activities calling viper.Get. Same approach as scrape.loadBlocklist and
// periodic.loadPdfSkipRules.
//
// On a read error the values loaded at startup are returned along with the
// error, so catching the file mid-save cannot stop a day.
func loadCrawlRules() (crawlRules, error) {
	fallback := crawlRules{
		URLBlocklist:       viper.GetStringSlice(urlBlocklistKey),
		DOIPrefixBlocklist: viper.GetStringSlice(doiPrefixBlocklistKey),
	}

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

	fresh := crawlRules{
		URLBlocklist:       v.GetStringSlice(urlBlocklistKey),
		DOIPrefixBlocklist: v.GetStringSlice(doiPrefixBlocklistKey),
	}

	// A file that parses but has lost a whole list is far more likely to be a
	// bad edit than a decision to block nothing: these lists carry hosts that
	// asked us not to crawl them and publishers we have agreed to leave alone,
	// so quietly dropping them would start hitting them again. Keep what we had
	// and say so. Emptying a list deliberately still works, it just needs a
	// worker restart.
	var lost []string
	if len(fresh.URLBlocklist) == 0 && len(fallback.URLBlocklist) > 0 {
		fresh.URLBlocklist = fallback.URLBlocklist
		lost = append(lost, urlBlocklistKey)
	}
	if len(fresh.DOIPrefixBlocklist) == 0 && len(fallback.DOIPrefixBlocklist) > 0 {
		fresh.DOIPrefixBlocklist = fallback.DOIPrefixBlocklist
		lost = append(lost, doiPrefixBlocklistKey)
	}
	if len(lost) > 0 {
		return fresh, fmt.Errorf(
			"%v empty in %s but populated at startup; keeping the startup lists", lost, path)
	}

	return fresh, nil
}

// crawlRulesReloader keeps the last denylists read from disk so a batch of
// hundreds of lines re-reads the config file on a TTL rather than once per
// line.
//
// Re-reading at all is the point -- see loadCrawlRules -- but "on the next
// line" used to mean "on the next activity", and an activity was one line. A
// batch activity runs for hours, so the property has to be kept by something
// other than the activity boundary. A TTL gives it back without putting a file
// read in front of every record.
type crawlRulesReloader struct {
	mu     sync.Mutex
	rules  crawlRules
	at     time.Time
	loaded bool
}

// blocklists is shared by every batch in the worker; the file it reads is too.
var blocklists = &crawlRulesReloader{}

// get returns the denylists, re-reading them when the cached copy is older
// than ttl.
//
// The error is only ever from a read that just happened, so a caller that logs
// it hears about a bad config file once per ttl rather than once per line. The
// rules are still returned alongside it: loadCrawlRules falls back to what was
// loaded at startup, and crawling with a stale blocklist beats crawling with
// none.
func (c *crawlRulesReloader) get(ttl time.Duration, now time.Time) (crawlRules, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.loaded && now.Sub(c.at) < ttl {
		return c.rules, nil
	}

	rules, err := loadCrawlRules()
	c.rules = rules
	c.at = now
	c.loaded = true
	return rules, err
}
