package indexing

import (
	"slices"
	"strings"
)

type SourcedDoc struct {
	// IngestSource is the raw Source field of a fatcat2 entity
	IngestSource string `json:"ingest_source"`

	// IngestSourceKind is the high level flavor of ingestion (daily or periodic)
	IngestSourceKind string `json:"ingest_source_kind"`

	// IngestSourceProvider is the name of where we found something (like an
	// upstream API or web crawl petabox collection)
	IngestSourceProvider string `json:"ingest_source_provider"`
}

type Sourced interface {
	GetSource() string
}

func (d *SourcedDoc) SetSourceFields(s Sourced) {
	raw := s.GetSource()
	d.IngestSource = raw
	split := strings.SplitN(raw, "-", 4)
	if len(split) != 4 {
		return
	}
	if slices.Contains([]string{"daily", "periodic"}, split[0]) {
		d.IngestSourceKind = split[0]
	}

	// the other stuff in the raw source (date of run, run id) are mostly just
	// for debugging

	d.IngestSourceProvider = split[3]
}
