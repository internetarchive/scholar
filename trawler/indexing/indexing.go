package indexing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/s3"
	"github.com/google/uuid"
	"github.com/spf13/viper"
)

// TODO will probably kick client into the sig for these
type ByUrlPriority []fatcat2.File

func (fs ByUrlPriority) Len() int      { return len(fs) }
func (fs ByUrlPriority) Swap(i, j int) { fs[i], fs[j] = fs[j], fs[i] }
func (fs ByUrlPriority) Less(i, j int) bool {
	for _, u := range fs[i].URLs {
		if strings.Contains(u.URL, "//web.archive.org") {
			return true
		}
	}
	return false
}

// IndexFulltext fetches a release from fc2 and related pdf artifacts for any files from seaweed
func IndexFulltext(rid uuid.UUID) error {
	// it's debateable that this is a good use of time but i think it's worth the effort...just have to make sure i'm working on stuff that is populated in seaweed

	client := &http.Client{}
	release, err := fatcat2.GetRelease(client, rid)
	if err != nil {
		return err
	}

	var container *fatcat2.Container
	if release.ContainerID != nil {
		c, err := fatcat2.GetContainer(client, *release.ContainerID)
		if err != nil {
			return err
		}
		container = &c
	}

	files, err := fatcat2.ReleaseFiles(client, rid)
	if err != nil {
		return err
	}

	sort.Sort(ByUrlPriority(files))

	ctx := context.Background()
	s3bucket := viper.GetString("blobproc.s3bucket")
	var grobidXML []byte
	var pdfText []byte
	var file *fatcat2.File
	for _, f := range files {
		s3Key := fmt.Sprintf("%s/%s/%s/%s/%s.txt",
			s3bucket, "grobid", f.Sha1[0:2], f.Sha1[2:4], f.Sha1)
		obj, err := s3.GetObject(ctx, s3Key)
		if err != nil {
			continue
		}

		grobidXML, err = io.ReadAll(obj)
		if err != nil {
			return fmt.Errorf("could not read '%s': %w", s3Key, err)
		}
		file = &f
	}

	for _, f := range files {
		s3Key := fmt.Sprintf("%s/%s/%s/%s/%s.txt",
			s3bucket, "text", f.Sha1[0:2], f.Sha1[2:4], f.Sha1)
		obj, err := s3.GetObject(ctx, s3Key)
		if err != nil {
			continue
		}

		pdfText, err = io.ReadAll(obj)
		if err != nil {
			return fmt.Errorf("could not read '%s': %w", s3Key, err)
		}

		if file == nil {
			file = &f
		}
	}

	if grobidXML == nil && pdfText == nil {
		return errors.New("could not find any full text in s3")
	}

	tctx := FulltextTransformCtx{
		HttpClient: client,
		Release:    release,
		Container:  container,
		File:       file,
		GrobidXML:  grobidXML,
		PdfText:    pdfText,
	}

	esDoc := PrepareFulltextDoc(tctx)

	bs, err := json.Marshal(esDoc)
	if err != nil {
		return err
	}

	return DoElasticIndex(client, viper.GetString("indexing.fulltext_ix"), esDoc.Key, bs)
}

// IndexRelease fetches a release from fc2 and indexes it into elasticsearch
func IndexRelease(rid uuid.UUID) error {
	client := &http.Client{}
	r, err := fatcat2.GetRelease(client, rid)
	if err != nil {
		return err
	}
	d, err := PrepareFatcatReleaseDoc(client, r)
	if err != nil {
		return err
	}
	bs, err := json.Marshal(d)
	if err != nil {
		return err
	}

	return DoElasticIndex(client, viper.GetString("indexing.fatcat_release_ix"), d.LegacyIdent, bs)
}

// IndexFile fetches a file from fc2 and indexes it into elasticsearch
func IndexFile(fid uuid.UUID) error {
	client := &http.Client{}
	f, err := fatcat2.GetFile(client, fid)
	if err != nil {
		return err
	}
	d := PrepareFatcatFileDoc(f)
	bs, err := json.Marshal(d)
	if err != nil {
		return err
	}

	return DoElasticIndex(client, viper.GetString("indexing.fatcat_file_ix"), d.LegacyIdent, bs)
}

// IndexContainer fetches a container from fc2 api and indexes it in elasticsearch
func IndexContainer(cid uuid.UUID) error {
	client := &http.Client{}
	c, err := fatcat2.GetContainer(client, cid)
	if err != nil {
		return err
	}

	d := PrepareFatcatContainerDoc(c)

	bs, err := json.Marshal(d)
	if err != nil {
		return err
	}

	return DoElasticIndex(client, viper.GetString("indexing.fatcat_container_ix"), d.LegacyIdent, bs)
}

func DoElasticIndex(client *http.Client, index string, docID string, doc []byte) error {
	u := fmt.Sprintf("%s/%s/_doc/%s",
		viper.GetString("indexing.elasticsearch_url"),
		index, docID)

	req, err := http.NewRequest("POST", u, bytes.NewReader(doc))
	if err != nil {
		return fmt.Errorf("could not prepare elasticsearch POST: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to POST elasticsearch: %w", err)
	}

	if resp.StatusCode > 299 || resp.StatusCode < 200 {
		var body string
		bs, err := io.ReadAll(resp.Body)
		if err == nil {
			body = string(bs)
		}

		return fmt.Errorf("elasticsearch failed to index: '%s'", body)
	}

	return nil
}
