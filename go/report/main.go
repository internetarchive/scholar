package main

/*
The purpose of this program is to produce a simple, rough report on the past 24 hours of scholar activity.

thoughts regarding stats over time: add columns for the past week of stats.
keep stats serialized as flat files and just grab the last N where N >=6 to
combine with the current day.

can have a check that shreds files when there's more than some limit. they will be tiny files so the limit can be stupid high--maybe 365*3.

*/

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	esURL = "https://scholar.archive.org/_es/scholar_fulltext/_count"
	fcURL = "https://scholar.archive.org/fatcat/stats.json"
	scURL = "http://wbgrp-svc506.us.archive.org:3030/rpc"
)

//go:embed report.tmpl.html
var reportTmpl string

type PDFMissReason struct {
	Reason string `json:"status"`
	Count  int
}

type reportCtx struct {
	FulltextPDFCount     int
	FatcatPaperCount     int
	FatcatContainerCount int
	FatcatReleaseCount   int
	FatcatRefCount       int
	PDFHitCount          int
	PDFMissCount         int
	PDFMissReasons       []PDFMissReason
	Date                 string
}

type fatcatStats struct {
	Papers    struct{ Total int }
	Container struct{ Total int }
	Release   struct {
		Total     int
		TotalRefs int `json:"refs_total"`
	}
}

type sandcrawlerStats struct {
	PDFMiss        int
	PDFHit         int
	PDFMissReasons []PDFMissReason
}

func getFulltextPDFCount(c *http.Client) (int, error) {
	body := bytes.NewBufferString(`
{"query": {
      "bool": {"must": [{"term": { "access.access_type": "wayback"}},
			                  {"term": {"access.mimetype": "application/pdf"}}]}}
}`)
	req, err := http.NewRequest(http.MethodGet, esURL, body)
	if err != nil {
		return -1, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Add("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return -1, fmt.Errorf("failed to talk to ES: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return -1, fmt.Errorf("got %d from ES", resp.StatusCode)
	}

	esCount := struct {
		Count int
	}{}

	rbody, err := io.ReadAll(resp.Body)
	if err != nil {
		return -1, fmt.Errorf("could not read ES response: %w", err)
	}

	if err = json.Unmarshal(rbody, &esCount); err != nil {
		return -1, fmt.Errorf("could not parse ES response: %w", err)
	}

	return esCount.Count, nil
}

func getFatcatStats(c *http.Client) (fatcatStats, error) {
	fcStats := fatcatStats{}
	req, err := http.NewRequest(http.MethodGet, fcURL, nil)
	if err != nil {
		return fcStats, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.Do(req)
	if err != nil {
		return fcStats, fmt.Errorf("failed to talk to fatcat: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fcStats, fmt.Errorf("got %d from fatcat", resp.StatusCode)
	}

	rbody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fcStats, fmt.Errorf("could not read fatcat response: %w", err)
	}

	if err = json.Unmarshal(rbody, &fcStats); err != nil {
		return fcStats, fmt.Errorf("could not parse fatcat response: %w", err)
	}

	return fcStats, nil
}

func getSandcrawlerStats(c *http.Client) (sandcrawlerStats, error) {
	scStats := sandcrawlerStats{}

	req, err := http.NewRequest(http.MethodGet, scURL+"/stat_failed_pdf", nil)
	if err != nil {
		return scStats, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.Do(req)
	if err != nil {
		return scStats, fmt.Errorf("failed to talk to sandcrawler: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return scStats, fmt.Errorf("got %d from sandcrawler", resp.StatusCode)
	}

	rbody, err := io.ReadAll(resp.Body)
	if err != nil {
		return scStats, fmt.Errorf("could not read sandcrawler response: %w", err)
	}

	type failed struct {
		Count int `json:"stat_failed_pdf"`
	}
	fparsed := []failed{}
	err = json.Unmarshal(rbody, &fparsed)
	if err != nil {
		return scStats, fmt.Errorf("could not parse sandcrawler response: %w", err)
	}
	if len(fparsed) > 0 {
		scStats.PDFMiss = fparsed[0].Count
	}

	req, err = http.NewRequest(http.MethodGet, scURL+"/stat_got_pdf", nil)
	if err != nil {
		return scStats, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err = c.Do(req)
	if err != nil {
		return scStats, fmt.Errorf("failed to talk to sandcrawler: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return scStats, fmt.Errorf("got %d from sandcrawler", resp.StatusCode)
	}

	rbody, err = io.ReadAll(resp.Body)
	if err != nil {
		return scStats, fmt.Errorf("could not read sandcrawler response: %w", err)
	}

	type got struct {
		Count int `json:"stat_got_pdf"`
	}
	gparsed := []got{}
	err = json.Unmarshal(rbody, &gparsed)
	if err != nil {
		return scStats, fmt.Errorf("could not parse sandcrawler response: %w", err)
	}
	if len(gparsed) > 0 {
		scStats.PDFHit = gparsed[0].Count
	}

	req, err = http.NewRequest(http.MethodGet, scURL+"/stat_error_counts", nil)
	if err != nil {
		return scStats, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err = c.Do(req)
	if err != nil {
		return scStats, fmt.Errorf("failed to talk to sandcrawler: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return scStats, fmt.Errorf("got %d from sandcrawler", resp.StatusCode)
	}

	rbody, err = io.ReadAll(resp.Body)
	if err != nil {
		return scStats, fmt.Errorf("could not read sandcrawler response: %w", err)
	}

	rparsed := []PDFMissReason{}
	err = json.Unmarshal(rbody, &rparsed)
	if err != nil {
		return scStats, fmt.Errorf("could not parse sandcrawler response: %w", err)
	}

	scStats.PDFMissReasons = rparsed

	return scStats, nil
}

func _main() error {
	tmpl, err := template.New("report").Parse(reportTmpl)
	if err != nil {
		return fmt.Errorf("template parsing failed: %w", err)
	}
	client := &http.Client{}

	esCount, err := getFulltextPDFCount(client)
	if err != nil {
		return fmt.Errorf("failed to get fulltext PDF count: %w", err)
	}

	fcStats, err := getFatcatStats(client)
	if err != nil {
		return fmt.Errorf("failed to get stats.json from fatcat: %w", err)
	}

	scStats, err := getSandcrawlerStats(client)
	if err != nil {
		return fmt.Errorf("failed to get crawl stats: %w", err)
	}

	tmplCtx := reportCtx{
		FulltextPDFCount:     esCount,
		FatcatPaperCount:     fcStats.Papers.Total,
		FatcatContainerCount: fcStats.Container.Total,
		FatcatReleaseCount:   fcStats.Release.Total,
		FatcatRefCount:       fcStats.Release.TotalRefs,
		PDFHitCount:          scStats.PDFHit,
		PDFMissCount:         scStats.PDFMiss,
		PDFMissReasons:       scStats.PDFMissReasons,
		Date:                 time.Now().Format("2006 Jan 2"),
	}

	err = tmpl.Execute(os.Stdout, tmplCtx)
	if err != nil {
		return fmt.Errorf("template execution failed: %w", err)
	}

	return nil
}

func main() {
	err := _main()
	if err != nil {
		fmt.Fprintf(os.Stderr, "melancholy: %s\n", err.Error())
		os.Exit(1)
	}
}
