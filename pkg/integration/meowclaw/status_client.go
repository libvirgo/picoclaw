package meowclaw

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

type RuntimeStatus struct {
	InstanceID   string              `json:"instance_id"`
	GeneratedAt  string              `json:"generated_at"`
	Quota        RuntimeQuota        `json:"quota"`
	Subscription RuntimeSubscription `json:"subscription"`
}

type RuntimeQuota struct {
	UsedQuota   int `json:"used_quota"`
	RemainQuota int `json:"remain_quota"`
	TotalQuota  int `json:"total_quota"`
}

type RuntimeSubscription struct {
	Active            bool   `json:"active"`
	Status            string `json:"status,omitempty"`
	Plan              string `json:"plan,omitempty"`
	Interval          string `json:"interval,omitempty"`
	CurrentPeriodEnd  string `json:"current_period_end,omitempty"`
	RemainingSeconds  int64  `json:"remaining_seconds,omitempty"`
	RemainingDays     int    `json:"remaining_days,omitempty"`
	CancelAtPeriodEnd bool   `json:"cancel_at_period_end,omitempty"`
	Source            string `json:"source,omitempty"`
}

var statusHTTPClient = &http.Client{Timeout: 10 * time.Second}

func FetchRuntimeStatus(ctx context.Context, cfg *config.Config) (*RuntimeStatus, error) {
	modelCfg, err := resolveProxyModelConfig(cfg)
	if err != nil {
		return nil, err
	}

	token := strings.TrimSpace(modelCfg.APIKey)
	if token == "" {
		return nil, fmt.Errorf("missing proxy token in model config")
	}

	endpoint, err := runtimeStatusEndpoint(modelCfg.APIBase)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build runtime status request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := statusHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request runtime status failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("runtime status request failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var status RuntimeStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, fmt.Errorf("decode runtime status failed: %w", err)
	}
	return &status, nil
}

func resolveProxyModelConfig(cfg *config.Config) (*config.ModelConfig, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}

	modelName := strings.TrimSpace(cfg.Agents.Defaults.GetModelName())
	if modelName != "" {
		if modelCfg, err := cfg.GetModelConfig(modelName); err == nil && modelCfg != nil {
			return modelCfg, nil
		}
	}

	if len(cfg.ModelList) == 0 {
		return nil, fmt.Errorf("model_list is empty")
	}
	return &cfg.ModelList[0], nil
}

func runtimeStatusEndpoint(apiBase string) (string, error) {
	apiBase = strings.TrimSpace(apiBase)
	if apiBase == "" {
		return "", fmt.Errorf("model api_base is empty")
	}
	parsed, err := url.Parse(apiBase)
	if err != nil {
		return "", fmt.Errorf("invalid model api_base: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid model api_base: missing scheme/host")
	}

	parsed.Path = "/internal/v1/runtime/status"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}
