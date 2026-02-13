package snapshot

import (
	"fmt"
	"os"
	"os/exec"
)

// SnapshotOaiScrape compacts all files from oai harvests. This will need to
// expand all data once, which may take 1TB or more.
//
// Data point, expanded 1.4TB uncompressed, collecting and writing all data
// takes about 16h; parallel reads from gzip files, fast write storage.  No
// real need to keep all data uncompressed.
func SnapshotOaiScrape(dir string) (string, error) {
	tmpf, err := os.CreateTemp("", "sk-oai-snapshot-*.txt")
	if err != nil {
		return "", err
	}
	defer tmpf.Close()
	c := fmt.Sprintf(`
		find %s -type f -name '*.gz' |
		parallel -j 32 'unpigz -c' |
		sk-oai-records > %s`, dir, tmpf.Name())
	cmd := exec.Command("bash", "-c", c)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("command failed: %s: %w", string(output), err)
	}
	// TODO: run sk-oai-dctojson on the raw record file to generate a JSON
	// version of this dataset
	return tmpf.Name(), nil
}
