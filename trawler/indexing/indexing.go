package indexing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/google/uuid"
	"github.com/spf13/viper"
)

// TODO will probably kick client into the sig for these

// IndexRelease fetches a release from fc2 and indexes it into elasticsearch
func IndexRelease(rid uuid.UUID) error {
	client := &http.Client{}
	r, err := fatcat2.GetRelease(client, rid)
	if err != nil {
		return err
	}
	d := PrepareFatcatReleaseDoc(client, r)
	bs, err := json.Marshal(d)
	if err != nil {
		return err
	}

	return doElasticIndex(client, viper.GetString("indexing.fatcat_file_ix"), d.LegacyIdent, bs)
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

	return doElasticIndex(client, viper.GetString("indexing.fatcat_file_ix"), d.LegacyIdent, bs)
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

	return doElasticIndex(client, viper.GetString("indexing.fatcat_container_ix"), d.LegacyIdent, bs)
}

func doElasticIndex(client *http.Client, index string, docID string, doc []byte) error {
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
