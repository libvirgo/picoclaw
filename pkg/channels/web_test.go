package channels

import (
	"context"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestSessionIDFromChatID(t *testing.T) {
	cases := []struct {
		name   string
		chatID string
		want   string
	}{
		{name: "empty", chatID: "", want: ""},
		{name: "plain session", chatID: "session-1", want: "session-1"},
		{name: "legacy session request format", chatID: "session-1:req-2", want: "session-1"},
		{name: "broadcast marker", chatID: "*", want: "*"},
		{name: "whitespace", chatID: "  session-2  ", want: "session-2"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionIDFromChatID(tc.chatID)
			if got != tc.want {
				t.Fatalf("sessionIDFromChatID(%q) = %q, want %q", tc.chatID, got, tc.want)
			}
		})
	}
}

func TestWebChannelSend_AppendsHistoryWithoutPendingStream(t *testing.T) {
	w := &WebChannel{}
	err := w.Send(context.Background(), bus.OutboundMessage{
		Channel: "web",
		ChatID:  "session-1:req-2",
		Content: "Reminder: time is up",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	history := w.getHistory("session-1")
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1", len(history))
	}
	if history[0].Role != "assistant" {
		t.Fatalf("history[0].Role = %q, want assistant", history[0].Role)
	}
	if history[0].Content != "Reminder: time is up" {
		t.Fatalf("history[0].Content = %q, want reminder message", history[0].Content)
	}
}

func TestWebChannelSend_DeliversToPendingStreamAndAppendsHistory(t *testing.T) {
	w := &WebChannel{}
	responseCh := make(chan string, 1)
	w.pending.Store("session-2", responseCh)
	defer w.pending.Delete("session-2")

	err := w.Send(context.Background(), bus.OutboundMessage{
		Channel: "web",
		ChatID:  "session-2",
		Content: "hello",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	select {
	case got := <-responseCh:
		if got != "hello" {
			t.Fatalf("delivered content = %q, want hello", got)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("did not receive pending stream delivery")
	}

	history := w.getHistory("session-2")
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1", len(history))
	}
	if history[0].Content != "hello" {
		t.Fatalf("history content = %q, want hello", history[0].Content)
	}
}

func TestWebChannelSend_KnownErrorsDoNotAppendHistory(t *testing.T) {
	t.Parallel()

	w := &WebChannel{}
	responseCh := make(chan string, 1)
	w.pending.Store("session-error", responseCh)
	defer w.pending.Delete("session-error")

	content := `Error processing message: API request failed:
  Status: 402
  Body:   {"error":"quota_exhausted","message":"credits exhausted"}`
	if err := w.Send(context.Background(), bus.OutboundMessage{
		Channel: "web",
		ChatID:  "session-error",
		Content: content,
	}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	select {
	case got := <-responseCh:
		if got != content {
			t.Fatalf("delivered content = %q, want original error content", got)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("did not receive pending error delivery")
	}

	history := w.getHistory("session-error")
	if len(history) != 0 {
		t.Fatalf("history len = %d, want 0", len(history))
	}
}

func TestClassifyWebChannelError(t *testing.T) {
	t.Parallel()

	code, ok := classifyWebChannelError(`Error processing message: API request failed:
  Status: 403
  Body:   {"error":{"code":"pre_consume_token_quota_failed"}}`)
	if !ok {
		t.Fatal("expected error to be classified")
	}
	if code != OutboundErrorQuotaExhausted {
		t.Fatalf("unexpected code: %q", code)
	}
}

func TestClassifyWebChannelErrorMarker(t *testing.T) {
	t.Parallel()

	code, ok := classifyWebChannelError(OutboundErrorMarkerPrefix + OutboundErrorServiceUnavailable)
	if !ok {
		t.Fatal("expected marker to be classified")
	}
	if code != OutboundErrorServiceUnavailable {
		t.Fatalf("unexpected code: %q", code)
	}
}

func TestWebChannelSend_BroadcastsToKnownSessions(t *testing.T) {
	w := &WebChannel{}
	w.appendHistory("session-a", chatMessage{Role: "user", Content: "hi"})
	w.appendHistory("session-b", chatMessage{Role: "user", Content: "yo"})

	chA := make(chan string, 1)
	chB := make(chan string, 1)
	w.pending.Store("session-a", chA)
	w.pending.Store("session-b", chB)
	defer w.pending.Delete("session-a")
	defer w.pending.Delete("session-b")

	err := w.Send(context.Background(), bus.OutboundMessage{
		Channel: "web",
		ChatID:  "*",
		Content: "timer done",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	select {
	case got := <-chA:
		if got != "timer done" {
			t.Fatalf("session-a delivered content = %q, want timer done", got)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("session-a did not receive broadcast")
	}

	select {
	case got := <-chB:
		if got != "timer done" {
			t.Fatalf("session-b delivered content = %q, want timer done", got)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("session-b did not receive broadcast")
	}

	historyA := w.getHistory("session-a")
	if historyA[len(historyA)-1].Content != "timer done" {
		t.Fatalf("session-a history tail = %q, want timer done", historyA[len(historyA)-1].Content)
	}
	historyB := w.getHistory("session-b")
	if historyB[len(historyB)-1].Content != "timer done" {
		t.Fatalf("session-b history tail = %q, want timer done", historyB[len(historyB)-1].Content)
	}
}

func TestWebChannelBroadcastAfterEnsureSession(t *testing.T) {
	w := &WebChannel{}
	w.ensureSession("session-c")

	err := w.Send(context.Background(), bus.OutboundMessage{
		Channel: "web",
		ChatID:  "*",
		Content: "timer done",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	history := w.getHistory("session-c")
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1", len(history))
	}
	if history[0].Role != "assistant" || history[0].Content != "timer done" {
		t.Fatalf("unexpected history[0]=%+v", history[0])
	}
}
