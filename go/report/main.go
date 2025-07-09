package main

/*
The purpose of this program is to produce a simple, rough report on the past 24 hours of scholar activity.

thoughts regarding stats over time: add columns for the past week of stats.
keep stats serialized as flat files and just grab the last N where N >=6 to
combine with the current day.

can have a check that shreds files when there's more than some limit. they will be tiny files so the limit can be stupid high--maybe 365*3.

jefferson wants data updates up to a year back. i'm considering a flat file of jsonl, one line for a 24 hour period with timestamps on each one.

jefferson doesn't want daily -- but i do. i've been spotting problems and am able to respond.

so I think I want to run daily in order to add a jsonl line. I might split this code into just stats gathering vs. templatized output. the monthly stuff is easier to just include in the weekly email.

- releases added to fatcat
  - this week
	- % change from last week
	- averages over time (30d, 90d, 180d, 365d)
	- source percentages
- URLs sent to SPN
	- this week
	- % change from last week
	- averages over time (30d, 90d, 180d, 365d)
	- source percentages
- PDFs acquired from SPN
  - this week
	- % change from last week
	- averages over time (30d, 90d, 180d, 365d)
	- source percentages
- total containers
- total works
- total works with an archived release
- % of works with an archived release
- total releases
- total citations
- total containers reflected by IAS index
- total releases in IAS index
- total works added in last month via one off crawls
- total releases added in last month via one off crawls
- scholar search queries in last month
- fatcat catalog quries in the last month
- total number of things in scholar sitemap
- total number of things in most recent kbart report

*/

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"time"
)

const (
	esURL     = "https://scholar.archive.org/_es/scholar_fulltext/_count"
	fcURL     = "https://scholar.archive.org/fatcat/stats.json"
	scURL     = "http://wbgrp-svc506.us.archive.org:3030/rpc"
	statsPath = "scholstats.jsonl"
)

//go:embed report.tmpl.html
var reportTmpl string

type pdfMissReasons struct {
	Reason string `json:"status"`
	Count  int
}

type dailyStats struct {
	FulltextPDFCount     int
	FatcatPaperCount     int
	FatcatContainerCount int
	FatcatReleaseCount   int
	FatcatRefCount       int
	PDFHitCount          int
	PDFMissCount         int
	PDFMissReasons       []pdfMissReasons
	Date                 string
}

type stats struct {
	// TODO
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
	PDFMissReasons []pdfMissReasons
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

	tmplCtx := dailyStats{
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

func gatherDailyStats() (s dailyStats, err error) {
	client := &http.Client{}
	esCount, err := getFulltextPDFCount(client)
	if err != nil {
		err = fmt.Errorf("failed to get fulltext PDF count: %w", err)
		return
	}

	fcStats, err := getFatcatStats(client)
	if err != nil {
		err = fmt.Errorf("failed to get stats.json from fatcat: %w", err)
		return
	}

	scStats, err := getSandcrawlerStats(client)
	if err != nil {
		err = fmt.Errorf("failed to get crawl stats: %w", err)
		return
	}
	s = dailyStats{
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

	return
}

func writeDailyStats() error {
	stats, err := gatherDailyStats()
	if err != nil {
		return err
	}

	bs, err := json.Marshal(stats)
	if err != nil {
		return err
	}

	bs = append(bs, '\n')

	return os.WriteFile(statsPath, bs, os.ModeAppend)
}

type tmplCtx struct {
	// TODO computed stats struct
	To []string
	CC []string
}

func email(freq string) error {
	tmpl, err := template.New("report").Parse(reportTmpl)
	if err != nil {
		return fmt.Errorf("template parsing failed: %w", err)
	}
	bs, err := os.ReadFile(statsPath)
	if err != nil {
		return err
	}

	// TODO rename stats to reflect single day nature
	// TODO new computed stats struct
	// TODO extract stat lines and compute big stats

	ctx := tmplCtx{}

	ctx.To = []string{"nsmith@archive.org"}
	switch freq {
	case "daily":
	case "weekly":
		ctx.CC = []string{"jefferson@archive.org"}
	default:
		panic("unknown freq: " + freq)
	}

	err = tmpl.Execute(os.Stdout, ctx)
	if err != nil {
		return fmt.Errorf("template execution failed: %w", err)
	}
	return nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "need an operation argument; one of 'update', 'email-daily', 'email-weekly'")
		os.Exit(2)
	}

	var err error

	switch os.Args[1] {
	case "update":
		err = writeDailyStats()
	case "email-daily":
		err = email("daily")
	case "email-weekly":
		err = email("weekly")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "melancholy: %s\n", err.Error())
		os.Exit(1)
	}
}
