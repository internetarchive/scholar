package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrg/xdg"
	"github.com/spf13/viper"
)

// Default values - single source of truth
var (
	DefaultSpoolDir        = filepath.Join(xdg.DataHome, "blobproc", "spool")
	DefaultTimeout         = 5 * time.Minute
	DefaultGrobidHost      = "http://localhost:8070"
	DefaultGrobidMaxSize   = int64(256 * 1024 * 1024) // 256MB
	DefaultGrobidTimeout   = 30 * time.Second
	DefaultS3Endpoint      = "localhost:9000"
	DefaultS3AccessKey     = "minioadmin"
	DefaultS3SecretKey     = "minioadmin"
	DefaultS3Bucket        = "sandcrawler"
	DefaultS3UseSSL        = false
	DefaultWorkers         = 4
	DefaultKeepSpool       = false
	DefaultDebug           = false
	DefaultServerAddr      = "0.0.0.0:8000"
	DefaultServerTimeout   = 15 * time.Second
	DefaultAccessLog       = ""
	DefaultURLMapFile      = ""
	DefaultURLMapHeader    = "X-Original-URL"
)

type Config struct {
	// Common settings
	Debug    bool          `mapstructure:"debug"`
	LogFile  string        `mapstructure:"log_file"`
	SpoolDir string        `mapstructure:"spool_dir"`
	Timeout  time.Duration `mapstructure:"timeout"`

	// S3 settings
	S3 S3Config `mapstructure:"s3"`

	// GROBID settings
	Grobid GrobidConfig `mapstructure:"grobid"`

	// Processing settings
	Processing ProcessingConfig `mapstructure:"processing"`

	// Server settings
	Server ServerConfig `mapstructure:"server"`
}

type S3Config struct {
	Endpoint      string `mapstructure:"endpoint"`
	AccessKey     string `mapstructure:"access_key"`
	SecretKey     string `mapstructure:"secret_key"`
	DefaultBucket string `mapstructure:"default_bucket"`
	UseSSL        bool   `mapstructure:"use_ssl"`
}

type GrobidConfig struct {
	Host        string        `mapstructure:"host"`
	MaxFileSize int64         `mapstructure:"max_file_size"`
	Timeout     time.Duration `mapstructure:"timeout"`
}

type ProcessingConfig struct {
	Workers   int  `mapstructure:"workers"`
	KeepSpool bool `mapstructure:"keep_spool"`
}

type ServerConfig struct {
	Addr            string        `mapstructure:"addr"`
	Timeout         time.Duration `mapstructure:"timeout"`
	AccessLog       string        `mapstructure:"access_log"`
	URLMapFile      string        `mapstructure:"urlmap_file"`
	URLMapHeader    string        `mapstructure:"urlmap_header"`
}

func Init() (*viper.Viper, error) {
	v := viper.New()

	// Set defaults
	setDefaults(v)

	// Config file search paths (viper auto-detects .yaml/.yml extension)
	v.SetConfigName("blobproc")
	v.AddConfigPath(".")
	v.AddConfigPath("$HOME/.config/blobproc")
	v.AddConfigPath("/etc/blobproc")

	// Environment variable prefix
	v.SetEnvPrefix("BLOBPROC")
	v.AutomaticEnv()
	// Replace dots with underscores in config keys for environment variable mapping
	// This allows s3.endpoint -> BLOBPROC_S3_ENDPOINT, processing.workers -> BLOBPROC_PROCESSING_WORKERS, etc.
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Try to read config file
	if err := v.ReadInConfig(); err != nil {
		// If no config file found, that's perfectly fine - continue silently
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			return v, nil
		}
		// Config file exists but is broken - fail fast and tell user which file
		return nil, fmt.Errorf("error parsing config file %s: %w", v.ConfigFileUsed(), err)
	}

	return v, nil
}

func setDefaults(v *viper.Viper) {
	// Common defaults
	v.SetDefault("debug", DefaultDebug)
	v.SetDefault("spool_dir", DefaultSpoolDir)
	v.SetDefault("timeout", DefaultTimeout)

	// S3 defaults
	v.SetDefault("s3.endpoint", DefaultS3Endpoint)
	v.SetDefault("s3.access_key", DefaultS3AccessKey)
	v.SetDefault("s3.secret_key", DefaultS3SecretKey)
	v.SetDefault("s3.default_bucket", DefaultS3Bucket)
	v.SetDefault("s3.use_ssl", DefaultS3UseSSL)

	// GROBID defaults
	v.SetDefault("grobid.host", DefaultGrobidHost)
	v.SetDefault("grobid.max_file_size", DefaultGrobidMaxSize)
	v.SetDefault("grobid.timeout", DefaultGrobidTimeout)

	// Processing defaults
	v.SetDefault("processing.workers", DefaultWorkers)
	v.SetDefault("processing.keep_spool", DefaultKeepSpool)

	// Server defaults
	v.SetDefault("server.addr", DefaultServerAddr)
	v.SetDefault("server.timeout", DefaultServerTimeout)
	v.SetDefault("server.access_log", DefaultAccessLog)
	v.SetDefault("server.urlmap_file", DefaultURLMapFile)
	v.SetDefault("server.urlmap_header", DefaultURLMapHeader)
}
