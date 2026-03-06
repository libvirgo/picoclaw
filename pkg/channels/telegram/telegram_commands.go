package telegram

import (
	"context"
	"fmt"
	"strings"

	"github.com/mymmrac/telego"

	"github.com/sipeed/picoclaw/pkg/config"
	integration "github.com/sipeed/picoclaw/pkg/integration/meowclaw"
)

type TelegramCommander interface {
	Help(ctx context.Context, message telego.Message) error
	Start(ctx context.Context, message telego.Message) error
	Show(ctx context.Context, message telego.Message) error
	List(ctx context.Context, message telego.Message) error
	Status(ctx context.Context, message telego.Message) error
}

type cmd struct {
	bot    *telego.Bot
	config *config.Config
}

func NewTelegramCommands(bot *telego.Bot, cfg *config.Config) TelegramCommander {
	return &cmd{
		bot:    bot,
		config: cfg,
	}
}

func commandArgs(text string) string {
	parts := strings.SplitN(text, " ", 2)
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func (c *cmd) Help(ctx context.Context, message telego.Message) error {
	msg := `/start - Start the bot
/help - Show this help message
/status - Show runtime quota and subscription status
/show [model|channel] - Show current configuration
/list [models|channels] - List available options
	`
	_, err := c.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: message.Chat.ID},
		Text:   msg,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: message.MessageID,
		},
	})
	return err
}

func (c *cmd) Start(ctx context.Context, message telego.Message) error {
	_, err := c.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: message.Chat.ID},
		Text:   "Hello! I am MeowClaw 🐾",
		ReplyParameters: &telego.ReplyParameters{
			MessageID: message.MessageID,
		},
	})
	return err
}

func (c *cmd) Show(ctx context.Context, message telego.Message) error {
	args := commandArgs(message.Text)
	if args == "" {
		_, err := c.bot.SendMessage(ctx, &telego.SendMessageParams{
			ChatID: telego.ChatID{ID: message.Chat.ID},
			Text:   "Usage: /show [model|channel]",
			ReplyParameters: &telego.ReplyParameters{
				MessageID: message.MessageID,
			},
		})
		return err
	}

	var response string
	switch args {
	case "model":
		response = fmt.Sprintf("Current Model: %s (Provider: %s)",
			c.config.Agents.Defaults.GetModelName(),
			c.config.Agents.Defaults.Provider)
	case "channel":
		response = "Current Channel: telegram"
	default:
		response = fmt.Sprintf("Unknown parameter: %s. Try 'model' or 'channel'.", args)
	}

	_, err := c.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: message.Chat.ID},
		Text:   response,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: message.MessageID,
		},
	})
	return err
}

func (c *cmd) List(ctx context.Context, message telego.Message) error {
	args := commandArgs(message.Text)
	if args == "" {
		_, err := c.bot.SendMessage(ctx, &telego.SendMessageParams{
			ChatID: telego.ChatID{ID: message.Chat.ID},
			Text:   "Usage: /list [models|channels]",
			ReplyParameters: &telego.ReplyParameters{
				MessageID: message.MessageID,
			},
		})
		return err
	}

	var response string
	switch args {
	case "models":
		provider := c.config.Agents.Defaults.Provider
		if provider == "" {
			provider = "configured default"
		}
		response = fmt.Sprintf("Configured Model: %s\nProvider: %s\n\nTo change models, update config.json",
			c.config.Agents.Defaults.GetModelName(), provider)

	case "channels":
		var enabled []string
		if c.config.Channels.Telegram.Enabled {
			enabled = append(enabled, "telegram")
		}
		if c.config.Channels.WhatsApp.Enabled {
			enabled = append(enabled, "whatsapp")
		}
		if c.config.Channels.Feishu.Enabled {
			enabled = append(enabled, "feishu")
		}
		if c.config.Channels.Discord.Enabled {
			enabled = append(enabled, "discord")
		}
		if c.config.Channels.Slack.Enabled {
			enabled = append(enabled, "slack")
		}
		response = fmt.Sprintf("Enabled Channels:\n- %s", strings.Join(enabled, "\n- "))

	default:
		response = fmt.Sprintf("Unknown parameter: %s. Try 'models' or 'channels'.", args)
	}

	_, err := c.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: message.Chat.ID},
		Text:   response,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: message.MessageID,
		},
	})
	return err
}

func (c *cmd) Status(ctx context.Context, message telego.Message) error {
	status, err := integration.FetchRuntimeStatus(ctx, c.config)
	if err != nil {
		_, sendErr := c.bot.SendMessage(ctx, &telego.SendMessageParams{
			ChatID: telego.ChatID{ID: message.Chat.ID},
			Text:   "Failed to load runtime status. Please try again later.",
			ReplyParameters: &telego.ReplyParameters{
				MessageID: message.MessageID,
			},
		})
		if sendErr != nil {
			return sendErr
		}
		return nil
	}

	subLine := "Subscription: not active"
	if status.Subscription.Active {
		subLine = fmt.Sprintf(
			"Subscription: active (%s/%s, status=%s)",
			fallbackString(status.Subscription.Plan, "unknown"),
			fallbackString(status.Subscription.Interval, "unknown"),
			fallbackString(status.Subscription.Status, "active"),
		)
	}

	msg := fmt.Sprintf(
		"Runtime Status\nQuota: %d remaining / %d total (used %d)\n%s",
		status.Quota.RemainQuota,
		status.Quota.TotalQuota,
		status.Quota.UsedQuota,
		subLine,
	)

	if status.Subscription.Active && status.Subscription.CurrentPeriodEnd != "" {
		msg += fmt.Sprintf(
			"\nSubscription end: %s (remaining %d days)",
			status.Subscription.CurrentPeriodEnd,
			status.Subscription.RemainingDays,
		)
	}

	_, sendErr := c.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: message.Chat.ID},
		Text:   msg,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: message.MessageID,
		},
	})
	return sendErr
}

func fallbackString(v, fallback string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback
	}
	return v
}
