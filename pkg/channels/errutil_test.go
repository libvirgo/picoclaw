package channels

import (
	"errors"
	"fmt"
	"testing"
)

func TestClassifySendError(t *testing.T) {
	raw := fmt.Errorf("some API error")

	tests := []struct {
		name       string
		statusCode int
		wantIs     error
		wantNil    bool
	}{
		{"429 -> ErrRateLimit", 429, ErrRateLimit, false},
		{"500 -> ErrTemporary", 500, ErrTemporary, false},
		{"502 -> ErrTemporary", 502, ErrTemporary, false},
		{"503 -> ErrTemporary", 503, ErrTemporary, false},
		{"400 -> ErrSendFailed", 400, ErrSendFailed, false},
		{"403 -> ErrSendFailed", 403, ErrSendFailed, false},
		{"404 -> ErrSendFailed", 404, ErrSendFailed, false},
		{"200 -> raw error", 200, nil, false},
		{"201 -> raw error", 201, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ClassifySendError(tt.statusCode, raw)
			if err == nil {
				t.Fatal("expected non-nil error")
			}
			if tt.wantIs != nil {
				if !errors.Is(err, tt.wantIs) {
					t.Errorf("errors.Is(err, %v) = false, want true; err = %v", tt.wantIs, err)
				}
			} else {
				// Should return the raw error unchanged
				if err != raw {
					t.Errorf("expected raw error to be returned unchanged for status %d, got %v", tt.statusCode, err)
				}
			}
		})
	}
}

func TestClassifySendErrorNoFalsePositive(t *testing.T) {
	raw := fmt.Errorf("some error")

	// 429 should NOT match ErrTemporary or ErrSendFailed
	err := ClassifySendError(429, raw)
	if errors.Is(err, ErrTemporary) {
		t.Error("429 should not match ErrTemporary")
	}
	if errors.Is(err, ErrSendFailed) {
		t.Error("429 should not match ErrSendFailed")
	}

	// 500 should NOT match ErrRateLimit or ErrSendFailed
	err = ClassifySendError(500, raw)
	if errors.Is(err, ErrRateLimit) {
		t.Error("500 should not match ErrRateLimit")
	}
	if errors.Is(err, ErrSendFailed) {
		t.Error("500 should not match ErrSendFailed")
	}

	// 400 should NOT match ErrRateLimit or ErrTemporary
	err = ClassifySendError(400, raw)
	if errors.Is(err, ErrRateLimit) {
		t.Error("400 should not match ErrRateLimit")
	}
	if errors.Is(err, ErrTemporary) {
		t.Error("400 should not match ErrTemporary")
	}
}

func TestClassifyNetError(t *testing.T) {
	t.Run("nil error returns nil", func(t *testing.T) {
		if err := ClassifyNetError(nil); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("non-nil error wraps as ErrTemporary", func(t *testing.T) {
		raw := fmt.Errorf("connection refused")
		err := ClassifyNetError(raw)
		if err == nil {
			t.Fatal("expected non-nil error")
		}
		if !errors.Is(err, ErrTemporary) {
			t.Errorf("errors.Is(err, ErrTemporary) = false, want true; err = %v", err)
		}
	})
}

func TestClassifyUserFacingOutboundError(t *testing.T) {
	t.Run("classifies quota exhaustion from raw runtime error", func(t *testing.T) {
		code, ok := ClassifyUserFacingOutboundError(`Error processing message: API request failed:
  Status: 402
  Body:   {"error":"quota_exhausted","message":"credits exhausted"}`)
		if !ok {
			t.Fatal("expected error classification")
		}
		if code != OutboundErrorQuotaExhausted {
			t.Fatalf("unexpected code: %q", code)
		}
	})

	t.Run("classifies web error marker", func(t *testing.T) {
		code, ok := ClassifyUserFacingOutboundError(OutboundErrorMarkerPrefix + OutboundErrorServiceUnavailable)
		if !ok {
			t.Fatal("expected marker classification")
		}
		if code != OutboundErrorServiceUnavailable {
			t.Fatalf("unexpected code: %q", code)
		}
	})
}

func TestTransformUserFacingOutboundContent(t *testing.T) {
	rawQuota := `Error processing message: API request failed:
  Status: 402
  Body:   {"error":"quota_exhausted","message":"credits exhausted"}`

	if got := TransformUserFacingOutboundContent("telegram", rawQuota); got != "喵～当前可用额度已用尽，请充值后继续使用。" {
		t.Fatalf("unexpected telegram content: %q", got)
	}

	if got := TransformUserFacingOutboundContent("web", rawQuota); got != OutboundErrorMarkerPrefix+OutboundErrorQuotaExhausted {
		t.Fatalf("unexpected web content: %q", got)
	}

	rawGeneric := "Error processing message: upstream request failed"
	if got := TransformUserFacingOutboundContent("slack", rawGeneric); got != "喵～我刚刚没有顺利处理这条消息，请稍后再试。" {
		t.Fatalf("unexpected slack content: %q", got)
	}

	rawUnauthorized := "Error processing message: unauthorized"
	if got := TransformUserFacingOutboundContent("telegram", rawUnauthorized); got != "喵～我刚刚没有顺利同步这次会话，请稍后再试。" {
		t.Fatalf("unexpected telegram unauthorized content: %q", got)
	}
}
