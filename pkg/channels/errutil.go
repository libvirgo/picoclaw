package channels

import (
	"fmt"
	"net/http"
	"strings"
)

const (
	OutboundErrorMarkerPrefix      = "__MEOWCLAW_ERROR__:"
	OutboundErrorQuotaExhausted    = "quota_exhausted"
	OutboundErrorSessionExpired    = "session_expired"
	OutboundErrorResponseTimedOut  = "response_timed_out"
	OutboundErrorServiceUnavailable = "service_unavailable"
)

// ClassifySendError wraps a raw error with the appropriate sentinel based on
// an HTTP status code. Channels that perform HTTP API calls should use this
// in their Send path.
func ClassifySendError(statusCode int, rawErr error) error {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %v", ErrRateLimit, rawErr)
	case statusCode >= 500:
		return fmt.Errorf("%w: %v", ErrTemporary, rawErr)
	case statusCode >= 400:
		return fmt.Errorf("%w: %v", ErrSendFailed, rawErr)
	default:
		return rawErr
	}
}

// ClassifyNetError wraps a network/timeout error as ErrTemporary.
func ClassifyNetError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrTemporary, err)
}

func ClassifyUserFacingOutboundError(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", false
	}

	if strings.HasPrefix(trimmed, OutboundErrorMarkerPrefix) {
		code := strings.TrimSpace(strings.TrimPrefix(trimmed, OutboundErrorMarkerPrefix))
		return code, code != ""
	}

	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "error processing message:") {
		return "", false
	}

	switch {
	case strings.Contains(lower, OutboundErrorQuotaExhausted) ||
		strings.Contains(lower, "pre_consume_token_quota_failed") ||
		strings.Contains(lower, "token quota is not enough"):
		return OutboundErrorQuotaExhausted, true
	case strings.Contains(lower, OutboundErrorSessionExpired) ||
		strings.Contains(lower, "unauthorized"):
		return OutboundErrorSessionExpired, true
	case strings.Contains(lower, "timeout"):
		return OutboundErrorResponseTimedOut, true
	default:
		return OutboundErrorServiceUnavailable, true
	}
}

func TransformUserFacingOutboundContent(channel, content string) string {
	code, ok := ClassifyUserFacingOutboundError(content)
	if !ok {
		return content
	}
	if strings.EqualFold(strings.TrimSpace(channel), "web") {
		return OutboundErrorMarkerPrefix + code
	}

	switch code {
	case OutboundErrorQuotaExhausted:
		return "喵～当前可用额度已用尽，请充值后继续使用。"
	case OutboundErrorSessionExpired:
		return "喵～我刚刚没有顺利同步这次会话，请稍后再试。"
	case OutboundErrorResponseTimedOut:
		return "喵～我这次回复超时了，请稍后再试。"
	default:
		return "喵～我刚刚没有顺利处理这条消息，请稍后再试。"
	}
}
