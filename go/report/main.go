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
)

const esURL = "https://scholar.archive.org/_es/scholar_fulltext/_count"

//go:embed report.tmpl.html
var reportTmpl string

type PDFMissReason struct {
	Reason string
	Count  int
}

type reportCtx struct {
	FulltextPDFCount     int
	FatcatPaperCount     int
	FatcatContainerCount int
	PDFHitCount          int
	PDFMissCount         int
	PDFMissReasons       []PDFMissReason
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
		return -1, fmt.Errorf("failed to talk to elasticsearch: %w", err)
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

	err = json.Unmarshal(rbody, &esCount)

	if err != nil {
		return -1, fmt.Errorf("could not parse ES response: %w", err)
	}

	return esCount.Count, nil
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

	tmplCtx := reportCtx{
		FulltextPDFCount: esCount,
	}

	// TODO consume https://scholar.archive.org/fatcat/stats.json
	// TODO consume http://wbgrp-svc506.us.archive.org:3030/rpc
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
