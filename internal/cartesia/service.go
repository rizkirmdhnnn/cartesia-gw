package cartesia

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/provider"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/tts"
)

// Service adapts Client to the provider interface.
type Service struct {
	client *Client
	// timeout bounds a synthesized clip when it has to be buffered for the
	// JSON envelope; streaming responses are not bounded by it.
	timeout time.Duration
}

// NewService wraps a client as the "cartesia" provider.
func NewService(client *Client) *Service { return &Service{client: client} }

// Name implements provider.Provider.
func (s *Service) Name() string { return "cartesia" }

// Label implements provider.Provider.
func (s *Service) Label() string { return "Cartesia" }

// Models implements provider.Provider.
func (s *Service) Models() []string { return Models }

// DefaultModel implements provider.Provider.
func (s *Service) DefaultModel() string { return DefaultModel }

// DefaultVoice implements provider.Provider. Cartesia has no single default
// voice, and sending a voice nobody asked for is worse than naming the
// omission, so the operator configures one.
func (s *Service) DefaultVoice() string { return "" }

// VoicePrefixes implements provider.Provider. Cartesia ids are UUIDs, which
// OwnerOf recognises by shape.
func (s *Service) VoicePrefixes() []string { return nil }

// Synthesize implements provider.Provider.
func (s *Service) Synthesize(ctx context.Context, apiKey string, plan tts.Plan) (*http.Response, error) {
	return s.client.Synthesize(ctx, apiKey, PlanToRequest(plan))
}

// ListVoices implements provider.Provider.
func (s *Service) ListVoices(ctx context.Context, apiKey string, query url.Values) (provider.VoicesPage, error) {
	page, err := s.client.ListVoices(ctx, apiKey, query)
	if err != nil {
		return provider.VoicesPage{}, err
	}
	return page.Page(), nil
}

// FetchPreview implements provider.Provider.
func (s *Service) FetchPreview(ctx context.Context, apiKey, rawURL string) (*http.Response, error) {
	return s.client.FetchPreview(ctx, apiKey, rawURL)
}

// Retryable means "another key could answer". It is the provider's own
// classification, so a 400 about the payload is not retried while a 429 or a
// 401 is.
func (s *Service) Classify(status int, body string) (string, bool) {
	return cleanMessage(body), retryable(status)
}

// retryable reports whether a status means "try this on another key".
func retryable(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden,
		http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return status >= 500
}

// Refs projects a provider page onto the fields this process reads.
func Refs(page provider.VoicesPage) []VoiceRef {
	refs := make([]VoiceRef, 0, len(page.Data))
	for _, voice := range page.Data {
		ref := VoiceRef{}
		if v, ok := voice["id"].(string); ok {
			ref.ID = v
		}
		if v, ok := voice["name"].(string); ok {
			ref.Name = v
		}
		if v, ok := voice["language"].(string); ok {
			ref.Language = v
		}
		if v, ok := voice["preview_file_url"].(string); ok && v != "" {
			ref.PreviewFileURL = &v
		}
		if ref.ID == "" {
			continue
		}
		refs = append(refs, ref)
	}
	return refs
}

// cleanMessage trims an upstream error body to something a client can read.
func cleanMessage(body string) string {
	message := strings.TrimSpace(body)
	if len(message) > 400 {
		message = message[:400]
	}
	return message
}

var _ provider.Provider = (*Service)(nil)

// String is used in log lines that describe which upstream answered.
func (s *Service) String() string { return fmt.Sprintf("cartesia(%s)", s.client.baseURL) }
