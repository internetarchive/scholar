// sk-feed retrieves various upstream data sources. We start with using
// external programs, but aim towards less shelling out in the future.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/adrg/xdg"
	"github.com/klauspost/compress/zstd"
	"github.com/internetarchive/scholar/scholkit"
	"github.com/internetarchive/scholar/scholkit/dateutil"
	"github.com/internetarchive/scholar/scholkit/feeds"
	"github.com/internetarchive/scholar/scholkit/xflag"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/sethgrid/pester"
)

var docs = strings.TrimLeft(`
# skfeed - fetch data feeds

Uses mostly external tools to fetch raw bibliographic data from the web:
rclone, metha, dcdump.  NOTE: not all flags may work, e.g. -B backfill is not
fully implemented yet.

## external tools

$ sudo apt install rclone
$ go install -v github.com/miku/metha/cmd/...@latest
$ go install -v github.com/miku/dcdump/cmd/...@latest

## openalex

Hardcoded "aws" prefix, please add it to rclone.conf, cf.
https://docs.openalex.org/download-all-data/download-to-your-machine

	$ cat ~/.config/rclone/rclone.conf

	[aws]
	type = s3

## list feeds

$ sk-feed -l
openalex
crossref
datacite
pubmed
oai

## fetch feed

$ sk-feed -s openalex
$ sk-feed -s crossref

## flags

`, "\n")

var (
	defaultDataDir   = path.Join(xdg.DataHome, "schol")
	availableSources = []string{
		"openalex",
		"crossref",
		"datacite",
		"pubmed",
		"oai",
		// TODO: add dblp, doaj, wikicite (maybe), JALC
	}
	yesterday = time.Now().Add(-86400 * time.Second)
	oneDay    = 86400 * time.Second
	oneHour   = 3600 * time.Second
)

// Config for feeds, TODO(martin): move to config file and environment variables.
type Config struct {
	// DataDir is the generic data dir for all scholkit tools.
	DataDir string
	// FeedDir is the directory specifically for raw data feeds only. Can be
	// anything, but recommended to be a subdirectory of the DataDir.
	FeedDir string
	// Source is the name of the source.
	Source string
	// EndpointURL for OAI-PMH (not used currently)
	EndpointURL        string
	Date               time.Time
	MaxRetries         int
	Timeout            time.Duration
	CrossrefApiEmail   string
	CrossrefUserAgent  string
	CrossrefFeedPrefix string
	CrossrefApiFilter  string
	RcloneTransfers    int
	RcloneCheckers     int
	DataciteSyncStart  string
	PubMedApiKey       string
	PubMedFeedPrefix   string
	// S3 settings for SeaweedFS upload
	S3Upload    bool
	S3Endpoint  string
	S3AccessKey string
	S3SecretKey string
	S3Bucket    string
	S3Prefix    string
	S3UseSSL    bool
}

var (
	dir         = flag.String("d", defaultDataDir, "the main cache directory to put all data under") // TODO: use env var
	fetchSource = flag.String("s", "", "name of the the source to update")
	listSources = flag.Bool("l", false, "list available source names")
	showStatus  = flag.Bool("a", false, "show status and path")
	dateStr     = flag.String("t", yesterday.Format("2006-01-02"), "date to capture")
	runBackfill = flag.String("B", "", "run a backfill, if possible, from a given day (YYYY-MM-DD) on")
	maxRetries  = flag.Int("r", 3, "max retries")
	timeout     = flag.Duration("T", oneHour, "connectiont timeout")
	showVersion = flag.Bool("version", false, "show version")
	// rclone is used for openalex
	rcloneTransfers = flag.Int("rclone-transfers", 8, "number of parallel transfers for rclone")
	rcloneCheckers  = flag.Int("rclone-checkers", 16, "number of parallel checkers for rclone")
	// s3 upload options (used with crossref)
	s3Upload    = flag.Bool("s3-upload", false, "upload harvested data to SeaweedFS after writing to disk")
	s3Endpoint  = flag.String("s3-endpoint", "localhost:8333", "SeaweedFS S3 endpoint")
	s3AccessKey = flag.String("s3-access-key", "", "S3 access key")
	s3SecretKey = flag.String("s3-secret-key", "", "S3 secret key")
	s3Bucket    = flag.String("s3-bucket", "sandcrawler", "S3 bucket to upload crossref data into")
	s3Prefix    = flag.String("s3-prefix", "", "optional folder prefix within the S3 bucket (e.g. \"pubmed/daily\")")
	s3UseSSL    = flag.Bool("s3-use-ssl", false, "use SSL for S3 connection")
	// sync date range (applies to crossref and pubmed)
	syncStart xflag.Date = xflag.Date{Time: dateutil.MustParse("2020-01-01")}
	syncEnd   xflag.Date = xflag.Date{Time: yesterday}
	// crossref specific options
	crossrefApiEmail   = flag.String("crossref-api-email", "", "crossref api email, add for more gracious limits")
	crossrefApiFilter  = flag.String("crossref-api-filter", "index", "api filter to use with crossref")
	crossrefUserAgent  = flag.String("crossref-user-agent", "scholkit/dev", "crossref user agent")
	crossrefFeedPrefix = flag.String("crossref-feed-prefix", "crossref-feed-0-", "prefix for filename to distinguish different runs")
	// datacite specific options
	dataciteSyncStart xflag.Date = xflag.Date{Time: dateutil.MustParse("2020-01-01")}
	// pubmed specific options
	pubmedApiKey     = flag.String("pubmed-api-key", "", "NCBI API key (increases rate limit from 3 to 10 req/s)")
	pubmedFeedPrefix = flag.String("pubmed-feed-prefix", "pubmed-feed-0-", "prefix for pubmed feed filenames")
	// oai specific options
	endpointURL = flag.String("oai-endpoint", "", "endpoint URL for OAI")
)

func main() {
	flag.Var(&syncStart, "sync-start", "start date for harvest (crossref, pubmed)")
	flag.Var(&syncEnd, "sync-end", "end date for harvest (crossref, pubmed)")
	flag.Var(&dataciteSyncStart, "datacite-sync-start", "start date for datacite harvest")
	flag.Usage = func() {
		io.WriteString(os.Stderr, docs)
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println(scholkit.Version)
		os.Exit(0)
	}
	date, err := time.Parse("2006-01-02", *dateStr)
	if err != nil {
		log.Fatalf("invalid date: %v", err)
	}
	config := &Config{
		DataDir:            *dir,
		FeedDir:            path.Join(*dir, "feeds"),
		Source:             *fetchSource,
		EndpointURL:        *endpointURL,
		Date:               date,
		MaxRetries:         *maxRetries,
		Timeout:            *timeout,
		CrossrefApiEmail:   *crossrefApiEmail,
		CrossrefApiFilter:  *crossrefApiFilter,
		CrossrefUserAgent:  *crossrefUserAgent,
		CrossrefFeedPrefix: *crossrefFeedPrefix,
		RcloneTransfers:    *rcloneTransfers,
		RcloneCheckers:     *rcloneCheckers,
		DataciteSyncStart:  dataciteSyncStart.Format("2006-01-02"),
		PubMedApiKey:       *pubmedApiKey,
		PubMedFeedPrefix:   *pubmedFeedPrefix,
		S3Upload:           *s3Upload,
		S3Endpoint:         *s3Endpoint,
		S3AccessKey:        *s3AccessKey,
		S3SecretKey:        *s3SecretKey,
		S3Bucket:           *s3Bucket,
		S3Prefix:           *s3Prefix,
		S3UseSSL:           *s3UseSSL,
	}
	// Ensure feeds directory exists
	if err := os.MkdirAll(config.FeedDir, 0755); err != nil {
		log.Fatal(err)
	}
	// HTTP client
	client := pester.New()
	client.Backoff = pester.ExponentialBackoff
	client.MaxRetries = *maxRetries
	client.RetryOnHTTP429 = true
	client.Timeout = *timeout
	switch {
	case *showStatus:
		fmt.Printf("feeds: %s\n", config.FeedDir)
	case *listSources:
		for _, s := range availableSources {
			fmt.Println(s)
		}
	case config.Source != "":
		log.Printf("fetching %v [...]", config.Source)
		switch config.Source {
		case "openalex":
			// openalex is updated in roughly monthly intervals; after an
			// update an rclone sync may take a few hours to fetch data from
			// AWS bucket
			dst := path.Join(config.FeedDir, "openalex")
			if err := os.MkdirAll(dst, 0755); err != nil {
				log.Fatal(err)
			}
			cmd := exec.Command("rclone",
				"sync",
				fmt.Sprintf("--transfers=%d", config.RcloneTransfers),
				fmt.Sprintf("--checkers=%d", config.RcloneCheckers),
				"-P",
				"aws:/openalex",
				dst)
			log.Println(cmd)
			b, err := cmd.CombinedOutput() // TODO(martin): show live update w/ pipe
			if _, err := os.Stderr.Write(b); err != nil {
				log.Fatal(err)
			}
			if err != nil {
				log.Fatal(err)
			}
		case "crossref":
			ch := feeds.CrossrefHarvester{
				Client:              client,
				ApiEndpoint:         "https://api.crossref.org/works",
				ApiFilter:           config.CrossrefApiFilter,
				ApiEmail:            config.CrossrefApiEmail,
				Rows:                1000,
				UserAgent:           config.CrossrefUserAgent,
				AcceptableMissRatio: 0.1,
				MaxRetries:          3,
			}
			dstDir := path.Join(config.FeedDir, "crossref")
			if err := os.MkdirAll(dstDir, 0755); err != nil {
				log.Fatal(err)
			}
			log.Println(ch)
			var mc *minio.Client
			if config.S3Upload {
				mc, err = minio.New(config.S3Endpoint, &minio.Options{
					Creds:  credentials.NewStaticV4(config.S3AccessKey, config.S3SecretKey, ""),
					Secure: config.S3UseSSL,
				})
				if err != nil {
					log.Fatalf("s3 client: %v", err)
				}
			}
			ctx := context.Background()
			ivs := dateutil.Daily(syncStart.Time, syncEnd.Time)
			for _, iv := range ivs {
				// TODO: we only need the start date, because we limit
				// ourselves to day slices
				if err := ch.WriteDaySlice(iv.Start, dstDir, config.CrossrefFeedPrefix); err != nil {
					log.Fatalf("crossref day slice: %v", err)
				}
				if config.S3Upload {
					key, _, _ := ch.DaySliceKey(iv.Start, config.CrossrefFeedPrefix)
					localPath := path.Join(dstDir, key)
					s3Key := strings.TrimSuffix(key, ".json.zst") + ".ndjson"
					if config.S3Prefix != "" {
						s3Key = config.S3Prefix + "/" + s3Key
					}
					if _, statErr := mc.StatObject(ctx, config.S3Bucket, s3Key, minio.StatObjectOptions{}); statErr == nil {
						log.Printf("already in S3: %v", s3Key)
						continue
					}
					f, err := os.Open(localPath)
					if err != nil {
						log.Fatalf("open %s: %v", localPath, err)
					}
					dec, err := zstd.NewReader(f)
					if err != nil {
						f.Close()
						log.Fatalf("zstd reader: %v", err)
					}
					log.Printf("uploading to S3: %v", s3Key)
					_, err = mc.PutObject(ctx, config.S3Bucket, s3Key, dec, -1, minio.PutObjectOptions{
						ContentType: "application/x-ndjson",
					})
					dec.Close()
					f.Close()
					if err != nil {
						log.Fatalf("s3 upload %s: %v", s3Key, err)
					}
					fmt.Println(s3Key)
				}
			}
		case "datacite":
			// TODO: fix GOAWAY
			//
			// Mar 25 08:31:53 sk-feed[3837740]: time="2025-03-25T08:31:53Z" level=info msg="batch done: https://api.datacite.org/dois?affiliation=true&page%5Bcursor%5D=1&page%5Bsize%5D=100&query=updated%3A%5B2022-05-02T18%3A>
			// Mar 25 08:31:54 sk-feed[3837740]: time="2025-03-25T08:31:54Z" level=info msg="requests=1, pages=1, total=47"
			// Mar 25 08:31:54 sk-feed[3837740]: time="2025-03-25T08:31:54Z" level=info msg="batch done: https://api.datacite.org/dois?affiliation=true&page%5Bcursor%5D=1&page%5Bsize%5D=100&query=updated%3A%5B2022-05-02T18%3A>
			// Mar 25 08:31:54 sk-feed[3837740]: time="2025-03-25T08:31:54Z" level=info msg="failed to create file for https://api.datacite.org/dois?affiliation=true&page%5Bcursor%5D=1&page%5Bsize%5D=100&query=updated%3A%5B20>
			// Mar 25 08:31:54 sk-feed[3837740]: time="2025-03-25T08:31:54Z" level=warning msg="incomplete harvest - maybe rm -f /var/data/schol/feeds/datacite/dcdump-*.ndjson"
			// Mar 25 08:31:54 sk-feed[3837740]: time="2025-03-25T08:31:54Z" level=fatal msg="http2: server sent GOAWAY and closed the connection; LastStreamID=18849, ErrCode=NO_ERROR, debug=\"\""
			// Mar 25 08:31:54 sk-feed[3837724]: 2025/03/25 08:31:54 exit status 1
			// Mar 25 08:31:54 systemd[1]: sk-feed-datacite.service: Main process exited, code=exited, status=1/FAILURE
			// Mar 25 08:31:54 systemd[1]: sk-feed-datacite.service: Failed with result 'exit-code'.
			// Mar 25 08:31:54 systemd[1]: Failed to start Harvest metadata from api.datacite.org.
			dstDir := path.Join(config.FeedDir, "datacite")
			if err := os.MkdirAll(dstDir, 0755); err != nil {
				log.Fatal(err)
			}
			cmd := exec.Command("dcdump",
				"-s", config.DataciteSyncStart,
				"-e", date.Add(oneDay).Format("2006-01-02"),
				"-i", "e", // most fine granular, takes a while to backfill
				"-d", dstDir)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			log.Println(cmd)
			if err = cmd.Run(); err != nil {
				log.Fatal(err)
			}
		case "pubmed":
			dstDir := path.Join(config.FeedDir, "pubmed")
			if err := os.MkdirAll(dstDir, 0755); err != nil {
				log.Fatal(err)
			}
			// Use the API-based day-slice harvester when -pubmed-sync-start is
			// explicitly provided; otherwise fall back to FTP bulk download.
			var useAPIHarvester bool
			flag.Visit(func(f *flag.Flag) {
				if f.Name == "sync-start" {
					useAPIHarvester = true
				}
			})
			if useAPIHarvester {
				h := feeds.PubMedHarvester{
					Client: client,
					ApiKey: config.PubMedApiKey,
				}
				var mc *minio.Client
				if config.S3Upload {
					mc, err = minio.New(config.S3Endpoint, &minio.Options{
						Creds:  credentials.NewStaticV4(config.S3AccessKey, config.S3SecretKey, ""),
						Secure: config.S3UseSSL,
					})
					if err != nil {
						log.Fatalf("s3 client: %v", err)
					}
				}
				ctx := context.Background()
				ivs := dateutil.Daily(syncStart.Time, syncEnd.Time)
				for _, iv := range ivs {
					if err := h.WriteDaySlice(iv.Start, dstDir, config.PubMedFeedPrefix); err != nil {
						log.Fatalf("pubmed day slice: %v", err)
					}
					if config.S3Upload {
						key, _, _ := h.DaySliceKey(iv.Start, config.PubMedFeedPrefix)
						localPath := path.Join(dstDir, key)
						s3Key := strings.TrimSuffix(key, ".json.zst") + ".ndjson"
						if config.S3Prefix != "" {
							s3Key = config.S3Prefix + "/" + s3Key
						}
						if _, statErr := mc.StatObject(ctx, config.S3Bucket, s3Key, minio.StatObjectOptions{}); statErr == nil {
							log.Printf("already in S3: %v", s3Key)
							continue
						}
						f, err := os.Open(localPath)
						if err != nil {
							log.Fatalf("open %s: %v", localPath, err)
						}
						dec, err := zstd.NewReader(f)
						if err != nil {
							f.Close()
							log.Fatalf("zstd reader: %v", err)
						}
						log.Printf("uploading to S3: %v", s3Key)
						_, err = mc.PutObject(ctx, config.S3Bucket, s3Key, dec, -1, minio.PutObjectOptions{
							ContentType: "application/x-ndjson",
						})
						dec.Close()
						f.Close()
						if err != nil {
							log.Fatalf("s3 upload %s: %v", s3Key, err)
						}
						fmt.Println(s3Key)
					}
				}
			} else {
				// Download baseline files.
				log.Println("syncing pubmed baseline...")
				fetcher, err := feeds.NewPubMedFetcher("https://ftp.ncbi.nlm.nih.gov/pubmed/baseline/")
				if err != nil {
					log.Fatal(err)
				}
				pmfs, err := fetcher.FetchFiles()
				if err != nil {
					log.Fatal(err)
				}
				log.Printf("found %d pubmed baseline files", len(pmfs))
				for _, pmf := range pmfs {
					dstFile := path.Join(dstDir, pmf.Filename)
					wip := dstFile + ".wip"
					if _, err := os.Stat(dstFile); os.IsNotExist(err) {
						cmd := exec.Command("curl", "-sL", "--retry", "10", "--max-time", "1800", "-o", wip, pmf.URL)
						cmd.Stdout = os.Stdout
						cmd.Stderr = os.Stderr
						log.Println(cmd)
						if err = cmd.Run(); err != nil {
							log.Fatal(err)
						}
						if err := os.Rename(wip, dstFile); err != nil {
							log.Fatal(err)
						}
					} else {
						log.Printf("already synced: %v", dstFile)
					}
				}
				log.Println("syncing pubmed updates...")
				fetcher, err = feeds.NewPubMedFetcher("https://ftp.ncbi.nlm.nih.gov/pubmed/updatefiles/")
				if err != nil {
					log.Fatal(err)
				}
				pmfs, err = fetcher.FetchFiles()
				if err != nil {
					log.Fatal(err)
				}
				log.Printf("found %d pubmed update files", len(pmfs))
				for _, pmf := range pmfs {
					dstFile := path.Join(dstDir, pmf.Filename)
					wip := dstFile + ".wip"
					if _, err := os.Stat(dstFile); os.IsNotExist(err) {
						cmd := exec.Command("curl", "-sL", "--retry", "10", "--max-time", "600", "-o", wip, pmf.URL)
						cmd.Stdout = os.Stdout
						cmd.Stderr = os.Stderr
						log.Println(cmd)
						if err = cmd.Run(); err != nil {
							log.Fatal(err)
						}
						if err := os.Rename(wip, dstFile); err != nil {
							log.Fatal(err)
						}
					} else {
						log.Printf("already synced: %v", dstFile)
					}
				}
			}
		case "oai":
			baseDir := path.Join(config.FeedDir, "metha")
			cmd := exec.Command("metha-sync",
				"-base-dir", baseDir,
				*endpointURL)
			log.Println(cmd)
			if _, err = cmd.CombinedOutput(); err != nil {
				log.Fatal(err)
			}
		}
	}
}
