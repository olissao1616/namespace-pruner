package policy

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	MaxImageAgeDays int           `yaml:"maxImageAgeDays"`
	Rules           []Rule        `yaml:"rules"`
	Whitelist       Whitelist     `yaml:"whitelist"`
	JIRA            JIRAConfig    `yaml:"jira"`
	Cleanup         CleanupConfig `yaml:"cleanup"`
}

type Rule struct {
	Type      string `yaml:"type"`
	Threshold int    `yaml:"threshold"`
}

type Whitelist struct {
	Namespaces []string `yaml:"namespaces"`
	Images     []string `yaml:"images"`
}

type JIRAConfig struct {
	Project   string `yaml:"project"`
	IssueType string `yaml:"issuetype"`
	SLADays   int    `yaml:"slaDays"`
}

type CleanupConfig struct {
	DryRun             bool `yaml:"dryRun"`
	MaxDeletionsPerRun int  `yaml:"maxDeletionsPerRun"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	return &cfg, yaml.Unmarshal(data, &cfg)
}

func (c *Config) IsNamespaceWhitelisted(ns string) bool {
	for _, n := range c.Whitelist.Namespaces {
		if n == ns {
			return true
		}
	}
	return false
}

func (c *Config) IsImageWhitelisted(image string) bool {
	for _, img := range c.Whitelist.Images {
		if img == image {
			return true
		}
	}
	return false
}
