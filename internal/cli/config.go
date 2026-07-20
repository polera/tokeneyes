package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Model           string           `yaml:"model"`
	Models          []string         `yaml:"models"`
	OutputTokens    []int64          `yaml:"output_tokens"`
	ReasoningTokens int64            `yaml:"reasoning_tokens"`
	CachedTokens    int64            `yaml:"cached_tokens"`
	MaxFileBytes    int64            `yaml:"max_file_bytes"`
	MaxTotalBytes   int64            `yaml:"max_total_bytes"`
	MaxMediaBytes   int64            `yaml:"max_media_size"`
	MaxMediaCount   int              `yaml:"max_media_count"`
	MaxPages        int              `yaml:"max_pages"`
	MaxDuration     string           `yaml:"max_duration"`
	Processing      string           `yaml:"processing"`
	ImageDetail     string           `yaml:"image_detail"`
	DocumentDetail  string           `yaml:"document_detail"`
	Transcripts     []string         `yaml:"transcripts"`
	Overrides       []ConfigOverride `yaml:"overrides"`
	Workers         int              `yaml:"workers"`
	Profile         string           `yaml:"profile"`
	Catalog         string           `yaml:"catalog"`
	Database        string           `yaml:"database"`
	NoSave          bool             `yaml:"no_save"`
	FailIncomplete  bool             `yaml:"fail_incomplete"`
	FailOverflow    bool             `yaml:"fail_overflow"`
	MaxInputTokens  int64            `yaml:"max_input_tokens"`
	MaxCostUSD      string           `yaml:"max_cost_usd"`
	EstimateBound   string           `yaml:"estimate_bound"`
	IncludeHidden   bool             `yaml:"include_hidden"`
}

type ConfigOverride struct {
	Glob           string `yaml:"glob"`
	Processing     string `yaml:"processing"`
	ImageDetail    string `yaml:"image_detail"`
	DocumentDetail string `yaml:"document_detail"`
}

func loadConfig(path string, explicit bool) (Config, error) {
	if path == "" {
		path = ".tokeneyes.yaml"
	}
	// #nosec G304 -- path is the caller-supplied config file, defaulting to .tokeneyes.yaml.
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) && !explicit {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(b))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

func defaultDBPath() (string, error) {
	if runtime.GOOS != "windows" {
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			return filepath.Join(xdg, "tokeneyes", "runs.db"), nil
		}
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "tokeneyes", "runs.db"), nil
}
