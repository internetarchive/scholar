package ia

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// SearchResponse represents the response from the Internet Archive scraping API.
type SearchResponse struct {
	Items  []SearchItem `json:"items"`
	Count  int          `json:"count"`
	Total  int          `json:"total"`
	Cursor string       `json:"cursor,omitempty"`
}

// SearchItem represents a single search result item.
type SearchItem struct {
	Identifier string `json:"identifier"`
}

// Item metadata.
type Item struct {
	Created int64  `json:"created"`
	D1      string `json:"d1"`
	D2      string `json:"d2"`
	Dir     string `json:"dir"`
	Files   []struct {
		Crc32     string `json:"crc32"`
		Format    string `json:"format"`
		Md5       string `json:"md5"`
		Mtime     string `json:"mtime"`
		Name      string `json:"name"`
		Original  string `json:"original"`
		Private   string `json:"private"`
		Sha1      string `json:"sha1"`
		Size      string `json:"size"`
		Source    string `json:"source"`
		Summation string `json:"summation"`
	} `json:"files"`
	FilesCount      int64 `json:"files_count"`
	ItemLastUpdated int64 `json:"item_last_updated"`
	ItemSize        int64 `json:"item_size"`
	Metadata        struct {
		AccessRestrictedItem string   `json:"access-restricted-item"`
		Addeddate            string   `json:"addeddate"`
		Collection           []string `json:"collection"`
		CollectionAdded      []string `json:"collection_added"`
		Contributor          string   `json:"contributor"`
		Crawler              string   `json:"crawler"`
		Crawljob             string   `json:"crawljob"`
		Creator              string   `json:"creator"`
		Date                 string   `json:"date"`
		Description          string   `json:"description"`
		Firstfiledate        string   `json:"firstfiledate"`
		Firstfileserial      string   `json:"firstfileserial"`
		Identifier           string   `json:"identifier"`
		IdentifierAccess     string   `json:"identifier-access"`
		Imagecount           string   `json:"imagecount"`
		Lastdate             string   `json:"lastdate"`
		Lastfiledate         string   `json:"lastfiledate"`
		Lastfileserial       string   `json:"lastfileserial"`
		Mediatype            string   `json:"mediatype"`
		Operator             string   `json:"operator"`
		Publicdate           string   `json:"publicdate"`
		Scandate             string   `json:"scandate"`
		Scanningcenter       string   `json:"scanningcenter"`
		Sizehint             string   `json:"sizehint"`
		Sponsor              string   `json:"sponsor"`
		Subject              string   `json:"subject"`
		Title                string   `json:"title"`
		Uploader             string   `json:"uploader"`
	} `json:"metadata"`
	Server          string   `json:"server"`
	Uniq            int64    `json:"uniq"`
	WorkableServers []string `json:"workable_servers"`
}

// SearchCollection searches for all items in a collection using the IA scraping API.
// It handles pagination automatically and returns all item identifiers.
func SearchCollection(client *http.Client, collection string) ([]string, error) {
	const baseURL = "https://archive.org/services/search/v1/scrape"
	var items []string
	cursor := ""

	for {
		u, err := url.Parse(baseURL)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("q", fmt.Sprintf("collection:%s", collection))
		q.Set("fields", "identifier")
		q.Set("count", "1000") // Maximum items per request
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		u.RawQuery = q.Encode()

		resp, err := client.Get(u.String())
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("search API returned status %d", resp.StatusCode)
		}
		var searchResp SearchResponse
		if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		for _, item := range searchResp.Items {
			items = append(items, item.Identifier)
		}
		if searchResp.Cursor == "" || len(searchResp.Items) == 0 {
			break
		}
		cursor = searchResp.Cursor
	}

	return items, nil
}
