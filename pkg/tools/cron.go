package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// JobExecutor is the interface for executing cron jobs through the agent
type JobExecutor interface {
	ProcessDirectWithChannel(ctx context.Context, content, sessionKey, channel, chatID string) (string, error)
}

// CronTool provides scheduling capabilities for the agent
type CronTool struct {
	cronService *cron.CronService
	executor    JobExecutor
	msgBus      *bus.MessageBus
	execTool    *ExecTool
	config      *config.Config
	channel     string
	chatID      string
	mu          sync.RWMutex
}

var supportedCronChannels = map[string]struct{}{
	"web":             {},
	"telegram":        {},
	"discord":         {},
	"slack":           {},
	"feishu":          {},
	"line":            {},
	"onebot":          {},
	"qq":              {},
	"dingtalk":        {},
	"wecom":           {},
	"wecom_app":       {},
	"whatsapp":        {},
	"whatsapp_native": {},
	"maixcam":         {},
	"pico":            {},
}

const cronWebBroadcastTarget = "*"

// NewCronTool creates a new CronTool
// execTimeout: 0 means no timeout, >0 sets the timeout duration
func NewCronTool(
	cronService *cron.CronService, executor JobExecutor, msgBus *bus.MessageBus, workspace string, restrict bool,
	execTimeout time.Duration, config *config.Config,
) (*CronTool, error) {
	execTool, err := NewExecToolWithConfig(workspace, restrict, config)
	if err != nil {
		return nil, fmt.Errorf("unable to configure exec tool: %w", err)
	}

	execTool.SetTimeout(execTimeout)
	return &CronTool{
		cronService: cronService,
		executor:    executor,
		msgBus:      msgBus,
		execTool:    execTool,
		config:      config,
	}, nil
}

// Name returns the tool name
func (t *CronTool) Name() string {
	return "cron"
}

// Description returns the tool description
func (t *CronTool) Description() string {
	return "Schedule reminders, tasks, or system commands. IMPORTANT: When user asks to be reminded or scheduled, you MUST call this tool. Use 'at_seconds' for one-time reminders (e.g., 'remind me in 10 minutes' -> at_seconds=600). Use 'every_seconds' ONLY for recurring tasks (e.g., 'every 2 hours' -> every_seconds=7200). Use 'cron_expr' for complex recurring schedules. Use 'command' to execute shell commands directly. By default notifications are delivered to all configured channels; optional target_channel/target_chat_id can force a specific destination."
}

// Parameters returns the tool parameters schema
func (t *CronTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"add", "list", "remove", "enable", "disable"},
				"description": "Action to perform. Use 'add' when user wants to schedule a reminder or task.",
			},
			"message": map[string]any{
				"type":        "string",
				"description": "The reminder/task message to display when triggered. If 'command' is used, this describes what the command does.",
			},
			"command": map[string]any{
				"type":        "string",
				"description": "Optional: Shell command to execute directly (e.g., 'df -h'). If set, the agent will run this command and report output instead of just showing the message. 'deliver' will be forced to false for commands.",
			},
			"at_seconds": map[string]any{
				"type":        "integer",
				"description": "One-time reminder: seconds from now when to trigger (e.g., 600 for 10 minutes later). Use this for one-time reminders like 'remind me in 10 minutes'.",
			},
			"every_seconds": map[string]any{
				"type":        "integer",
				"description": "Recurring interval in seconds (e.g., 3600 for every hour). Use this ONLY for recurring tasks like 'every 2 hours' or 'daily reminder'.",
			},
			"cron_expr": map[string]any{
				"type":        "string",
				"description": "Cron expression for complex recurring schedules (e.g., '0 9 * * *' for daily at 9am). Use this for complex recurring schedules.",
			},
			"job_id": map[string]any{
				"type":        "string",
				"description": "Job ID (for remove/enable/disable)",
			},
			"deliver": map[string]any{
				"type":        "boolean",
				"description": "If true, send message directly to channel. If false, let agent process message (for complex tasks). Default: true",
			},
			"target_channel": map[string]any{
				"type":        "string",
				"description": "Optional target channel for notification delivery (e.g. telegram, discord, slack, feishu, line, web). When set, it overrides broadcast defaults.",
			},
			"target_chat_id": map[string]any{
				"type":        "string",
				"description": "Optional target chat/user id for the target channel. Required when target_channel differs from current channel.",
			},
			"broadcast_all_channels": map[string]any{
				"type":        "boolean",
				"description": "When true (default), reminders notify all configured channels with resolvable recipients. Ignored when target_channel/target_chat_id is provided.",
			},
		},
		"required": []string{"action"},
	}
}

// SetContext sets the current session context for job creation
func (t *CronTool) SetContext(channel, chatID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.channel = channel
	t.chatID = chatID
}

// Execute runs the tool with the given arguments
func (t *CronTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	action, ok := args["action"].(string)
	if !ok {
		return ErrorResult("action is required")
	}

	switch action {
	case "add":
		return t.addJob(args)
	case "list":
		return t.listJobs()
	case "remove":
		return t.removeJob(args)
	case "enable":
		return t.enableJob(args, true)
	case "disable":
		return t.enableJob(args, false)
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

func (t *CronTool) addJob(args map[string]any) *ToolResult {
	t.mu.RLock()
	channel := t.channel
	chatID := t.chatID
	t.mu.RUnlock()

	message, ok := args["message"].(string)
	if !ok || message == "" {
		return ErrorResult("message is required for add")
	}

	var schedule cron.CronSchedule

	// Check for at_seconds (one-time), every_seconds (recurring), or cron_expr
	atSeconds, hasAt := args["at_seconds"].(float64)
	everySeconds, hasEvery := args["every_seconds"].(float64)
	cronExpr, hasCron := args["cron_expr"].(string)

	// Priority: at_seconds > every_seconds > cron_expr
	if hasAt {
		atMS := time.Now().UnixMilli() + int64(atSeconds)*1000
		schedule = cron.CronSchedule{
			Kind: "at",
			AtMS: &atMS,
		}
	} else if hasEvery {
		everyMS := int64(everySeconds) * 1000
		schedule = cron.CronSchedule{
			Kind:    "every",
			EveryMS: &everyMS,
		}
	} else if hasCron {
		schedule = cron.CronSchedule{
			Kind: "cron",
			Expr: cronExpr,
		}
	} else {
		return ErrorResult("one of at_seconds, every_seconds, or cron_expr is required")
	}

	// Read deliver parameter, default to true
	deliver := true
	if d, ok := args["deliver"].(bool); ok {
		deliver = d
	}

	command, _ := args["command"].(string)
	if command != "" {
		// Commands must be processed by agent/exec tool, so deliver must be false (or handled specifically)
		// Actually, let's keep deliver=false to let the system know it's not a simple chat message
		// But for our new logic in ExecuteJob, we can handle it regardless of deliver flag if Payload.Command is set.
		// However, logically, it's not "delivered" to chat directly as is.
		deliver = false
	}

	rawTargetChannel, _ := args["target_channel"].(string)
	rawTargetChannel = strings.TrimSpace(rawTargetChannel)
	rawTargetChatID, _ := args["target_chat_id"].(string)
	rawTargetChatID = strings.TrimSpace(rawTargetChatID)
	explicitTarget := rawTargetChannel != "" || rawTargetChatID != ""

	broadcastAll := true
	if v, ok := args["broadcast_all_channels"].(bool); ok {
		broadcastAll = v
	}

	targetChannel := channel
	targetChatID := chatID
	var targets []cron.CronTarget

	if explicitTarget {
		if rawTargetChannel != "" {
			normalized, err := normalizeCronTargetChannel(rawTargetChannel)
			if err != nil {
				return ErrorResult(err.Error())
			}
			targetChannel = normalized
		}
		if rawTargetChatID != "" {
			targetChatID = rawTargetChatID
		}
		if targetChannel == "" || targetChatID == "" {
			return ErrorResult("target channel/chat_id not resolved. Use this tool in an active conversation or provide target_channel + target_chat_id.")
		}
		if targetChannel != channel && rawTargetChatID == "" {
			return ErrorResult("target_chat_id is required when target_channel differs from current channel")
		}
		targets = []cron.CronTarget{{Channel: targetChannel, To: targetChatID}}
	} else {
		targets = t.resolveNotificationTargets(channel, chatID, broadcastAll)
		if len(targets) == 0 {
			return ErrorResult("no valid notification targets resolved. Configure channel allow_from or provide target_channel + target_chat_id.")
		}
		targetChannel = targets[0].Channel
		targetChatID = targets[0].To
	}

	// Truncate message for job name (max 30 chars)
	messagePreview := utils.Truncate(message, 30)

	job, err := t.cronService.AddJob(
		messagePreview,
		schedule,
		message,
		deliver,
		targetChannel,
		targetChatID,
		targets,
	)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Error adding job: %v", err))
	}

	if command != "" {
		job.Payload.Command = command
		// Need to save the updated payload
		t.cronService.UpdateJob(job)
	}

	return SilentResult(fmt.Sprintf("Cron job added: %s (id: %s)", job.Name, job.ID))
}

func normalizeCronTargetChannel(channel string) (string, error) {
	v := strings.TrimSpace(strings.ToLower(channel))
	switch v {
	case "lark":
		v = "feishu"
	}
	if _, ok := supportedCronChannels[v]; ok {
		return v, nil
	}
	return "", fmt.Errorf("unsupported target_channel: %s", channel)
}

func (t *CronTool) resolveNotificationTargets(sourceChannel, sourceChatID string, broadcastAll bool) []cron.CronTarget {
	targets := make([]cron.CronTarget, 0, 8)
	seen := make(map[string]struct{}, 8)

	appendTarget := func(channel, chatID string) {
		channel = strings.TrimSpace(strings.ToLower(channel))
		chatID = strings.TrimSpace(chatID)
		if channel == "" || chatID == "" {
			return
		}
		key := channel + "|" + chatID
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		targets = append(targets, cron.CronTarget{Channel: channel, To: chatID})
	}

	// Always include the source conversation first when available.
	appendTarget(sourceChannel, sourceChatID)

	if !broadcastAll || t.config == nil {
		return targets
	}

	ch := t.config.Channels
	appendConfigTargets := func(enabled bool, channel string, ids config.FlexibleStringSlice) {
		if !enabled {
			return
		}
		for _, id := range ids {
			appendTarget(channel, id)
		}
	}

	if ch.Web.Enabled {
		if len(ch.Web.AllowFrom) == 0 {
			appendTarget("web", cronWebBroadcastTarget)
		} else {
			appendConfigTargets(true, "web", ch.Web.AllowFrom)
		}
	}
	appendConfigTargets(ch.Telegram.Enabled && strings.TrimSpace(ch.Telegram.Token) != "", "telegram", ch.Telegram.AllowFrom)
	appendConfigTargets(ch.Feishu.Enabled, "feishu", ch.Feishu.AllowFrom)
	appendConfigTargets(ch.Discord.Enabled && strings.TrimSpace(ch.Discord.Token) != "", "discord", ch.Discord.AllowFrom)
	appendConfigTargets(ch.Slack.Enabled && strings.TrimSpace(ch.Slack.BotToken) != "", "slack", ch.Slack.AllowFrom)
	appendConfigTargets(ch.LINE.Enabled && strings.TrimSpace(ch.LINE.ChannelAccessToken) != "", "line", ch.LINE.AllowFrom)
	appendConfigTargets(ch.OneBot.Enabled && strings.TrimSpace(ch.OneBot.WSUrl) != "", "onebot", ch.OneBot.AllowFrom)
	appendConfigTargets(ch.QQ.Enabled, "qq", ch.QQ.AllowFrom)
	appendConfigTargets(ch.DingTalk.Enabled && strings.TrimSpace(ch.DingTalk.ClientID) != "", "dingtalk", ch.DingTalk.AllowFrom)
	appendConfigTargets(ch.WeCom.Enabled && strings.TrimSpace(ch.WeCom.Token) != "", "wecom", ch.WeCom.AllowFrom)
	appendConfigTargets(ch.WeComApp.Enabled && strings.TrimSpace(ch.WeComApp.CorpID) != "", "wecom_app", ch.WeComApp.AllowFrom)
	appendConfigTargets(ch.MaixCam.Enabled, "maixcam", ch.MaixCam.AllowFrom)
	appendConfigTargets(ch.Pico.Enabled && strings.TrimSpace(ch.Pico.Token) != "", "pico", ch.Pico.AllowFrom)
	if ch.WhatsApp.Enabled {
		if ch.WhatsApp.UseNative {
			appendConfigTargets(true, "whatsapp_native", ch.WhatsApp.AllowFrom)
		} else if strings.TrimSpace(ch.WhatsApp.BridgeURL) != "" {
			appendConfigTargets(true, "whatsapp", ch.WhatsApp.AllowFrom)
		}
	}

	return targets
}

func (t *CronTool) listJobs() *ToolResult {
	jobs := t.cronService.ListJobs(false)

	if len(jobs) == 0 {
		return SilentResult("No scheduled jobs")
	}

	result := "Scheduled jobs:\n"
	for _, j := range jobs {
		var scheduleInfo string
		if j.Schedule.Kind == "every" && j.Schedule.EveryMS != nil {
			scheduleInfo = fmt.Sprintf("every %ds", *j.Schedule.EveryMS/1000)
		} else if j.Schedule.Kind == "cron" {
			scheduleInfo = j.Schedule.Expr
		} else if j.Schedule.Kind == "at" {
			scheduleInfo = "one-time"
		} else {
			scheduleInfo = "unknown"
		}
		result += fmt.Sprintf("- %s (id: %s, %s)\n", j.Name, j.ID, scheduleInfo)
	}

	return SilentResult(result)
}

func (t *CronTool) removeJob(args map[string]any) *ToolResult {
	jobID, ok := args["job_id"].(string)
	if !ok || jobID == "" {
		return ErrorResult("job_id is required for remove")
	}

	if t.cronService.RemoveJob(jobID) {
		return SilentResult(fmt.Sprintf("Cron job removed: %s", jobID))
	}
	return ErrorResult(fmt.Sprintf("Job %s not found", jobID))
}

func (t *CronTool) enableJob(args map[string]any, enable bool) *ToolResult {
	jobID, ok := args["job_id"].(string)
	if !ok || jobID == "" {
		return ErrorResult("job_id is required for enable/disable")
	}

	job := t.cronService.EnableJob(jobID, enable)
	if job == nil {
		return ErrorResult(fmt.Sprintf("Job %s not found", jobID))
	}

	status := "enabled"
	if !enable {
		status = "disabled"
	}
	return SilentResult(fmt.Sprintf("Cron job '%s' %s", job.Name, status))
}

// ExecuteJob executes a cron job through the agent
func (t *CronTool) ExecuteJob(ctx context.Context, job *cron.CronJob) string {
	targets := collectCronTargets(job.Payload)
	if len(targets) == 0 {
		targets = []cron.CronTarget{{Channel: "cli", To: "direct"}}
	}
	primaryTarget := targets[0]

	// Execute command if present
	if job.Payload.Command != "" {
		args := map[string]any{
			"command": job.Payload.Command,
		}

		result := t.execTool.Execute(ctx, args)
		var output string
		if result.IsError {
			output = fmt.Sprintf("Error executing scheduled command: %s", result.ForLLM)
		} else {
			output = fmt.Sprintf("Scheduled command '%s' executed:\n%s", job.Payload.Command, result.ForLLM)
		}

		t.publishToTargets(targets, output)
		return "ok"
	}

	// If deliver=true, send message directly without agent processing
	if job.Payload.Deliver {
		t.publishToTargets(targets, job.Payload.Message)
		return "ok"
	}

	// For deliver=false, process through agent (for complex tasks)
	sessionKey := fmt.Sprintf("cron-%s", job.ID)

	// Call agent with job's message
	response, err := t.executor.ProcessDirectWithChannel(
		ctx,
		job.Payload.Message,
		sessionKey,
		primaryTarget.Channel,
		primaryTarget.To,
	)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}

	// Response is automatically sent by AgentLoop to primary target.
	// For additional targets, mirror the same output to keep behavior consistent.
	if response != "" && len(targets) > 1 {
		t.publishToTargets(targets[1:], response)
	}
	return "ok"
}

func collectCronTargets(payload cron.CronPayload) []cron.CronTarget {
	targets := make([]cron.CronTarget, 0, len(payload.Targets)+1)
	seen := make(map[string]struct{}, len(payload.Targets)+1)
	appendTarget := func(channel, to string) {
		channel = strings.TrimSpace(strings.ToLower(channel))
		to = strings.TrimSpace(to)
		if channel == "" || to == "" {
			return
		}
		key := channel + "|" + to
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		targets = append(targets, cron.CronTarget{Channel: channel, To: to})
	}

	for _, target := range payload.Targets {
		appendTarget(target.Channel, target.To)
	}
	appendTarget(payload.Channel, payload.To)
	return targets
}

func (t *CronTool) publishToTargets(targets []cron.CronTarget, content string) {
	if t.msgBus == nil || strings.TrimSpace(content) == "" {
		return
	}
	for _, target := range targets {
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		t.msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
			Channel: target.Channel,
			ChatID:  target.To,
			Content: content,
		})
		pubCancel()
	}
}
