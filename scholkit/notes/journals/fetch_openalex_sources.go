package main

import (
	"crypto/sha1"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path"
	"time"

	"github.com/adrg/xdg"
	"github.com/sethgrid/pester"
)

const AppName = "fetch-openalex-sources"

var maxPage = flag.Int("m", 2546, "last page to fetch")

func main() {
	flag.Parse()
	var (
		cacher  = &FileCacher{}
		page    = 1
		perPage = 100
	)
	for {
		if page > *maxPage {
			log.Println("done")
			break
		}
		link := fmt.Sprintf("https://api.openalex.org/sources?page=%d&per_page=%d", page, perPage)
		log.Printf("%d entries fetched: %s", page*perPage, link)
		_, err := cacher.Get(link)
		if err == ErrCacheMiss {
			req, err := http.NewRequest("GET", link, nil)
			if err != nil {
				log.Fatal(err)
			}
			resp, err := pester.Do(req)
			if err != nil {
				log.Fatal(err)
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(resp.Body)
			if err != nil {
				log.Fatal(err)
			}
			err = cacher.Set(link, b)
			if err != nil {
				log.Fatal(err)
			}
			time.Sleep(2 * time.Second)
		} else {
			log.Printf("already cached")
		}
		page++
	}
}

type OpenAlexSourcesResponse struct {
	Meta struct {
		Count            int64 `json:"count"`
		DbResponseTimeMs int64 `json:"db_response_time_ms"`
		Page             int64 `json:"page"`
		PerPage          int64 `json:"per_page"`
	} `json:"meta"`
	Results []struct {
		AbbreviatedTitle string      `json:"abbreviated_title"`
		AlternateTitles  []string    `json:"alternate_titles"`
		ApcPrices        interface{} `json:"apc_prices"`
		ApcUsd           interface{} `json:"apc_usd"`
		CitedByCount     int64       `json:"cited_by_count"`
		CountryCode      string      `json:"country_code"`
		CountsByYear     []struct {
			CitedByCount int64 `json:"cited_by_count"`
			WorksCount   int64 `json:"works_count"`
			Year         int64 `json:"year"`
		} `json:"counts_by_year"`
		CreatedDate             string   `json:"created_date"`
		DisplayName             string   `json:"display_name"`
		HomepageUrl             string   `json:"homepage_url"`
		HostOrganization        string   `json:"host_organization"`
		HostOrganizationLineage []string `json:"host_organization_lineage"`
		HostOrganizationName    string   `json:"host_organization_name"`
		Id                      string   `json:"id"`
		Ids                     []string `json:"ids"`
		IsInDoaj                bool     `json:"is_in_doaj"`
		IsOa                    bool     `json:"is_oa"`
		Issn                    string   `json:"issn"`
		IssnL                   string   `json:"issn_l"`
		Societies               []string `json:"societies"`
		SummaryStats            struct {
			HIndex          int64   `json:"h_index"`
			I10Index        int64   `json:"i10_index"`
			YrMeanCitedness float64 `json:"2yr_mean_citedness"`
		} `json:"summary_stats"`
		TopicShare  []string `json:"topic_share"`
		Topics      []string `json:"topics"`
		Type        string   `json:"type"`
		UpdatedDate string   `json:"updated_date"`
		WorksApiUrl string   `json:"works_api_url"`
		WorksCount  int64    `json:"works_count"`
		XConcepts   []string `json:"x_concepts"`
	} `json:"results"`
}

var ErrCacheMiss = errors.New("cache miss")

// Cacher allows to save and request data by key.
type Cacher interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
}

// FileCacher uses the filesystem and standard locations.
type FileCacher struct {
	Sleep time.Duration
}

// slugify a string, to we can use it as filename.
func (c *FileCacher) slugify(s string) string {
	h := sha1.New()
	_, _ = io.WriteString(h, s)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (c *FileCacher) Get(key string) ([]byte, error) {
	filename, err := xdg.CacheFile(path.Join(AppName, "fc", c.slugify(key)))
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return nil, ErrCacheMiss
	}
	if err != nil {
		return nil, err
	}
	return ioutil.ReadFile(filename)
}

func (c *FileCacher) Set(key string, value []byte) error {
	filename, err := xdg.CacheFile(path.Join(AppName, "fc", c.slugify(key)))
	if err != nil {
		return err
	}
	if c.Sleep > 0 {
		zz := c.Sleep + time.Duration(rand.Intn(2000))*time.Millisecond
		log.Printf("sleeping for %s", zz)
		time.Sleep(zz)
	}
	log.Printf("cached: %s (%s)", filename, key)
	return WriteFileAtomic(filename, value, 0644)
}

// WriteFileAtomic writes the data to a temp file and atomically move if everything else succeeds.
func WriteFileAtomic(filename string, data []byte, perm os.FileMode) error {
	dir, name := path.Split(filename)
	f, err := ioutil.TempFile(dir, name)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if permErr := os.Chmod(f.Name(), perm); err == nil {
		err = permErr
	}
	if err == nil {
		err = os.Rename(f.Name(), filename)
	}
	// Any err should result in full cleanup.
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}
