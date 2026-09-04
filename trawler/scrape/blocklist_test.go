package scrape

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// writeConfig points the process-global viper at a config file on disk, the way
// main.go does at startup, and returns its path. The file is named "config"
// with no extension to match how it is actually deployed -- that is the case
// viper cannot sniff a format from.
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

func blocklistConfig(patterns ...string) string {
	s := "[crawling]\nurl_blocklist = [\n"
	for _, p := range patterns {
		s += "  \"" + p + "\",\n"
	}
	return s + "]\n"
}

// The whole point: a pattern added to the config file while a scrape is running
// is visible to the next crawl, without restarting anything.
func Test_loadBlocklist_seesEditsBetweenCalls(t *testing.T) {
	path := writeConfig(t, blocklistConfig("//first.example/"))

	got, err := loadBlocklist()
	require.NoError(t, err)
	require.Equal(t, []string{"//first.example/"}, got)

	// operator appends a host mid-run
	require.NoError(t, os.WriteFile(path,
		[]byte(blocklistConfig("//first.example/", "//second.example/")), 0o644))

	got, err = loadBlocklist()
	require.NoError(t, err)
	require.Equal(t, []string{"//first.example/", "//second.example/"}, got)

	// and the process global is deliberately untouched by the re-read, which is
	// what keeps this safe to call from concurrent activities
	require.Equal(t, []string{"//first.example/"}, viper.GetStringSlice(urlBlocklistKey))
}

// Removing a pattern takes effect too; this is not append-only.
func Test_loadBlocklist_seesRemovals(t *testing.T) {
	path := writeConfig(t, blocklistConfig("//a.example/", "//b.example/"))

	require.NoError(t, os.WriteFile(path, []byte(blocklistConfig("//a.example/")), 0o644))
	got, err := loadBlocklist()
	require.NoError(t, err)
	require.Equal(t, []string{"//a.example/"}, got)
}

func Test_loadBlocklist_noConfigFile(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(urlBlocklistKey, []string{"//startup.example/"})

	got, err := loadBlocklist()
	require.Error(t, err)
	require.Contains(t, err.Error(), "no config file")
	require.Equal(t, []string{"//startup.example/"}, got,
		"must fall back to what was loaded at startup")
}

// Catching the file mid-save must not stop a scrape.
func Test_loadBlocklist_unparseableConfigFallsBack(t *testing.T) {
	path := writeConfig(t, blocklistConfig("//startup.example/"))
	require.NoError(t, os.WriteFile(path, []byte("[crawling\nurl_blocklist = ["), 0o644))

	got, err := loadBlocklist()
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to re-read config")
	require.Equal(t, []string{"//startup.example/"}, got)
}

// A file that parses but has lost the entire list is far likelier to be a bad
// edit than an intent to block nothing, and the list carries hosts that asked
// us not to crawl them. Keep the startup list and complain.
func Test_loadBlocklist_emptiedListKeepsStartupValues(t *testing.T) {
	path := writeConfig(t, blocklistConfig("//noicrawl.example/"))
	require.NoError(t, os.WriteFile(path, []byte("[crawling]\n"), 0o644))

	got, err := loadBlocklist()
	require.Error(t, err)
	require.Contains(t, err.Error(), "keeping the startup list")
	require.Equal(t, []string{"//noicrawl.example/"}, got)
}

// But a genuinely empty blocklist at startup stays empty without complaint.
func Test_loadBlocklist_emptyBothWaysIsFine(t *testing.T) {
	writeConfig(t, "[crawling]\n")

	got, err := loadBlocklist()
	require.NoError(t, err)
	require.Empty(t, got)
}

// The deployed config is named "config" with no extension; viper cannot infer a
// format from that, so the loader must say so itself.
func Test_loadBlocklist_extensionlessConfigFile(t *testing.T) {
	writeConfig(t, blocklistConfig("//x.example/"))
	require.Empty(t, filepath.Ext(viper.ConfigFileUsed()))

	got, err := loadBlocklist()
	require.NoError(t, err)
	require.Equal(t, []string{"//x.example/"}, got)
}
