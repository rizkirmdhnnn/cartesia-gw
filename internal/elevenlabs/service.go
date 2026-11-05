package elevenlabs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/provider"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/tts"
)

// Service adapts Client to the provider interface.
type Service struct{ client *Client }

// NewService wraps a client as the "elevenlabs" provider.
func NewService(client *Client) *Service { return &Service{client: client} }

// Name implements provider.Provider.
func (s *Service) Name() string { return "elevenlabs" }

// Label implements provider.Provider.
func (s *Service) Label() string { return "ElevenLabs" }

// Models implements provider.Provider.
func (s *Service) Models() []string { return Models }

// DefaultModel implements provider.Provider.
func (s *Service) DefaultModel() string { return DefaultModel }

// DefaultVoice implements provider.Provider.
func (s *Service) DefaultVoice() string { return DefaultVoice }

// VoicePrefixes implements provider.Provider. ElevenLabs voice ids are opaque
// 20-character strings, so they are recognised by shape rather than prefix.
func (s *Service) VoicePrefixes() []string { return nil }

// Synthesize implements provider.Provider.
func (s *Service) Synthesize(ctx context.Context, apiKey string, plan tts.Plan) (*http.Response, error) {
	return s.client.Synthesize(ctx, apiKey, plan)
}

// ListVoices implements provider.Provider.
func (s *Service) ListVoices(ctx context.Context, apiKey string, query url.Values) (provider.VoicesPage, error) {
	return s.client.ListVoices(ctx, apiKey, query)
}

// FetchPreview implements provider.Provider.
func (s *Service) FetchPreview(ctx context.Context, apiKey, rawURL string) (*http.Response, error) {
	return s.client.FetchPreview(ctx, apiKey, rawURL)
}

// Classify implements provider.Provider. ElevenLabs answers 401 for a rejected
// key and for an exhausted credit balance, 429 for the concurrency limit, and
// 422 for a body it cannot accept. Only the first two are worth another key;
// a 422 would fail the same way everywhere.
func (s *Service) Classify(status int, body string) (string, bool) {
	message := cleanMessage(body)
	retryable := status == http.StatusUnauthorized ||
		status == http.StatusPaymentRequired ||
		status == http.StatusForbidden ||
		status == http.StatusRequestTimeout ||
		status == http.StatusTooManyRequests ||
		status >= 500
	return message, retryable
}

// cleanMessage flattens ElevenLabs' {"detail":[...]} error shape into one
// readable line, so history and the client see the reason, not the envelope.
func cleanMessage(body string) string {
	raw := strings.TrimSpace(body)
	var envelope struct {
		Detail json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err == nil && len(envelope.Detail) > 0 {
		var list []struct {
			Msg  string `json:"msg"`
			Type string `json:"type"`
		}
		if err := json.Unmarshal(envelope.Detail, &list); err == nil && len(list) > 0 {
			parts := make([]string, 0, len(list))
			for _, item := range list {
				switch {
				case item.Msg != "":
					parts = append(parts, item.Msg)
				case item.Type != "":
					parts = append(parts, item.Type)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, "; ")
			}
		}
		var plain string
		if err := json.Unmarshal(envelope.Detail, &plain); err == nil && plain != "" {
			return plain
		}
	}
	if len(raw) > 400 {
		raw = raw[:400]
	}
	if raw == "" {
		return ""
	}
	return raw
}

var _ provider.Provider = (*Service)(nil)

// String is used in log lines that describe which upstream answered.
func (s *Service) String() string { return fmt.Sprintf("elevenlabs(%s)", s.client.baseURL) }
