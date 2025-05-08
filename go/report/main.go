package main

/*
The purpose of this program is to produce a simple, rough report on the past 24 hours of scholar activity.
*/

import (
	_ "embed"
	"fmt"
	"html/template"
	"os"
)

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
	// TODO consume https://scholar.archive.org/fatcat/stats.json
	// TODO consume https://scholar.archive.org/_es
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
