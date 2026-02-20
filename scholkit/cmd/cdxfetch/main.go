package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"

	"github.com/sethgrid/pester"
)

// SearchResponse is an API response.
type SearchResponse struct {
	Response struct {
		Docs []struct {
			Identifier string `json:"identifier"`
		} `json:"docs"`
		NumFound int64 `json:"numFound"`
		Start    int64 `json:"start"`
	} `json:"response"`
	ResponseHeader struct {
		Params struct {
			Fields string `json:"fields"`
			Qin    string `json:"qin"`
			Query  string `json:"query"`
			Rows   string `json:"rows"`
			Start  int64  `json:"start"`
			Wt     string `json:"wt"`
		} `json:"params"`
		QTime  int64
		Status int64 `json:"status"`
	} `json:"responseHeader"`
}

var (
	searchURL = flag.String("api", "https://archive.org/advancedsearch.php", "api location")
)

func main() {
	flag.Parse()
	if flag.NArg() == 0 {
		log.Fatal("missing collection name")
	}
	name := flag.Arg(0)
	// https://archive.org/advancedsearch.php?q=collection%3AOA-JOURNAL-CRAWL-2023-10&fl%5B%5D=identifier&sort%5B%5D=&sort%5B%5D=&sort%5B%5D=&rows=50&page=1&output=json
	opts := &SearchOpts{
		URL:    *searchURL,
		Q:      fmt.Sprintf("collection:%v", name),
		Fields: "identifier",
	}
	responses, err := AdvancedSearch(opts)
	if err != nil {
		log.Fatal(err)
	}
	var ids []string
	for _, r := range responses {
		for _, doc := range r.Response.Docs {
			ids = append(ids, doc.Identifier)
		}
	}
	log.Printf("%v", ids)
}

type SearchOpts struct {
	URL    string
	Q      string
	Fields string
	Sort   string
	Rows   int
	Page   int
	Output string
}

func AdvancedSearch(opts *SearchOpts) ([]SearchResponse, error) {
	client := pester.New()
	client.SetRetryOnHTTP429(true)
	var (
		page   = 1
		rows   = 50
		result []SearchResponse
	)
	for {
		vs := url.Values{}
		vs.Set("q", opts.Q)
		vs.Set("fl", opts.Fields)
		vs.Set("rows", strconv.Itoa(opts.Rows))
		vs.Set("page", strconv.Itoa(page))
		u := fmt.Sprintf("%s?%s", opts.URL, vs.Encode())
		log.Printf(u)
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var sr SearchResponse
		if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
			return nil, err
		}
		result = append(result, sr)
		if int(sr.Response.NumFound) < page*rows {
			break
		}
		page = page + 1
	}
	return result, nil
}
