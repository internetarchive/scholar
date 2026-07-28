// fcmatch reads a TSV of articles (with "article title" and "creator" columns)
// and writes a new TSV with one extra column: the fatcat ident of the
// best-matching release found via the public scholar.archive.org ES proxy.
//
// A row is considered a match only if the input title's significant tokens
// overlap with the candidate hit's title above a threshold; this is a sanity
// check on top of ES's own scoring.
package main

import (
	"bufio"
	"bytes"
	"encoding/base32"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
)

const esURL = "https://scholar.archive.org/_es/fatcat_release/_search"

type esHit struct {
	Score  float64 `json:"_score"`
	Source struct {
		Ident        string   `json:"ident"`
		Title        string   `json:"title"`
		ContribNames []string `json:"contrib_names"`
	} `json:"_source"`
}

type esResponse struct {
	Hits struct {
		Hits []esHit `json:"hits"`
	} `json:"hits"`
}

func main() {
	var inPath, outPath string
	var delayMs int
	flag.StringVar(&inPath, "input", "articles_1.0.1.tsv", "input TSV path")
	flag.StringVar(&outPath, "output", "", "output TSV path; default stdout")
	flag.IntVar(&delayMs, "delay", 250, "milliseconds to wait between requests")
	flag.Parse()

	in, err := os.Open(inPath)
	if err != nil {
		log.Fatal(err)
	}
	defer in.Close()

	var outFile io.Writer = os.Stdout
	if outPath != "" {
		f, err := os.Create(outPath)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		outFile = f
	}

	r := csv.NewReader(bufio.NewReader(in))
	r.Comma = '\t'
	r.FieldsPerRecord = -1
	r.LazyQuotes = true

	w := csv.NewWriter(outFile)
	w.Comma = '\t'
	defer w.Flush()

	header, err := r.Read()
	if err != nil {
		log.Fatalf("reading header: %v", err)
	}
	titleIdx, creatorIdx := -1, -1
	for i, h := range header {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "article title", "title":
			titleIdx = i
		case "creator", "creators":
			creatorIdx = i
		}
	}
	if titleIdx < 0 {
		log.Fatalf("no title column in header: %v", header)
	}
	outHeader := append(append([]string{}, header...), "fatcat_uuid")
	if err := w.Write(outHeader); err != nil {
		log.Fatal(err)
	}
	w.Flush()

	client := &http.Client{Timeout: 30 * time.Second}

	rowN := 1
	matched, missed, skipped := 0, 0, 0
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("read error row %d: %v", rowN+1, err)
			continue
		}
		rowN++

		title := ""
		if titleIdx < len(row) {
			title = strings.TrimSpace(row[titleIdx])
		}
		creator := ""
		if creatorIdx >= 0 && creatorIdx < len(row) {
			creator = strings.TrimSpace(row[creatorIdx])
		}

		uuidStr := ""
		if title == "" {
			skipped++
		} else {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
			ident, err := lookup(client, title, creator)
			if err != nil {
				log.Printf("row %d lookup error %q: %v", rowN, truncate(title, 50), err)
			}
			if ident != "" {
				u, err := fcIdentToUUID(ident)
				if err != nil {
					log.Printf("row %d ident decode error %q: %v", rowN, ident, err)
				} else {
					uuidStr = u
				}
			}
			if uuidStr != "" {
				matched++
				log.Printf("row %d MATCH %s -- %q", rowN, uuidStr, truncate(title, 60))
			} else {
				missed++
				log.Printf("row %d MISS  -- %q", rowN, truncate(title, 60))
			}
		}

		outRow := append(append([]string{}, row...), uuidStr)
		if err := w.Write(outRow); err != nil {
			log.Fatal(err)
		}
		w.Flush()
	}
	log.Printf("done: %d matched, %d missed, %d skipped (empty title)", matched, missed, skipped)
}

func lookup(client *http.Client, title, creator string) (string, error) {
	body, err := json.Marshal(buildQuery(title, creator))
	if err != nil {
		return "", err
	}

	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequest("POST", esURL, bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		r, err := client.Do(req)
		if err != nil {
			if attempt == 2 {
				return "", err
			}
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		if r.StatusCode == 429 || r.StatusCode >= 500 {
			r.Body.Close()
			if attempt == 2 {
				return "", fmt.Errorf("status %d after retries", r.StatusCode)
			}
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		resp = r
		break
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	var er esResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return "", err
	}
	inputTokens := titleTokens(title)
	if len(inputTokens) == 0 {
		return "", nil
	}
	for _, h := range er.Hits.Hits {
		if titleOverlap(inputTokens, titleTokens(h.Source.Title)) {
			return h.Source.Ident, nil
		}
	}
	return "", nil
}

func buildQuery(title, creator string) map[string]any {
	must := []any{
		map[string]any{"match": map[string]any{"title": title}},
	}
	var should []any
	for _, name := range splitCreators(creator) {
		should = append(should, map[string]any{
			"match_phrase": map[string]any{"contrib_names": name},
		})
	}
	boolQ := map[string]any{"must": must}
	if len(should) > 0 {
		boolQ["should"] = should
		boolQ["minimum_should_match"] = 1
	}
	return map[string]any{
		"size":  5,
		"query": map[string]any{"bool": boolQ},
	}
}

func splitCreators(s string) []string {
	var out []string
	for _, c := range strings.Split(s, ";") {
		c = strings.TrimSpace(c)
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// titleTokens returns lowercased alphanumeric tokens of length >= 4 that are
// not in the stopword list.
func titleTokens(s string) map[string]struct{} {
	m := make(map[string]struct{})
	var cur strings.Builder
	flush := func() {
		t := cur.String()
		cur.Reset()
		if len(t) < 4 {
			return
		}
		if _, ok := stopwords[t]; ok {
			return
		}
		m[t] = struct{}{}
	}
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return m
}

var stopwords = map[string]struct{}{
	"with": {}, "from": {}, "this": {}, "that": {}, "into": {},
	"their": {}, "what": {}, "when": {}, "your": {}, "have": {},
	"been": {}, "than": {}, "they": {}, "them": {}, "were": {},
	"where": {}, "which": {}, "would": {}, "could": {}, "should": {},
	"about": {},
}

// titleOverlap returns true if the input and hit token sets share at least
// 40% of the input's tokens (with a floor of 1 shared token).
func titleOverlap(input, hit map[string]struct{}) bool {
	if len(input) == 0 || len(hit) == 0 {
		return false
	}
	common := 0
	for k := range input {
		if _, ok := hit[k]; ok {
			common++
		}
	}
	if common == 0 {
		return false
	}
	return common*5 >= len(input)*2
}

// fcIdentToUUID decodes fatcat's 26-char lowercase base32 ident into the
// canonical 8-4-4-4-12 UUID string. Matches the conversion in
// trawler/fatcat2.fc2uuid.
func fcIdentToUUID(ident string) (string, error) {
	padded := strings.ToUpper(ident) + "======"
	b, err := base32.StdEncoding.DecodeString(padded)
	if err != nil {
		return "", err
	}
	if len(b) != 16 {
		return "", fmt.Errorf("decoded length %d, want 16", len(b))
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
