package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	integration "github.com/sipeed/picoclaw/pkg/integration/meowclaw"
)

type MeowClawRuntimeStatusTool struct {
	config *config.Config
}

func NewMeowClawRuntimeStatusTool(cfg *config.Config) *MeowClawRuntimeStatusTool {
	return &MeowClawRuntimeStatusTool{config: cfg}
}

func (t *MeowClawRuntimeStatusTool) Name() string {
	return "meowclaw_runtime_status"
}

func (t *MeowClawRuntimeStatusTool) Description() string {
	return "Query MeowClaw runtime status (non-sensitive), including token quota and subscription remaining time."
}

func (t *MeowClawRuntimeStatusTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (t *MeowClawRuntimeStatusTool) Execute(ctx context.Context, _ map[string]any) *ToolResult {
	status, err := integration.FetchRuntimeStatus(ctx, t.config)
	if err != nil {
		return ErrorResult("failed to fetch meowclaw runtime status: " + err.Error()).WithError(err)
	}
	return SilentResult(formatRuntimeStatus(status))
}

func formatRuntimeStatus(status *integration.RuntimeStatus) string {
	if status == nil {
		return "runtime status unavailable"
	}

	lines := []string{
		"Runtime Status",
		fmt.Sprintf("- Instance ID: %s", valueOr(status.InstanceID, "unknown")),
		fmt.Sprintf(
			"- Quota: remain=%d, total=%d, used=%d",
			status.Quota.RemainQuota,
			status.Quota.TotalQuota,
			status.Quota.UsedQuota,
		),
	}

	sub := status.Subscription
	if sub.Active {
		lines = append(lines, fmt.Sprintf(
			"- Subscription: active, plan=%s, interval=%s, status=%s",
			valueOr(sub.Plan, "unknown"),
			valueOr(sub.Interval, "unknown"),
			valueOr(sub.Status, "active"),
		))
		if strings.TrimSpace(sub.CurrentPeriodEnd) != "" {
			lines = append(lines, fmt.Sprintf(
				"- Subscription End: %s (remaining_days=%d, remaining_seconds=%d)",
				sub.CurrentPeriodEnd,
				sub.RemainingDays,
				sub.RemainingSeconds,
			))
		}
		if sub.CancelAtPeriodEnd {
			lines = append(lines, "- Subscription cancel_at_period_end=true")
		}
	} else {
		lines = append(lines, "- Subscription: not active")
		if strings.TrimSpace(sub.Status) != "" {
			lines = append(lines, fmt.Sprintf("- Last Subscription Status: %s", sub.Status))
		}
		if strings.TrimSpace(sub.CurrentPeriodEnd) != "" {
			lines = append(lines, fmt.Sprintf(
				"- Last Subscription End: %s",
				sub.CurrentPeriodEnd,
			))
		}
	}

	if strings.TrimSpace(status.GeneratedAt) != "" {
		lines = append(lines, fmt.Sprintf("- Generated At: %s", status.GeneratedAt))
	}

	return strings.Join(lines, "\n")
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
