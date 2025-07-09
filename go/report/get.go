package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

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
		return -1, fmt.Errorf("failed to talk to ES: %w", err)
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

	if err = json.Unmarshal(rbody, &esCount); err != nil {
		return -1, fmt.Errorf("could not parse ES response: %w", err)
	}

	return esCount.Count, nil
}

func getFatcatStats(c *http.Client) (fatcatStats, error) {
	fcStats := fatcatStats{}
	req, err := http.NewRequest(http.MethodGet, fcURL, nil)
	if err != nil {
		return fcStats, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.Do(req)
	if err != nil {
		return fcStats, fmt.Errorf("failed to talk to fatcat: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fcStats, fmt.Errorf("got %d from fatcat", resp.StatusCode)
	}

	rbody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fcStats, fmt.Errorf("could not read fatcat response: %w", err)
	}

	if err = json.Unmarshal(rbody, &fcStats); err != nil {
		return fcStats, fmt.Errorf("could not parse fatcat response: %w", err)
	}

	return fcStats, nil
}

func getSandcrawlerStats(c *http.Client) (sandcrawlerStats, error) {
	scStats := sandcrawlerStats{}

	scQuery := func(path string) ([]byte, error) {
		req, err := http.NewRequest(http.MethodGet, scURL+"/"+path, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		resp, err := c.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to talk to sandcrawler: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("got %d from sandcrawler", resp.StatusCode)
		}

		rbody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("could not read sandcrawler response: %w", err)
		}

		return rbody, nil
	}

	rbody, err := scQuery("stat_failed_pdf")
	if err != nil {
		return scStats, fmt.Errorf("sc stat_failed_pdf call failed: %w", err)
	}

	// NB after we came back from power outage this was no longer returning json
	// but just a bare number. I am not sure why.
	//type failed struct {
	//	Count int `json:"stat_failed_pdf"`
	//}
	//fparsed := []failed{}
	//err = json.Unmarshal(rbody, &fparsed)
	//if err != nil {
	//	return scStats, fmt.Errorf("could not parse stat_failed_pdf response: %w", err)
	//}
	//if len(fparsed) > 0 {
	//	scStats.PDFMiss = fparsed[0].Count
	//}
	missCount, err := strconv.Atoi(string(rbody))
	if err != nil {
		return scStats, fmt.Errorf("could not parse stat_failed_pdf response: %w", err)
	}
	scStats.PDFMiss = missCount

	rbody, err = scQuery("stat_got_pdf")
	if err != nil {
		return scStats, fmt.Errorf("sc stat_got_pdf call failed: %w", err)
	}

	// NB after we came back from power outage this was no longer returning json
	// but just a bare number. I am not sure why.
	//type got struct {
	//	Count int `json:"stat_got_pdf"`
	//}
	//gparsed := []got{}
	//err = json.Unmarshal(rbody, &gparsed)
	//if err != nil {
	//	return scStats, fmt.Errorf("could not parse stat_got_pdf response: %w", err)
	//}
	//if len(gparsed) > 0 {
	//	scStats.PDFHit = gparsed[0].Count
	//}
	hitCount, err := strconv.Atoi(string(rbody))
	if err != nil {
		return scStats, fmt.Errorf("could not parse stat_got_pdf response: %w", err)
	}
	scStats.PDFHit = hitCount

	rbody, err = scQuery("stat_error_counts")
	if err != nil {
		return scStats, fmt.Errorf("sc stat_got_pdf call failed: %w", err)
	}

	rparsed := []pdfMissReasons{}
	err = json.Unmarshal(rbody, &rparsed)
	if err != nil {
		return scStats, fmt.Errorf("could not parse stat_error_counts response: %w", err)
	}

	scStats.PDFMissReasons = rparsed

	return scStats, nil
}
