package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/miku/blobproc"
	"github.com/miku/blobproc/config"
	"github.com/miku/blobproc/pdfextract"
	"github.com/miku/grobidclient"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	cfgFile string
	v       *viper.Viper
	cfg     *config.Config
)

// Root command
var rootCmd = &cobra.Command{
	Use:   "blobproc",
	Short: "Process and persist PDF derivatives",
	Long: `BLOBPROC is a PDF postprocessing utility that generates derivatives
like fulltext, thumbnails, and metadata from PDF files and can persist them to S3.

Examples:
  blobproc run                    # Process files from spool directory (sequential)
  blobproc run -w 4               # Process with 4 parallel workers
  blobproc single file.pdf        # Process single file for testing
  blobproc config                 # Show current configuration
  blobproc serve                  # Start server to accept and store pdfs`,
}

// Run command - process files from spool
var runCmd = &cobra.Command{
	Use:   "run [flags]",
	Short: "Process files from spool directory",
	Long: `Process all PDF files in the spool directory, generating
derivatives and storing them in S3. This is the main processing mode.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runProcessor()
	},
}

// Single command - process single file
var singleCmd = &cobra.Command{
	Use:   "single [flags] <file>",
	Short: "Process a single file for testing",
	Long: `Process a single PDF file using local tools only.
Emits JSON with extracted data to stdout.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSingleFile(args[0])
	},
}

// Config command - show configuration
var configCmd = &cobra.Command{
	Use:   "config [flags]",
	Short: "Show current configuration",
	Long: `Display the current configuration values, showing where each
value comes from (default, config file, environment variable, or flag).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return showConfig()
	},
}

// Serve command - run HTTP server
var serveCmd = &cobra.Command{
	Use:   "serve [flags]",
	Short: "Run HTTP server to receive PDF blobs",
	Long: `Start an HTTP server that receives binary PDF data via POST or PUT
requests and stores them in the spool folder for later processing.

The server provides the following endpoints:
  POST/PUT /spool    - Upload a PDF blob
  GET /spool         - List spool contents
  GET /spool/{id}    - Get status of a specific spool item`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServer()
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "print version and exit",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(blobproc.Version)
	},
}

func main() {
	Execute()
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	// Set up the PersistentPreRunE hook (must be done here to avoid initialization cycle)
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		return initConfig()
	}

	// Add subcommands
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(singleCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(versionCmd)

	// Global flags (using hardcoded defaults - use 'blobproc config' to see effective values)
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", fmt.Sprintf("config file (searches: ./blobproc.yaml, %s/.config/blobproc/blobproc.yaml, /etc/blobproc/blobproc.yaml)", os.Getenv("HOME")))
	rootCmd.PersistentFlags().Bool("debug", config.DefaultDebug, "enable debug logging")
	rootCmd.PersistentFlags().String("spool-dir", config.DefaultSpoolDir, "spool directory path")
	rootCmd.PersistentFlags().String("log-file", "", "log file path (empty = stderr)")
	rootCmd.PersistentFlags().Duration("timeout", config.DefaultTimeout, "subprocess timeout")

	// S3 flags (global - used by run and single)
	rootCmd.PersistentFlags().String("s3-endpoint", config.DefaultS3Endpoint, "S3 endpoint")
	rootCmd.PersistentFlags().String("s3-access-key", config.DefaultS3AccessKey, "S3 access key")
	rootCmd.PersistentFlags().String("s3-secret-key", config.DefaultS3SecretKey, "S3 secret key")
	rootCmd.PersistentFlags().String("s3-default-bucket", config.DefaultS3Bucket, "S3 default bucket")
	rootCmd.PersistentFlags().Bool("s3-use-ssl", config.DefaultS3UseSSL, "use SSL for S3 connections")

	// GROBID flags (global - used by run and single)
	rootCmd.PersistentFlags().String("grobid-host", config.DefaultGrobidHost, "GROBID host URL")
	rootCmd.PersistentFlags().Int64("grobid-max-filesize", config.DefaultGrobidMaxSize, "max file size for GROBID in bytes")
	rootCmd.PersistentFlags().Duration("grobid-timeout", config.DefaultGrobidTimeout, "GROBID request timeout")

	// Run-specific flags
	runCmd.Flags().IntP("workers", "w", config.DefaultWorkers, "number of parallel workers (1=sequential, >1=parallel)")
	runCmd.Flags().BoolP("keep", "k", config.DefaultKeepSpool, "keep files in spool after processing")

	// Config-specific flags
	configCmd.Flags().Bool("show-defaults", false, "show default configuration values")
	configCmd.Flags().Bool("show-file", false, "show config file location")

	// Serve-specific flags
	serveCmd.Flags().String("addr", config.DefaultServerAddr, "server listen address")
	serveCmd.Flags().Duration("server-timeout", config.DefaultServerTimeout, "server read/write timeout")
	serveCmd.Flags().String("access-log", config.DefaultAccessLog, "access log file (empty = discard)")
	serveCmd.Flags().String("urlmap-file", config.DefaultURLMapFile, "URL map database file (empty = disabled)")
	serveCmd.Flags().String("urlmap-header", config.DefaultURLMapHeader, "HTTP header for URL mapping")
}

func initConfig() error {
	var err error
	v, err = config.Init()
	if err != nil {
		return err
	}

	// Override config file if specified
	if cfgFile != "" {
		v.SetConfigFile(cfgFile)
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("error reading config file: %w", err)
		}
	}

	// Bind command-line flags to viper instance (must be done after viper is created)
	// Global flags
	v.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
	v.BindPFlag("spool_dir", rootCmd.PersistentFlags().Lookup("spool-dir"))
	v.BindPFlag("log_file", rootCmd.PersistentFlags().Lookup("log-file"))
	v.BindPFlag("timeout", rootCmd.PersistentFlags().Lookup("timeout"))

	// S3 flags
	v.BindPFlag("s3.endpoint", rootCmd.PersistentFlags().Lookup("s3-endpoint"))
	v.BindPFlag("s3.access_key", rootCmd.PersistentFlags().Lookup("s3-access-key"))
	v.BindPFlag("s3.secret_key", rootCmd.PersistentFlags().Lookup("s3-secret-key"))
	v.BindPFlag("s3.default_bucket", rootCmd.PersistentFlags().Lookup("s3-default-bucket"))
	v.BindPFlag("s3.use_ssl", rootCmd.PersistentFlags().Lookup("s3-use-ssl"))

	// GROBID flags
	v.BindPFlag("grobid.host", rootCmd.PersistentFlags().Lookup("grobid-host"))
	v.BindPFlag("grobid.max_file_size", rootCmd.PersistentFlags().Lookup("grobid-max-filesize"))
	v.BindPFlag("grobid.timeout", rootCmd.PersistentFlags().Lookup("grobid-timeout"))

	// Run flags
	v.BindPFlag("processing.workers", runCmd.Flags().Lookup("workers"))
	v.BindPFlag("processing.keep_spool", runCmd.Flags().Lookup("keep"))

	// Config flags
	v.BindPFlag("config.show_defaults", configCmd.Flags().Lookup("show-defaults"))
	v.BindPFlag("config.show_file", configCmd.Flags().Lookup("show-file"))

	// Serve flags
	v.BindPFlag("server.addr", serveCmd.Flags().Lookup("addr"))
	v.BindPFlag("server.timeout", serveCmd.Flags().Lookup("server-timeout"))
	v.BindPFlag("server.access_log", serveCmd.Flags().Lookup("access-log"))
	v.BindPFlag("server.urlmap_file", serveCmd.Flags().Lookup("urlmap-file"))
	v.BindPFlag("server.urlmap_header", serveCmd.Flags().Lookup("urlmap-header"))

	// Unmarshal config
	if err := v.Unmarshal(&cfg); err != nil {
		return fmt.Errorf("error unmarshaling config: %w", err)
	}

	// Setup logging
	setupLogging()

	return nil
}

func setupLogging() {
	var (
		logLevel = slog.LevelInfo
		h        slog.Handler
		w        io.Writer
	)

	if cfg.Debug {
		logLevel = slog.LevelDebug
	}

	switch {
	case cfg.LogFile != "":
		f, err := os.OpenFile(cfg.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			slog.Error("cannot open log", "err", err)
			os.Exit(1)
		}
		w = f
	default:
		w = os.Stderr
	}

	h = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: logLevel})
	logger := slog.New(h)
	slog.SetDefault(logger)
}

// Command implementations
func runProcessor() error {
	if cfg.Processing.Workers > 1 {
		return runParallelProcessor()
	}
	return runSequentialProcessor()
}

// ensureSpoolDir creates the spool directory if it doesn't exist
func ensureSpoolDir() error {
	if cfg.SpoolDir == "" {
		return fmt.Errorf("spool directory not configured")
	}
	if _, err := os.Stat(cfg.SpoolDir); os.IsNotExist(err) {
		slog.Info("creating spool directory", "path", cfg.SpoolDir)
		if err := os.MkdirAll(cfg.SpoolDir, 0755); err != nil {
			return fmt.Errorf("cannot create spool directory: %w", err)
		}
	}
	return nil
}

// setupServices initializes GROBID and S3 clients; returns nil clients if
// services are unavailable for graceful degradation
func setupServices() (*grobidclient.Grobid, *blobproc.BlobStore) {
	var (
		grobid        *grobidclient.Grobid = grobidclient.New(cfg.Grobid.Host)
		blobStore     *blobproc.BlobStore
		blobStoreOpts = &blobproc.BlobStoreOptions{
			AccessKey:     strings.TrimSpace(cfg.S3.AccessKey),
			SecretKey:     strings.TrimSpace(cfg.S3.SecretKey),
			DefaultBucket: cfg.S3.DefaultBucket,
			UseSSL:        cfg.S3.UseSSL,
		}
		err error
	)
	slog.Info("grobid client", "host", cfg.Grobid.Host)
	blobStore, err = blobproc.NewBlobStore(cfg.S3.Endpoint, blobStoreOpts)
	if err != nil {
		slog.Warn("cannot initialize S3 client, S3 operations will be skipped", "err", err, "endpoint", cfg.S3.Endpoint)
		blobStore = nil
	} else {
		slog.Info("blobstore (s3)", "endpoint", cfg.S3.Endpoint)
	}
	return grobid, blobStore
}

func runSequentialProcessor() error {
	slog.Info("starting sequential processor",
		"spool_dir", cfg.SpoolDir,
		"workers", cfg.Processing.Workers,
		"keep_spool", cfg.Processing.KeepSpool)

	if err := ensureSpoolDir(); err != nil {
		return err
	}
	grobid, blobStore := setupServices()
	started := time.Now()
	var stats struct {
		NumFiles   int
		NumOK      int
		NumSkipped int
	}
	err := filepath.Walk(cfg.SpoolDir, func(path string, info fs.FileInfo, err error) error {
		stats.NumFiles++
		if err != nil {
			return err
		}
		if info.IsDir() {
			stats.NumSkipped++
			return nil
		}
		if info.Size() == 0 {
			stats.NumSkipped++
			slog.Warn("skipping empty file", "path", path)
			return nil
		}
		slog.Debug("processing", "path", path)
		defer func() {
			if !cfg.Processing.KeepSpool {
				if _, err := os.Stat(path); err == nil {
					if err := os.Remove(path); err != nil {
						slog.Warn("error removing file from spool", "err", err, "path", path)
					}
				}
			} else {
				slog.Debug("keeping file in spool", "path", path)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
		defer cancel()
		if err := processSingleFile(ctx, path, info.Size(), grobid, blobStore); err != nil {
			slog.Warn("processing failed", "err", err, "path", path)
			return nil // Continue with other files
		}
		stats.NumOK++
		slog.Debug("processing finished successfully", "path", path)
		return nil
	})
	if err != nil {
		slog.Error("walk failed", "err", err)
		return err
	}
	slog.Info("directory walk done",
		"duration", time.Since(started),
		"duration_str", time.Since(started).String(),
		"total", stats.NumFiles,
		"ok", stats.NumOK,
		"skipped", stats.NumSkipped)

	return nil
}

func runParallelProcessor() error {
	slog.Info("starting parallel processor",
		"spool_dir", cfg.SpoolDir,
		"workers", cfg.Processing.Workers,
		"keep_spool", cfg.Processing.KeepSpool)
	if err := ensureSpoolDir(); err != nil {
		return err
	}
	grobid, blobStore := setupServices()
	walker := blobproc.WalkFast{
		Dir:               cfg.SpoolDir,
		NumWorkers:        cfg.Processing.Workers,
		KeepSpool:         cfg.Processing.KeepSpool,
		GrobidMaxFileSize: cfg.Grobid.MaxFileSize,
		Timeout:           cfg.Timeout,
		Grobid:            grobid,
		S3:                blobStore,
	}
	return walker.Run(context.Background())
}

func processSingleFile(ctx context.Context, path string, size int64, grobid *grobidclient.Grobid, blobStore *blobproc.BlobStore) error {
	result := pdfextract.ProcessFile(ctx, path, &pdfextract.Options{
		Dim:       pdfextract.Dim{180, 300},
		ThumbType: "JPEG",
	})
	switch {
	case result.Status != "success":
		slog.Warn("pdfextract failed", "path", path, "status", result.Status, "err", result.Err)
	case len(result.SHA1Hex) != blobproc.ExpectedSHA1Length:
		slog.Warn("invalid sha1 in response", "sha1", result.SHA1Hex)
	case result.Status == "success":
		if result.HasPage0Thumbnail() {
			switch {
			case blobStore == nil:
				slog.Debug("skipping S3 put (thumbnail), S3 client not available", "sha1", result.SHA1Hex)
			default:
				opts := blobproc.BlobRequestOptions{
					Bucket:  "thumbnail",
					Folder:  "pdf",
					Blob:    result.Page0Thumbnail,
					SHA1Hex: result.SHA1Hex,
					Ext:     "180px.jpg",
					Prefix:  "",
				}
				resp, err := blobStore.PutBlob(ctx, &opts)
				if err != nil {
					slog.Error("s3 failed (thumbnail)", "err", err, "sha1", result.SHA1Hex)
				} else {
					slog.Debug("s3 put ok", "bucket", resp.Bucket, "path", resp.ObjectPath)
				}
			}
		}
		if len(result.Text) > 0 {
			switch {
			case blobStore == nil:
				slog.Debug("skipping S3 put (text), S3 client not available", "sha1", result.SHA1Hex)
			default:
				opts := blobproc.BlobRequestOptions{
					Bucket:  "sandcrawler",
					Folder:  "text",
					Blob:    []byte(result.Text),
					SHA1Hex: result.SHA1Hex,
					Ext:     "txt",
					Prefix:  "",
				}
				resp, err := blobStore.PutBlob(ctx, &opts)
				if err != nil {
					slog.Error("s3 failed (text)", "err", err, "sha1", result.SHA1Hex)
				} else {
					slog.Debug("s3 put ok", "bucket", resp.Bucket, "path", resp.ObjectPath)
				}
			}
		}
	}
	if grobid == nil {
		slog.Debug("skipping GROBID processing, GROBID client not available", "path", path)
		return nil
	}
	if size > cfg.Grobid.MaxFileSize {
		slog.Warn("skipping too large file for GROBID", "path", path, "size", size)
		return nil
	}
	gres, err := grobid.ProcessPDFContext(ctx, path, "processFulltextDocument", &grobidclient.Options{
		GenerateIDs:            true,
		ConsolidateHeader:      true,
		ConsolidateCitations:   false,
		IncludeRawCitations:    true,
		IncludeRawAffiliations: true,
		TEICoordinates:         []string{"ref", "figure", "persName", "formula", "biblStruct"},
		SegmentSentences:       true,
	})
	switch {
	case err != nil || gres.Err != nil:
		slog.Warn("grobid failed", "err", err)
	default:
		switch {
		case blobStore == nil:
			slog.Debug("skipping S3 put (grobid), S3 client not available", "sha1", gres.SHA1Hex)
		default:
			opts := blobproc.BlobRequestOptions{
				Bucket:  "sandcrawler",
				Folder:  "grobid",
				Blob:    gres.Body,
				SHA1Hex: gres.SHA1Hex,
				Ext:     "tei.xml",
				Prefix:  "",
			}
			resp, err := blobStore.PutBlob(ctx, &opts)
			if err != nil {
				slog.Error("s3 failed (grobid)", "err", err)
				return err
			}
			slog.Debug("s3 put ok", "bucket", resp.Bucket, "path", resp.ObjectPath)
		}
	}
	return nil
}

func runSingleFile(filename string) error {
	slog.Info("processing single file", "file", filename)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	result := pdfextract.ProcessFile(ctx, filename, &pdfextract.Options{
		Dim:       pdfextract.Dim{180, 300},
		ThumbType: "JPEG",
	})

	if result.Err != nil {
		return fmt.Errorf("processing failed: %w", result.Err)
	}
	if result.Status != "success" {
		return fmt.Errorf("process failed with status: %v", result.Status)
	}

	return json.NewEncoder(os.Stdout).Encode(result)
}

func showConfig() error {
	fmt.Printf("BLOBPROC Configuration:\n")
	if v.ConfigFileUsed() != "" {
		fmt.Printf("Config File: %s\n", v.ConfigFileUsed())
	} else {
		fmt.Printf("Config File: none (using defaults/env vars/flags)\n")
	}
	fmt.Println()

	if v.GetBool("config.show_file") && v.ConfigFileUsed() != "" {
		fmt.Printf("Config file location: %s\n", v.ConfigFileUsed())
		fmt.Println()
	}

	fmt.Printf("Effective Configuration:\n")
	fmt.Printf("  Debug: %t\n", cfg.Debug)
	fmt.Printf("  Spool Dir: %s\n", cfg.SpoolDir)
	fmt.Printf("  Log File: %s\n", cfg.LogFile)
	fmt.Printf("  Timeout: %v\n", cfg.Timeout)
	fmt.Println()

	fmt.Printf("S3:\n")
	fmt.Printf("  Endpoint: %s\n", cfg.S3.Endpoint)
	fmt.Printf("  Access Key: %s\n", maskSensitive(cfg.S3.AccessKey))
	fmt.Printf("  Secret Key: %s\n", maskSensitive(cfg.S3.SecretKey))
	fmt.Printf("  Default Bucket: %s\n", cfg.S3.DefaultBucket)
	fmt.Printf("  Use SSL: %t\n", cfg.S3.UseSSL)
	fmt.Println()

	fmt.Printf("GROBID:\n")
	fmt.Printf("  Host: %s\n", cfg.Grobid.Host)
	fmt.Printf("  Max File Size: %d bytes\n", cfg.Grobid.MaxFileSize)
	fmt.Printf("  Timeout: %v\n", cfg.Grobid.Timeout)
	fmt.Println()

	fmt.Printf("Processing:\n")
	fmt.Printf("  Workers: %d\n", cfg.Processing.Workers)
	fmt.Printf("  Keep Spool: %t\n", cfg.Processing.KeepSpool)

	return nil
}

func maskSensitive(value string) string {
	if len(value) <= 4 {
		return "****"
	}
	return value[:2] + "****" + value[len(value)-2:]
}

func runServer() error {
	slog.Info("starting HTTP server",
		"addr", cfg.Server.Addr,
		"spool_dir", cfg.SpoolDir,
		"timeout", cfg.Server.Timeout)

	if err := ensureSpoolDir(); err != nil {
		return err
	}

	// Setup access log writer
	var accessLogWriter io.Writer
	switch {
	case cfg.Server.AccessLog != "":
		f, err := os.OpenFile(cfg.Server.AccessLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("cannot open access log: %w", err)
		}
		defer f.Close()
		accessLogWriter = f
	default:
		accessLogWriter = io.Discard
	}

	// Setup web spool service
	svc := &blobproc.WebSpoolService{
		Dir:              cfg.SpoolDir,
		ListenAddr:       cfg.Server.Addr,
		URLMapHttpHeader: cfg.Server.URLMapHeader,
	}

	// Setup URL map if configured
	if cfg.Server.URLMapFile != "" {
		urlMap := blobproc.URLMap{Path: cfg.Server.URLMapFile}
		if err := urlMap.EnsureDB(); err != nil {
			return fmt.Errorf("cannot initialize URL map: %w", err)
		}
		defer urlMap.Close()
		svc.URLMap = &urlMap
	}

	// Setup router
	r := mux.NewRouter()
	banner := `{"id": "blobproc serve", "about": "Send your PDF payload to %s/spool - a 200 OK status only confirms receipt, not successful postprocessing, which may take more time. Check Location header for spool id."}`
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, err := fmt.Fprintf(w, banner+"\n", cfg.Server.Addr)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	r.HandleFunc("/spool", svc.BlobHandler).Methods("POST", "PUT")
	r.HandleFunc("/spool", svc.SpoolListHandler).Methods("GET")
	r.HandleFunc("/spool/{id}", svc.SpoolStatusHandler).Methods("GET")

	loggedRouter := handlers.LoggingHandler(accessLogWriter, r)
	srv := &http.Server{
		Handler:      loggedRouter,
		Addr:         cfg.Server.Addr,
		WriteTimeout: cfg.Server.Timeout,
		ReadTimeout:  cfg.Server.Timeout,
	}

	// Create context that cancels on SIGINT/SIGTERM
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start server in goroutine
	go func() {
		slog.Info("server ready", "addr", srv.Addr, "spool", cfg.SpoolDir)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server listen error", "err", err)
		}
	}()

	// Wait for interrupt signal
	<-ctx.Done()
	slog.Info("shutdown signal received, stopping server")

	// Give outstanding requests 30 seconds to complete
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown failed: %w", err)
	}

	slog.Info("server stopped gracefully")
	return nil
}
