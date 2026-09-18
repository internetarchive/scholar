package daily

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// writeConfig points the process-global viper at a config file on disk, the
// way main.go does at startup, and returns its path. The file is named
// "config" with no extension to match how it is actually deployed -- that is
// the case viper cannot sniff a format from.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	viper.Reset()
	viper.SetConfigFile(path)
	viper.SetConfigType("toml")
	require.NoError(t, viper.ReadInConfig())
	t.Cleanup(viper.Reset)
	return path
}

func rulesConfig(urls, dois []string) string {
	s := "[crawling]\nurl_blocklist = [\n"
	for _, p := range urls {
		s += "  \"" + p + "\",\n"
	}
	s += "]\ndoi_prefix_blocklist = [\n"
	for _, p := range dois {
		s += "  \"" + p + "\",\n"
	}
	return s + "]\n"
}

// The whole point: a pattern added to the config file while a day is running
// is visible to the next crawl, without restarting anything.
func Test_loadCrawlRules_seesEditsBetweenCalls(t *testing.T) {
	path := writeConfig(t, rulesConfig([]string{"//first.example/"}, []string{"10.1/"}))

	got, err := loadCrawlRules()
	require.NoError(t, err)
	require.Equal(t, []string{"//first.example/"}, got.URLBlocklist)
	require.Equal(t, []string{"10.1/"}, got.DOIPrefixBlocklist)

	// operator appends a host and a publisher mid-run
	require.NoError(t, os.WriteFile(path, []byte(rulesConfig(
		[]string{"//first.example/", "//second.example/"},
		[]string{"10.1/", "10.2/"})), 0o644))

	got, err = loadCrawlRules()
	require.NoError(t, err)
	require.Equal(t, []string{"//first.example/", "//second.example/"}, got.URLBlocklist)
	require.Equal(t, []string{"10.1/", "10.2/"}, got.DOIPrefixBlocklist)

	// and the process global is deliberately untouched by the re-read, which is
	// what keeps this safe to call from concurrent batches
	require.Equal(t, []string{"//first.example/"}, viper.GetStringSlice(urlBlocklistKey))
}

// Removing a pattern takes effect too; this is not append-only.
func Test_loadCrawlRules_seesRemovals(t *testing.T) {
	path := writeConfig(t, rulesConfig(
		[]string{"//a.example/", "//b.example/"}, []string{"10.1/", "10.2/"}))

	require.NoError(t, os.WriteFile(path,
		[]byte(rulesConfig([]string{"//a.example/"}, []string{"10.1/"})), 0o644))
	got, err := loadCrawlRules()
	require.NoError(t, err)
	require.Equal(t, []string{"//a.example/"}, got.URLBlocklist)
	require.Equal(t, []string{"10.1/"}, got.DOIPrefixBlocklist)
}

func Test_loadCrawlRules_noConfigFile(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(urlBlocklistKey, []string{"//startup.example/"})
	viper.Set(doiPrefixBlocklistKey, []string{"10.9/"})

	got, err := loadCrawlRules()
	require.Error(t, err)
	require.Contains(t, err.Error(), "no config file")
	require.Equal(t, []string{"//startup.example/"}, got.URLBlocklist,
		"must fall back to what was loaded at startup")
	require.Equal(t, []string{"10.9/"}, got.DOIPrefixBlocklist)
}

// Catching the file mid-save must not stop a day.
func Test_loadCrawlRules_unparseableConfigFallsBack(t *testing.T) {
	path := writeConfig(t, rulesConfig([]string{"//startup.example/"}, []string{"10.9/"}))
	require.NoError(t, os.WriteFile(path, []byte("[crawling\nurl_blocklist = ["), 0o644))

	got, err := loadCrawlRules()
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to re-read config")
	require.Equal(t, []string{"//startup.example/"}, got.URLBlocklist)
	require.Equal(t, []string{"10.9/"}, got.DOIPrefixBlocklist)
}

// A file that parses but has lost a whole list is far likelier to be a bad
// edit than an intent to block nothing: these lists carry hosts that asked us
// not to crawl them and publishers we have agreed to leave alone.
func Test_loadCrawlRules_emptiedListKeepsStartupValues(t *testing.T) {
	path := writeConfig(t, rulesConfig([]string{"//noicrawl.example/"}, []string{"10.9/"}))
	require.NoError(t, os.WriteFile(path, []byte("[crawling]\n"), 0o644))

	got, err := loadCrawlRules()
	require.Error(t, err)
	require.Contains(t, err.Error(), "keeping the startup lists")
	require.Equal(t, []string{"//noicrawl.example/"}, got.URLBlocklist)
	require.Equal(t, []string{"10.9/"}, got.DOIPrefixBlocklist)
}

// One list emptied does not drag the other into the fallback.
func Test_loadCrawlRules_emptiedListIsIndependent(t *testing.T) {
	path := writeConfig(t, rulesConfig([]string{"//noicrawl.example/"}, []string{"10.9/"}))
	require.NoError(t, os.WriteFile(path,
		[]byte(rulesConfig(nil, []string{"10.9/", "10.8/"})), 0o644))

	got, err := loadCrawlRules()
	require.ErrorContains(t, err, urlBlocklistKey)
	require.NotContains(t, err.Error(), doiPrefixBlocklistKey)
	require.Equal(t, []string{"//noicrawl.example/"}, got.URLBlocklist)
	require.Equal(t, []string{"10.9/", "10.8/"}, got.DOIPrefixBlocklist)
}

// But genuinely empty lists at startup stay empty without complaint.
func Test_loadCrawlRules_emptyBothWaysIsFine(t *testing.T) {
	writeConfig(t, "[crawling]\n")

	got, err := loadCrawlRules()
	require.NoError(t, err)
	require.Empty(t, got.URLBlocklist)
	require.Empty(t, got.DOIPrefixBlocklist)
}

// The deployed config is named "config" with no extension; viper cannot infer
// a format from that, so the loader must say so itself.
func Test_loadCrawlRules_extensionlessConfigFile(t *testing.T) {
	writeConfig(t, rulesConfig([]string{"//x.example/"}, nil))
	require.Empty(t, filepath.Ext(viper.ConfigFileUsed()))

	got, err := loadCrawlRules()
	require.NoError(t, err)
	require.Equal(t, []string{"//x.example/"}, got.URLBlocklist)
}

// A batch of hundreds of lines must not put a file read in front of every one
// of them, but it must not run for hours on the list it started with either.
func Test_crawlRulesReloader_cachesUntilTTL(t *testing.T) {
	path := writeConfig(t, rulesConfig([]string{"//first.example/"}, nil))

	r := &crawlRulesReloader{}
	now := time.Now()

	got, err := r.get(time.Minute, now)
	require.NoError(t, err)
	require.Equal(t, []string{"//first.example/"}, got.URLBlocklist)

	require.NoError(t, os.WriteFile(path,
		[]byte(rulesConfig([]string{"//second.example/"}, nil)), 0o644))

	// Inside the TTL the edit is not picked up...
	got, err = r.get(time.Minute, now.Add(30*time.Second))
	require.NoError(t, err)
	require.Equal(t, []string{"//first.example/"}, got.URLBlocklist)

	// ...and past it, it is.
	got, err = r.get(time.Minute, now.Add(90*time.Second))
	require.NoError(t, err)
	require.Equal(t, []string{"//second.example/"}, got.URLBlocklist)
}
