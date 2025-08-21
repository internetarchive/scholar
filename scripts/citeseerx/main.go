package main

/*
This script operates on sha1s gleaned from a comparison of citeseerx's sitemap with fatcat's file records.

the actual file consumed here was produced by this sql on fatcat's prod db:

fatcat_prod=# create table temp_citeseer_sha1s (sha1 text);
fatcat_prod=# \copy temp_citeseer_sha1s (sha1) from '/home/nsmith/common_citeseer.txt';
fatcat_prod=# create index sigh on temp_citeseer_sha1s (sha1);
fatcat_prod=# COPY (select c.sha1, fru.url from file_rev_url fru join file_rev fr on fr.id = fru.file_rev join temp_citeseer_sha1s c on c.sha1 = fr.sha1 where fru.url like '%archive.org%') TO '/home/nsmith/cs_sha1_urls.tsv' WITH (FORMAT CSV, DELIMITER E'\t');

the purpose of this script is to handle the selection of a single wayback machine URL for a given sha1 since we record multiple urls for many files.

*/

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	tsvPath  = "/home/vilmibm/src/work/scratch/cs_sha1_urls.tsv"
	outPath  = "./data/citeseerx_wbm.tsv"
	skipPath = "./data/skipped_sha1.txt"
)

func csurl(sha1 string) string {
	return fmt.Sprintf("https://citeseerx.ist.psu.edu/document?repid=rep1&type=pdf&doi=%s", sha1)
}

func _main() error {
	f, err := os.Open(tsvPath)
	if err != nil {
		return err
	}
	defer f.Close()

	outf, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer outf.Close()

	skipf, err := os.Create(skipPath)
	if err != nil {
		return err
	}
	defer skipf.Close()

	seen := map[string][]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		fields := strings.Split(line, "\t")
		sha1 := fields[0]
		if len(sha1) != 40 {
			return fmt.Errorf("bad sha1: %s", sha1)
		}
		url := ""
		if len(fields) > 1 {
			url = fields[1]
		}
		var urls []string
		var ok bool

		if urls, ok = seen[sha1]; !ok {
			urls = []string{}
		}

		if url != "" && !strings.Contains(url, "/web/None/") {
			urls = append(urls, url)
		}

		seen[sha1] = urls
	}

	err = s.Err()
	if err != nil {
		return err
	}

	fmt.Println(len(seen))

	for sha1, urls := range seen {
		if len(urls) == 0 {
			fmt.Fprintln(skipf, sha1)
			continue
		}
		sort.Slice(urls, func(i, j int) bool {
			// TODO consider extracting timestamp and sorting with that
			// TODO assuming that https is constant
			return urls[i] > urls[j]
		})
		fmt.Fprintf(outf, "%s\t%s\t%s\n", sha1, csurl(sha1), urls[0])
	}

	// TODO the skipf is not used because sha1s missing urls just don't seem to end up in our sql's out
	// TODO we are wondering if citeseerx is just in wayback (ie fatcat not version of record for this); might have to run a spark job like i did with gifcities

	return nil
}

func main() {
	err := _main()
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		os.Exit(1)
	}
}
