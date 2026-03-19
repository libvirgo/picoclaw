package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const webChannelPort = 18791
const webBroadcastChatID = "*"

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// sessionHistory holds per-session chat history with its own mutex.
type sessionHistory struct {
	mu       sync.Mutex
	messages []chatMessage
}

type WebChannel struct {
	*BaseChannel
	config  config.WebConfig
	pending sync.Map // sessionID -> chan string
	history sync.Map // sessionID -> *sessionHistory
	server  *http.Server
}

func NewWebChannel(cfg config.WebConfig, messageBus *bus.MessageBus) (*WebChannel, error) {
	base := NewBaseChannel("web", cfg, messageBus, cfg.AllowFrom)
	return &WebChannel{
		BaseChannel: base,
		config:      cfg,
	}, nil
}

func (w *WebChannel) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat", w.handleChat)
	mux.HandleFunc("/api/chat/history", w.handleHistory)

	w.server = &http.Server{
		Addr:         fmt.Sprintf("0.0.0.0:%d", webChannelPort),
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 150 * time.Second,
	}

	go func() {
		logger.InfoCF("web", "Web channel HTTP server starting", map[string]any{
			"port": webChannelPort,
		})
		if err := w.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.ErrorCF("web", "Web channel HTTP server error", map[string]any{
				"error": err.Error(),
			})
		}
	}()

	w.SetRunning(true)
	return nil
}

func (w *WebChannel) Stop(ctx context.Context) error {
	if w.server != nil {
		_ = w.server.Shutdown(ctx)
	}
	w.SetRunning(false)
	return nil
}

func (w *WebChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	sessionID := sessionIDFromChatID(msg.ChatID)
	if sessionID == "" {
		return nil
	}
	if sessionID == webBroadcastChatID {
		w.broadcastToKnownSessions(msg.Content)
		return nil
	}

	w.deliverToPendingSession(sessionID, msg.Content)
	if _, ok := classifyWebChannelError(msg.Content); ok {
		return nil
	}

	if strings.TrimSpace(msg.Content) != "" {
		w.appendHistory(sessionID, chatMessage{Role: "assistant", Content: msg.Content})
	}
	return nil
}

func (w *WebChannel) deliverToPendingSession(sessionID, content string) {
	if ch, ok := w.pending.Load(sessionID); ok {
		select {
		case ch.(chan string) <- content:
		case <-time.After(5 * time.Second):
			logger.WarnCF("web", "Timeout sending to response channel", map[string]any{
				"session_id": sessionID,
			})
		}
	}
}

func (w *WebChannel) broadcastToKnownSessions(content string) {
	sessions := make(map[string]struct{})

	w.history.Range(func(key, _ any) bool {
		if sid, ok := key.(string); ok && strings.TrimSpace(sid) != "" {
			sessions[sid] = struct{}{}
		}
		return true
	})
	w.pending.Range(func(key, _ any) bool {
		if sid, ok := key.(string); ok && strings.TrimSpace(sid) != "" {
			sessions[sid] = struct{}{}
		}
		return true
	})

	for sid := range sessions {
		w.deliverToPendingSession(sid, content)
		if strings.TrimSpace(content) != "" {
			w.appendHistory(sid, chatMessage{Role: "assistant", Content: content})
		}
	}
}

func (w *WebChannel) handleChat(wr http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		setCORSHeaders(wr)
		wr.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(wr, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check flusher support before writing any headers.
	flusher, ok := wr.(http.Flusher)
	if !ok {
		http.Error(wr, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Limit request body size.
	r.Body = http.MaxBytesReader(wr, r.Body, 1<<20)

	var body struct {
		Message   string `json:"message"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(wr, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Message == "" || body.SessionID == "" {
		http.Error(wr, "message and session_id are required", http.StatusBadRequest)
		return
	}

	requestID := uuid.New().String()
	chatID := body.SessionID

	responseCh := make(chan string, 10)
	w.pending.Store(chatID, responseCh)
	defer w.pending.Delete(chatID)

	// Save user message to history.
	w.appendHistory(body.SessionID, chatMessage{Role: "user", Content: body.Message})

	// Set SSE headers before publishing to avoid race with fast responses.
	setCORSHeaders(wr)
	wr.Header().Set("Content-Type", "text/event-stream")
	wr.Header().Set("Cache-Control", "no-cache")
	wr.Header().Set("Connection", "keep-alive")
	wr.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Publish inbound message to the bus.
	w.BaseChannel.HandleMessage(r.Context(),
		bus.Peer{Kind: "direct", ID: body.SessionID},
		requestID, body.SessionID, chatID, body.Message,
		nil, nil,
	)

	timeout := time.After(120 * time.Second)

	// Wait for the agent's response.
	select {
	case content := <-responseCh:
		if code, ok := classifyWebChannelError(content); ok {
			data, _ := json.Marshal(map[string]string{"error": code})
			fmt.Fprintf(wr, "event: error\ndata: %s\n\n", data)
			flusher.Flush()
			return
		}

		data, _ := json.Marshal(map[string]string{"content": content})
		fmt.Fprintf(wr, "data: %s\n\n", data)
		flusher.Flush()

		// Drain additional messages with idle + absolute timeout.
		drainDeadline := time.After(30 * time.Second)
		drainIdle := time.After(2 * time.Second)
	drain:
		for {
			select {
			case extra := <-responseCh:
				data, _ := json.Marshal(map[string]string{"content": extra})
				fmt.Fprintf(wr, "data: %s\n\n", data)
				flusher.Flush()
				drainIdle = time.After(2 * time.Second)
			case <-drainIdle:
				break drain
			case <-drainDeadline:
				break drain
			case <-r.Context().Done():
				return
			}
		}

	case <-timeout:
		data, _ := json.Marshal(map[string]string{"error": "timeout"})
		fmt.Fprintf(wr, "event: error\ndata: %s\n\n", data)
		flusher.Flush()
		return

	case <-r.Context().Done():
		return
	}

	fmt.Fprintf(wr, "event: done\ndata: {}\n\n")
	flusher.Flush()
}

func classifyWebChannelError(content string) (string, bool) {
	return ClassifyUserFacingOutboundError(content)
}

func (w *WebChannel) handleHistory(wr http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		setCORSHeaders(wr)
		wr.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(wr, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(wr, "session_id is required", http.StatusBadRequest)
		return
	}

	w.ensureSession(sessionID)
	messages := w.getHistory(sessionID)

	setCORSHeaders(wr)
	wr.Header().Set("Content-Type", "application/json")
	json.NewEncoder(wr).Encode(map[string]any{
		"messages": messages,
	})
}

func (w *WebChannel) ensureSession(sessionID string) {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return
	}
	w.history.LoadOrStore(sid, &sessionHistory{})
}

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func sessionIDFromChatID(chatID string) string {
	trimmed := strings.TrimSpace(chatID)
	if trimmed == "" {
		return ""
	}
	if idx := strings.Index(trimmed, ":"); idx > 0 {
		return trimmed[:idx]
	}
	return trimmed
}

func (w *WebChannel) appendHistory(sessionID string, msg chatMessage) {
	val, _ := w.history.LoadOrStore(sessionID, &sessionHistory{})
	sh := val.(*sessionHistory)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.messages = append(sh.messages, msg)
}

func (w *WebChannel) getHistory(sessionID string) []chatMessage {
	val, ok := w.history.Load(sessionID)
	if !ok {
		return []chatMessage{}
	}
	sh := val.(*sessionHistory)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	out := make([]chatMessage, len(sh.messages))
	copy(out, sh.messages)
	return out
}
