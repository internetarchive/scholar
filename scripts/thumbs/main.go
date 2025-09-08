package main

/*
curl -XGET 'https://scholar.archive.org/_es/scholar_fulltext/_search?scroll=1m' -H 'Content-Type: application/json' -d '{"fields": ["fulltext.thumbnail_url"], "_source": false, "size": 10}' | jq > sample.json

{
  "_scroll_id": "...",
  "took": 1235,
  "timed_out": false,
  "_shards": {
    "total": 12,
    "successful": 12,
    "skipped": 0,
    "failed": 0
  },
  "hits": {
    "total": {
      "value": 298139552,
      "relation": "eq"
    },
    "max_score": 1,
    "hits": [
      {
        "_index": "scholar_fulltext_v01_20211208",
        "_type": "_doc",
        "_id": "page_sim_automobile-magazine_1899-11_1_2_146",
        "_score": 1,
        "fields": {
          "fulltext.thumbnail_url": [
            "https://archive.org/serve/sim_automobile-magazine_1899-11_1_2/__ia_thumb.jpg"
          ]
        }
      },
		...
	]}}
*/

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

const (
	path       = "https://scholar.archive.org/_es/scholar_fulltext/_search"
	scrollPath = "https://scholar.archive.org/_es/scholar_fulltext/_search/scroll"
)

type elasticResult struct {
	ScrollID string `json:"_scroll_id"`
	Hits     struct {
		Hits []struct {
			Fields struct {
				Turl []string `json:"fulltext.thumbnail_url"`
			}
		}
	}
}

func main() {
	l := log.New(os.Stderr, "", log.Lshortfile)
	client := http.Client{}
	initQuery := `{"fields": ["fulltext.thumbnail_url"], "_source": false, "size":10000}`
	req, err := http.NewRequest("GET", path, bytes.NewBufferString(initQuery))
	if err != nil {
		panic(err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	q := req.URL.Query()
	q.Set("scroll", "1m")

	resp, err := client.Do(req)
	if err != nil {
		panic(err.Error())
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}

	var er elasticResult
	err = json.Unmarshal(body, &er)
	if err != nil {
		panic(err)
	}

	scrollID := er.ScrollID
	thumbsFound := 0

	for scrollID != "" {
		for _, hit := range er.Hits.Hits {
			for _, turl := range hit.Fields.Turl {
				l.Println(turl)
				if strings.Contains(turl, "/thumbnail/pdf") {
					sp := strings.SplitN(turl, "/", 2)
					fmt.Println(sp[1])
					thumbsFound++
				}
			}
		}
		scrollQuery := `{"scroll": "1m", "scroll_id": "` + scrollID + `"}`
		req, err := http.NewRequest("GET", scrollPath, bytes.NewBufferString(scrollQuery))
		if err != nil {
			panic(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			panic(err.Error())
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			panic(err)
		}

		err = json.Unmarshal(body, &er)
		if err != nil {
			panic(err)
		}

		scrollID = er.ScrollID
	}

	if thumbsFound == 0 {
		fmt.Fprintln(os.Stderr, "no thumbs found")
		os.Exit(1)
	}
}
