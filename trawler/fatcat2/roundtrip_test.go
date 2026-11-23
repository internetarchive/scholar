package fatcat2

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

// fc2's PUT replaces an entity wholesale, so a GET-modify-PUT through these
// structs is only safe if decoding a GET body and encoding it again yields
// something the server reads as the same entity. These tests hold the structs
// to that against real production bodies in testdata/.
//
// "Reads as the same" is looser than byte-identical, in exactly the ways fc2's
// PUT handling is (see djscholar fcapi/test_roundtrip.py for the server side):
//   - a key may go missing if its value was null: fc2 reads absent as NULL.
//   - a child list may go missing if it was empty: fc2 reads absent as [].
//   - updated, hidden_when and hidden_reason may go missing from any entity
//     (an object with an id, including releases embedded in a file): the
//     server maintains or preserves them when absent. created may NOT: until
//     Nov 2026 fc2 nulled it out when absent, which 500s on its NOT NULL
//     column -- this allowance is how that got past these tests once.
//   - a key may appear if its value is null and fc2 ignores it (legacy_rev_id).
// Anything else -- a value that changed, a non-empty list or non-null value
// that vanished -- is data a PUT would destroy.

var serverMaintained = map[string]bool{
	"updated": true, "hidden_when": true, "hidden_reason": true,
}

var ignoredByServer = map[string]bool{"legacy_rev_id": true}

func roundTripDiffs(path string, orig, back any) []string {
	switch o := orig.(type) {
	case map[string]any:
		b, ok := back.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: object became %#v", path, back)}
		}
		_, isEntity := o["id"]
		var out []string
		for k, ov := range o {
			bv, present := b[k]
			if !present {
				if ov == nil || (isEntity && serverMaintained[k]) {
					continue
				}
				if l, isList := ov.([]any); isList && len(l) == 0 {
					continue
				}
				out = append(out, fmt.Sprintf("%s.%s: dropped (was %v)", path, k, ov))
				continue
			}
			out = append(out, roundTripDiffs(path+"."+k, ov, bv)...)
		}
		for k, bv := range b {
			if _, inOrig := o[k]; !inOrig && !(bv == nil && ignoredByServer[k]) {
				out = append(out, fmt.Sprintf("%s.%s: added (%v)", path, k, bv))
			}
		}
		return out
	case []any:
		b, ok := back.([]any)
		if !ok || len(b) != len(o) {
			return []string{fmt.Sprintf("%s: list %v became %v", path, orig, back)}
		}
		var out []string
		for i := range o {
			out = append(out, roundTripDiffs(fmt.Sprintf("%s[%d]", path, i), o[i], b[i])...)
		}
		return out
	default:
		if !reflect.DeepEqual(orig, back) {
			return []string{fmt.Sprintf("%s: %#v became %#v", path, orig, back)}
		}
		return nil
	}
}

func assertRoundTrips[T any](t *testing.T, body []byte) {
	t.Helper()
	var orig map[string]any
	if err := json.Unmarshal(body, &orig); err != nil {
		t.Fatal(err)
	}
	var entity T
	if err := json.Unmarshal(body, &entity); err != nil {
		t.Fatalf("decode into %T: %v", entity, err)
	}
	out, err := json.Marshal(&entity)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	for _, d := range roundTripDiffs("", orig, back) {
		t.Error(d)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	bs, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return bs
}

func Test_Release_RoundTrip(t *testing.T) {
	for _, name := range []string{
		// 29 citations, null dates and nullable strings
		"roundtrip_release_citations.json",
		// contribs with null given_name/surname/raw_affiliation, raw refs
		"roundtrip_release_contribs.json",
	} {
		t.Run(name, func(t *testing.T) {
			assertRoundTrips[Release](t, readFixture(t, name))
		})
	}
}

// Legacy-imported contribs often have no position, which must not come back as
// 0. None of the fixtures happen to have one, so make one.
func Test_Release_RoundTrip_NullPosition(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(readFixture(t, "roundtrip_release_contribs.json"), &doc); err != nil {
		t.Fatal(err)
	}
	doc["contribs"].([]any)[0].(map[string]any)["position"] = nil
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	assertRoundTrips[Release](t, body)
}

func Test_File_RoundTrip(t *testing.T) {
	assertRoundTrips[File](t, readFixture(t, "roundtrip_file.json"))
}

// The checker has to be able to fail: a body with data the structs do not
// carry must be reported.
func Test_roundTripDiffs_catchesLoss(t *testing.T) {
	orig := map[string]any{
		"citations": []any{map[string]any{"position": 1.0}},
		"id":        "x",
		"title":     "x",
		"subtitle":  nil,
	}
	back := map[string]any{"id": "x", "title": "y"}
	if got := roundTripDiffs("", orig, back); len(got) != 2 {
		t.Errorf("want a dropped citations and a changed title, got %v", got)
	}
}
