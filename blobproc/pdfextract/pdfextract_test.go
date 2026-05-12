package pdfextract

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"image/jpeg"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/internetarchive/scholar/blobproc/pdfinfo"
)

// TestPdfExtract uses a snapshot style test. If the expected JSON files are
// removed, the test will not fail, but will write out the current
// serialization out. This is useful, if processing has changed significantly,
// and it is easier to remove the previous snapshot and to generate a new one
// instead.
func TestPdfExtract(t *testing.T) {
	var cases = []struct {
		filename string
		dim      Dim
		status   string
		snapshot string
		links    []string
	}{
		{
			filename: "../testdata/pdf/1906.02444.pdf",
			dim:      Dim{180, 300},
			status:   "success",
			snapshot: "../testdata/extract/1906.02444.json",
			links:    nil,
		},
		{
			filename: "../testdata/pdf/1906.11632.pdf",
			dim:      Dim{180, 300},
			status:   "success",
			snapshot: "../testdata/extract/1906.11632.json",
			links: []string{
				"http://arXiv",
				"http://arxiv",
				"http://arxiv.org/abs/1607.07539",
				"http://arxiv.org/abs/1805.06725",
				"http://dblp.uni-trier.de/db/journals/",
				"http://kdd.ics.uci.edu/",
				"http://papers.nips.cc/paper/",
				"http://yann.lecun.com/exdb/",
				"https://www.tensorflow.org/",
			},
		},
		{
			filename: "../testdata/pdf/1906.11964.pdf",
			dim:      Dim{180, 300},
			status:   "success",
			snapshot: "../testdata/extract/1906.11964.json",
			links: []string{
				"http://arxiv.org/abs/1902.02534",
				"http://arxiv.org/abs/1902.03287",
				"http://arxiv.org/abs/1906.06039",
				"http://citationgecko.com",
				"http://excite.west.uni-koblenz.de",
				"http://issi-society.org/open-citations-letter",
				"http://opencitations.net",
				"http://opencitations.net/corpus",
				"http://opencitations.net/download",
				"http://opencitations.net/index",
				"http://opencitations.net/index/coci",
				"http://opencitations.net/index/croci",
				"http://opencitations.net/oci",
				"http://scoss.org",
				"http://visualbib.uniud.it/",
				"http://www.sparontologies.net",
				"http://www.vosviewer.com",
				"https://api.crossref.org",
				"https://arxiv.org/abs/1904.06052",
				"https://blog.research-plus.library.manchester.ac.uk/2019/03/04/using-open-citation-data-to-ident",
				"https://chanzuckerberg.com",
				"https://choosealicense.com/licenses/isc/",
				"https://creativecommons.org/licenses/by/4.0/",
				"https://creativecommons.org/publicdomain/zero/1.0/",
				"https://doi.org/10.1007/978-3-030-00668-6_8",
				"https://doi.org/10.1007/978-3-030-01379-0_6",
				"https://doi.org/10.1007/978-3-319-68204-4_19",
				"https://doi.org/10.1007/s11192-009-0146-3",
				"https://doi.org/10.1038/sdata.2016.18",
				"https://doi.org/10.1145/3197026.3197050",
				"https://doi.org/10.3233/DS-190016",
				"https://doi.org/10.5281/zenodo.1066316",
				"https://doi.org/10.6084/m9.figshare.1314859",
				"https://doi.org/10.6084/m9.figshare.3443876",
				"https://doi.org/10.6084/m9.figshare.6683855",
				"https://doi.org/10.6084/m9.figshare.7127816",
				"https://doi.org/10.6084/m9.figshare.8050352.v2",
				"https://dossier-ng.univ-st-etienne.fr/scd/www/oci/OCI_graphe_accueil.html",
				"https://en.wikipedia.org/wiki/Sandbox_(software_development)",
				"https://europepmc.org/RestfulWebService",
				"https://github.com/opencitations",
				"https://github.com/opencitations/lucinda",
				"https://github.com/opencitations/oscar",
				"https://github.com/opencitations/ramose",
				"https://i4oc.org",
				"https://identifiers.org/oci",
				"https://investinopen.org",
				"https://locdb.bib.uni-mannheim.de",
				"https://opencitations.wordpress.com/2018/02/19/citations-as-first-class-data-entities-introduction",
				"https://opencitations.wordpress.com/2018/12/23/the-wellcome-trust-funds-opencitations/",
				"https://orcid.org/0000-0001-5506-523X",
				"https://orcid.org/0000-0003-0530-4305",
				"https://philippmayr.github.io/papers/JCDL2019-EXCITE-demo.pdf",
				"https://sloan.org",
				"https://sloan.org/grant-detail/8017",
				"https://venicescholar.dhlab.epfl.ch",
				"https://w3id.org/oc/corpus/br/1",
				"https://w3id.org/oc/index/api/v1",
				"https://w3id.org/oc/ontology",
				"https://wellcome.ac.uk/funding/people-and-projects/grants-awarded/open-biomedical-citations-c",
				"https://www.arcadiafund.org.uk",
				"https://www.coar-repositories.org/files/NGR-Final-Formatted-Report-cc.pdf",
				"https://www.crossref.org/",
				"https://www.crossref.org/reference-distribution/",
				"https://www.force11.org",
				"https://www.ncbi.nlm.nih.gov/pmc/tools/openftlist/",
				"https://www.project-freya.eu",
				"https://www.project-freya.eu/en/deliverables/freya_d3-1.pdf",
				"https://www.w3.org/TR/rdf11-concepts/",
				"https://www.w3.org/TR/sparql11-query/",
			},
		},
		{
			filename: "../testdata/misc/wordle.py",
			dim:      Dim{180, 300},
			status:   "not-pdf",
		},
	}
	for _, c := range cases {
		result := ProcessFile(context.Background(), c.filename, &Options{
			Dim:       c.dim,
			ThumbType: "jpg",
		})
		if result.Status != c.status {
			t.Fatalf("got %v, want %v (%s, %v)", result.Status, c.status, c.filename, result.Err)
		}
		if result.Status != "success" {
			continue
		}
		var want Result
		if _, err := os.Stat(c.snapshot); os.IsNotExist(err) {
			f, err := os.CreateTemp("", "blobproc-pdf-snapshot-*.json")
			if err != nil {
				t.Fatalf("could not create snapshot: %v", err)
			}
			defer f.Close()
			if err := json.NewEncoder(f).Encode(result); err != nil {
				t.Fatalf("encode: %v", err)
			}
			if err := f.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if err := Move(f.Name(), c.snapshot); err != nil {
				t.Fatalf("rename: %v", err)
			}
			t.Logf("created new snapshot: %v", c.snapshot)
		}
		b, err := os.ReadFile(c.snapshot)
		if err != nil {
			t.Fatalf("cannot read snapshot: %v", c.snapshot)
		}
		if err := json.Unmarshal(b, &want); err != nil {
			t.Fatalf("snapshot broken: %v", err)
		}
		// Ignore Page0Thumbnail in struct comparison - JPEG output varies across
		// renderer versions and platforms.
		cmpOpts := []cmp.Option{
			cmpopts.EquateEmpty(),
			cmpopts.IgnoreFields(Result{}, "Page0Thumbnail"),
			cmpopts.IgnoreFields(pdfinfo.PDFCPUInfo{}, "Keywords", "Source", "CreationDate"),
			cmpopts.IgnoreFields(pdfinfo.Info{}, "CreationDate", "ModDate"),
			cmp.FilterPath(func(p cmp.Path) bool {
				return p.String() == "Metadata.PDFCPU.Header"
			}, cmp.Ignore()),
		}
		if !cmp.Equal(result, &want, cmpOpts...) {
			// If we fail, we write the result JSON to a tempfile for later
			// inspection or snapshot creation.
			t.Logf("file: %v, snapshot: %v", c.filename, c.snapshot)
			t.Fatalf("diff: %v", cmp.Diff(result, &want, cmpOpts...))
		}
		// Validate thumbnail separately: check it's a valid JPEG with reasonable size.
		if len(want.Page0Thumbnail) > 0 {
			if len(result.Page0Thumbnail) == 0 {
				t.Fatalf("expected thumbnail but got none for %v", c.filename)
			}
			// Verify it's a valid JPEG by attempting to decode it.
			img, err := jpeg.Decode(bytes.NewReader(result.Page0Thumbnail))
			if err != nil {
				t.Fatalf("thumbnail is not valid JPEG for %v: %v", c.filename, err)
			}
			// Check dimensions are reasonable (non-zero).
			bounds := img.Bounds()
			if bounds.Dx() == 0 || bounds.Dy() == 0 {
				t.Fatalf("thumbnail has zero dimensions for %v", c.filename)
			}
		}
		// Check link extraction.
		if !cmp.Equal(result.Weblinks, c.links, cmpopts.EquateEmpty()) {
			t.Logf("file: %v, snapshot: %v", c.filename, c.snapshot)
			t.Fatalf("diff: %v", cmp.Diff(result.Weblinks, c.links, cmpopts.EquateEmpty()))
		}
	}
}

//go:embed testdata/pdf/1906.02444.pdf
var testdataPdf1 []byte

func TestGenerateFileInfo(t *testing.T) {
	var cases = []struct {
		data   []byte
		result FileInfo
	}{
		{
			data: []byte{},
			result: FileInfo{
				Size:      0,
				MD5Hex:    "d41d8cd98f00b204e9800998ecf8427e",
				SHA1Hex:   "da39a3ee5e6b4b0d3255bfef95601890afd80709",
				SHA256Hex: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				Mimetype:  "text/plain",
			},
		},
		{
			data: testdataPdf1,
			result: FileInfo{
				Size:      633850,
				MD5Hex:    "e04a100bc6a02efbf791566d6cb62bc9",
				SHA1Hex:   "4e6ca8dfc787a8b33e92773df3674fadf4d4cdb6",
				SHA256Hex: "31d0504caf44007be46d5aa64640dc2c1054aa7f4f404851f3a40c06d4ed7008",
				Mimetype:  "application/pdf",
			},
		},
	}
	for _, c := range cases {
		var fi FileInfo
		fi.FromBytes(c.data)
		if !reflect.DeepEqual(fi, c.result) {
			t.Fatalf("got %v, want %v", fi, c.result)
		}
	}
}

func BenchmarkPdfExtract(b *testing.B) {
	for n := 0; n < b.N; n++ {
		_ = ProcessFile(context.Background(), "testdata/pdf/1906.02444.pdf", &Options{
			Dim:       Dim{180, 300},
			ThumbType: "na",
		})
	}
}

func Move(source, destination string) error {
	err := os.Rename(source, destination)
	if err != nil && strings.Contains(err.Error(), "invalid cross-device link") {
		return moveCrossDevice(source, destination)
	}
	return err
}

func moveCrossDevice(source, destination string) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	dst, err := os.Create(destination)
	if err != nil {
		_ = src.Close()
		return err
	}
	_, err = io.Copy(dst, src)
	if err != nil {
		return err
	}
	_ = src.Close()
	_ = dst.Close()
	fi, err := os.Stat(source)
	if err != nil {
		_ = os.Remove(destination)
		return err
	}
	err = os.Chmod(destination, fi.Mode())
	if err != nil {
		_ = os.Remove(destination)
		return err
	}
	return os.Remove(source)
}
