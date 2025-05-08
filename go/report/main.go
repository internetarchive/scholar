package main

/*
The purpose of this program is to produce a simple, rough report on the past 24 hours of scholar activity.
*/

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
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

func _main() error {
	tmpl, err := template.New("report").Parse(reportTmpl)
	if err != nil {
		return fmt.Errorf("template parsing failed: %w", err)
	}
	tmplCtx := reportCtx{PDFMissReasons: []PDFMissReason{}}
	client := &http.Client{}
	body := bytes.NewBufferString(`
{"query": {
      "bool": {"must": [{"term": { "access.access_type": "wayback"}},
			                  {"term": {"access.mimetype": "application/pdf"}}]}}
}`)
	esReq, err := http.NewRequest(http.MethodGet, esURL, body)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	esReq.Header.Add("Content-Type", "application/json")
	resp, err := client.Do(esReq)
	if err != nil {
		return fmt.Errorf("failed to talk to elasticsearch: %w", err)
	}

	// TODO
	fmt.Println(resp)

	// TODO consume https://scholar.archive.org/_es
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
