package etd

import (
	"maps"
	"reflect"

	"git.archive.org/webgroup/scholar/trawler/fatcat2"
)

// fillBlanks copies into existing whatever it lacks from incoming, and returns
// the names of the fields it filled (nil if nothing changed). It never
// overwrites a value fatcat already has: an existing release came from a source
// we trust at least as much as an ETD aggregator, and a thesis record is mostly
// useful for what it adds.
//
// The one thing it always writes is extra.etd, which is ours.
func fillBlanks(existing *fatcat2.Release, incoming fatcat2.Release) []string {
	var filled []string
	setStr := func(name string, dst *string, src string) {
		if *dst == "" && src != "" {
			*dst = src
			filled = append(filled, name)
		}
	}

	setStr("release_type", &existing.Type, incoming.Type)
	setStr("release_stage", &existing.Stage, incoming.Stage)
	setStr("language", &existing.Language, incoming.Language)
	setStr("publisher", &existing.Publisher, incoming.Publisher)

	if existing.ReleaseDate == nil && incoming.ReleaseDate != nil {
		d := *incoming.ReleaseDate
		existing.ReleaseDate = &d
		filled = append(filled, "release_date")
	}
	if existing.ReleaseYear == 0 && incoming.ReleaseYear != 0 {
		existing.ReleaseYear = incoming.ReleaseYear
		filled = append(filled, "release_year")
	}

	// Lists are all-or-nothing: merging our author list into an existing one
	// would mostly produce duplicates spelled two ways.
	if len(existing.Contribs) == 0 && len(incoming.Contribs) > 0 {
		existing.Contribs = incoming.Contribs
		filled = append(filled, "contribs")
	}
	if len(existing.Abstracts) == 0 && len(incoming.Abstracts) > 0 {
		existing.Abstracts = incoming.Abstracts
		filled = append(filled, "abstracts")
	}

	// External IDs by type: add a type fatcat lacks, never a second value for
	// a type it has (that would be a conflict for a human, not a blank).
	have := map[string]bool{}
	for _, e := range existing.ExternalIDs {
		have[e.Type] = true
	}
	for _, e := range incoming.ExternalIDs {
		if !have[e.Type] {
			existing.ExternalIDs = append(existing.ExternalIDs, fatcat2.ExternalID{Type: e.Type, Value: e.Value})
			have[e.Type] = true
			filled = append(filled, "extid:"+e.Type)
		}
	}

	if etd, ok := incoming.Extra["etd"]; ok {
		if existing.Extra == nil {
			existing.Extra = map[string]any{}
		}
		if !reflect.DeepEqual(existing.Extra["etd"], etd) {
			// Copy rather than alias, so the caller's incoming release stays
			// independent of the one being sent.
			if m, ok := etd.(map[string]any); ok {
				etd = maps.Clone(m)
			}
			existing.Extra["etd"] = etd
			filled = append(filled, "extra.etd")
		}
	}

	return filled
}
