// sk-cdx looks up CDX records at the Internet Archive. Additional docs:
// https://github.com/internetarchive/wayback/blob/master/wayback-cdx-server/README.md
//
// (formerly known as cdxlookup)
//
// $ echo 'https://heise.de' | sk-cdx | jq .
// {
//   "surt": "de,heise)/",
//   "date": "19961222042120",
//   "link": "http://www.heise.de:80/",
//   "type": "text/html",
//   "code": "200",
//   "checksum": "47YA6M7IS6JL3T5PKGOOBNO7K2DDGIOV",
//   "size": "2465"
// }

package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/adrg/xdg"
	"github.com/miku/scholkit"
	"github.com/sethgrid/pester"
)

var (
	limit       = flag.Int("l", 1, "limit")
	countOnly   = flag.Bool("C", false, "count only query")
	showVersion = flag.Bool("version", false, "show version")
)

// CDX line, might add more fields later.
type CDX struct {
	Surt        string `json:"surt"`
	Date        string `json:"date"`
	Link        string `json:"link"`
	ContentType string `json:"type"`
	StatusCode  string `json:"code"`
	Checksum    string `json:"checksum"`
	Size        string `json:"size"`
}

func main() {
	flag.Parse()
	if *showVersion {
		fmt.Println(scholkit.Version)
		os.Exit(0)
	}
	br := bufio.NewReader(os.Stdin)
	bw := bufio.NewWriter(os.Stdout)
	defer bw.Flush()
	enc := json.NewEncoder(bw)
	for {
		line, err := br.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		line = strings.TrimSpace(line)
		result, err := Lookup(line, *limit)
		if err != nil {
			log.Fatal(err)
		}
		switch {
		case *countOnly:
			payload := map[string]interface{}{
				"url":   line,
				"count": len(result),
			}
			if err := enc.Encode(payload); err != nil {
				log.Fatal(err)
			}
		default:
			for _, r := range result {
				if err := enc.Encode(r); err != nil {
					log.Fatal(err)
				}
			}
		}
	}
}

// Lookup asks CDX API. Result will be like:
// net,ijmse)/uploadfile/2016/1214/20161214052559646.pdf 20170516210333
// http://www.ijmse.net:80/uploadfile/2016/1214/20161214052559646.pdf
// application/pdf 200 PBPHE2OILTB43TAOUO33GBWLE2SS4LQX 2079755
//
// TODO: paging
func Lookup(link string, limit int) (result []CDX, err error) {
	link = prependSchema(link)
	cdxlink := fmt.Sprintf("http://web.archive.org/cdx/search/cdx?url=%s&limit=%d", link, limit)
	h := sha1.New()
	_, _ = h.Write([]byte(cdxlink))
	sum := fmt.Sprintf("%x", h.Sum(nil))
	shard, filename := sum[:2], sum[2:]
	cached := path.Join(xdg.CacheHome, "cdxlookup", shard, filename)
	if _, err := os.Stat(path.Dir(cached)); os.IsNotExist(err) {
		if err := os.MkdirAll(path.Dir(cached), 0755); err != nil {
			return nil, err
		}
	}
	var r io.Reader
	if _, err := os.Stat(cached); err == nil {
		f, err := os.Open(cached)
		if err != nil {
			return nil, err
		}
		r = f
	} else {
		req, err := http.NewRequest("GET", cdxlink, nil)
		if err != nil {
			return nil, err
		}
		resp, err := pester.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		f, err := os.Create(cached)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = io.TeeReader(resp.Body, f)
	}
	b, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		var fields = strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 7 {
			log.Printf("short line: %s", line)
			continue
		}
		cdx := CDX{
			Surt:        fields[0],
			Date:        fields[1],
			Link:        fields[2],
			ContentType: fields[3],
			StatusCode:  fields[4],
			Checksum:    fields[5],
			Size:        fields[6],
		}
		result = append(result, cdx)
	}
	return result, nil
}

func prependSchema(s string) string {
	if strings.HasPrefix(s, "http") {
		return s
	}
	return fmt.Sprintf("http://%s", s)
}
