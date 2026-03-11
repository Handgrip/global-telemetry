package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// AgentConfig is the local per-node configuration (agent.yaml).
type AgentConfig struct {
	ProbeName             string            `yaml:"probe_name"`
	ConfigURL             string            `yaml:"config_url"`
	ConfigRefreshInterval string            `yaml:"config_refresh_interval"`
	PushInterval          string            `yaml:"push_interval"`
	CacheDir              string            `yaml:"cache_dir"`
	MetricPrefix          string            `yaml:"metric_prefix"`
	GrafanaCloud          GrafanaCloudConfig `yaml:"grafana_cloud"`
}

type GrafanaCloudConfig struct {
	RemoteWriteURL string `yaml:"remote_write_url"`
	Username       string `yaml:"username"`
	APIKey         string `yaml:"api_key"`
}

func (c *AgentConfig) GetConfigRefreshInterval() time.Duration {
	d, err := time.ParseDuration(c.ConfigRefreshInterval)
	if err != nil {
		return 60 * time.Second
	}
	return d
}

func (c *AgentConfig) GetPushInterval() time.Duration {
	d, err := time.ParseDuration(c.PushInterval)
	if err != nil {
		return 60 * time.Second
	}
	return d
}

// TargetsConfig is the remote shared configuration (targets.json).
type TargetsConfig struct {
	Defaults TargetDefaults `json:"defaults"`
	Targets  []Target       `json:"targets"`
}

type TargetDefaults struct {
	IntervalSeconds int `json:"interval_seconds"`
	TimeoutSeconds  int `json:"timeout_seconds"`
}

type Target struct {
	Name            string            `json:"name"`
	Type            string            `json:"type"` // "icmp" or "http"
	Host            string            `json:"host,omitempty"`
	URL             string            `json:"url,omitempty"`
	ICMP            *ICMPConfig       `json:"icmp,omitempty"`
	HTTP            *HTTPConfig       `json:"http,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	IntervalSeconds int               `json:"interval_seconds,omitempty"`
	TimeoutSeconds  int               `json:"timeout_seconds,omitempty"`
}

type ICMPConfig struct {
	Count int `json:"count"`
}

type HTTPConfig struct {
	Method         string `json:"method"`
	ExpectedStatus int    `json:"expected_status"`
	SkipTLSVerify  bool   `json:"skip_tls_verify"`
}

func (t *Target) GetInterval(defaults TargetDefaults) time.Duration {
	s := t.IntervalSeconds
	if s <= 0 {
		s = defaults.IntervalSeconds
	}
	if s <= 0 {
		s = 30
	}
	return time.Duration(s) * time.Second
}

func (t *Target) GetTimeout(defaults TargetDefaults) time.Duration {
	s := t.TimeoutSeconds
	if s <= 0 {
		s = defaults.TimeoutSeconds
	}
	if s <= 0 {
		s = 5
	}
	return time.Duration(s) * time.Second
}

// LoadAgentConfig reads and parses the agent YAML config file.
func LoadAgentConfig(path string) (*AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent config: %w", err)
	}
	var cfg AgentConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse agent config: %w", err)
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = "/var/lib/probe-agent"
	}
	return &cfg, nil
}

// ConfigManager handles fetching, caching, and hot-reloading the targets config.
type ConfigManager struct {
	configURL       string
	refreshInterval time.Duration
	cacheDir        string
	httpClient      *http.Client

	mu       sync.RWMutex
	targets  *TargetsConfig
	lastRaw  []byte
	changed  chan struct{}
}

func NewConfigManager(agentCfg *AgentConfig) *ConfigManager {
	return &ConfigManager{
		configURL:       agentCfg.ConfigURL,
		refreshInterval: agentCfg.GetConfigRefreshInterval(),
		cacheDir:        agentCfg.CacheDir,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		changed: make(chan struct{}, 1),
	}
}

// Changed returns a channel that receives a value when the targets config is refreshed.
func (cm *ConfigManager) Changed() <-chan struct{} {
	return cm.changed
}

func (cm *ConfigManager) notifyChanged() {
	select {
	case cm.changed <- struct{}{}:
	default:
	}
}

func (cm *ConfigManager) cachePath() string {
	return filepath.Join(cm.cacheDir, "targets.cache.json")
}

// InitialLoad fetches targets from the remote URL; falls back to local cache.
func (cm *ConfigManager) InitialLoad(ctx context.Context) error {
	if _, remoteErr := cm.fetchAndCache(ctx); remoteErr != nil {
		slog.Warn("remote config fetch failed, trying cache", "error", remoteErr)
		if cacheErr := cm.loadFromCache(); cacheErr != nil {
			return fmt.Errorf("no config available (remote failed: %v, cache failed: %v)", remoteErr, cacheErr)
		}
		slog.Info("loaded targets from cache")
		return nil
	}
	slog.Info("loaded targets from remote", "url", cm.configURL)
	return nil
}

// fetchAndCache fetches the targets config and returns whether the content changed.
func (cm *ConfigManager) fetchAndCache(ctx context.Context) (changed bool, err error) {
	var body []byte

	if strings.HasPrefix(cm.configURL, "file://") {
		body, err = os.ReadFile(strings.TrimPrefix(cm.configURL, "file://"))
		if err != nil {
			return false, fmt.Errorf("read local config: %w", err)
		}
	} else if !strings.Contains(cm.configURL, "://") {
		body, err = os.ReadFile(cm.configURL)
		if err != nil {
			return false, fmt.Errorf("read local config: %w", err)
		}
	} else {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cm.configURL, nil)
		if err != nil {
			return false, fmt.Errorf("build request: %w", err)
		}
		resp, err := cm.httpClient.Do(req)
		if err != nil {
			return false, fmt.Errorf("fetch config: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false, fmt.Errorf("unexpected status %d from config URL", resp.StatusCode)
		}
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			return false, fmt.Errorf("read response body: %w", err)
		}
	}

	var targets TargetsConfig
	if err := json.Unmarshal(body, &targets); err != nil {
		return false, fmt.Errorf("parse targets config: %w", err)
	}

	cm.mu.Lock()
	changed = !bytes.Equal(cm.lastRaw, body)
	cm.targets = &targets
	cm.lastRaw = body
	cm.mu.Unlock()

	if changed {
		if err := os.MkdirAll(cm.cacheDir, 0755); err == nil {
			_ = os.WriteFile(cm.cachePath(), body, 0644)
		}
	}

	return changed, nil
}

func (cm *ConfigManager) loadFromCache() error {
	data, err := os.ReadFile(cm.cachePath())
	if err != nil {
		return fmt.Errorf("read cache: %w", err)
	}
	var targets TargetsConfig
	if err := json.Unmarshal(data, &targets); err != nil {
		return fmt.Errorf("parse cache: %w", err)
	}
	cm.mu.Lock()
	cm.targets = &targets
	cm.mu.Unlock()
	return nil
}

// GetTargets returns a copy of the current targets config (thread-safe).
func (cm *ConfigManager) GetTargets() *TargetsConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.targets == nil {
		return nil
	}
	// Return a shallow copy to prevent callers from modifying the internal state
	configCopy := &TargetsConfig{
		Defaults: cm.targets.Defaults,
		Targets:  make([]Target, len(cm.targets.Targets)),
	}
	copy(configCopy.Targets, cm.targets.Targets)
	return configCopy
}

// StartRefreshLoop periodically re-fetches the targets config.
func (cm *ConfigManager) StartRefreshLoop(ctx context.Context) {
	ticker := time.NewTicker(cm.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			changed, err := cm.fetchAndCache(ctx)
			if err != nil {
				slog.Warn("config refresh failed, keeping current config", "error", err)
			} else if changed {
				slog.Info("targets config changed, notifying scheduler")
				cm.notifyChanged()
			} else {
				slog.Debug("config refreshed, no changes")
			}
		}
	}
}
