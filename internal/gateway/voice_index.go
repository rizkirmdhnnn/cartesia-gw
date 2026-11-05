package gateway

import (
	"context"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/provider"
)

// voiceNameResult caches the first catalogue page's name→id map.
type voiceNameResult struct {
	ids   map[string]bool
	names map[string]string
}

// probeKeys reports how many keys a provider has at all, without picking one
// or touching last_used. The speech path uses it to fail fast with a setup
// message instead of a 503 from the rotation loop.
func (s *Server) probeKeys(ctx context.Context, providerName string) (int, error) {
	keys, err := s.store.KeysFor(ctx, providerName)
	if err != nil {
		return 0, err
	}
	return len(keys), nil
}

// previewResolver is how the preview handler resolves a voice's file URL. It
// is a field so a test can answer without a network hop while the catalogue
// path keeps its own host guard.
type previewResolver interface {
	lookupPreviewURL(ctx context.Context, p provider.Provider, voiceID string) (string, error)
}

// previewURLFor resolves through the test override when one is set.
func (s *Server) previewURLFor(ctx context.Context, p provider.Provider, voiceID string) (string, error) {
	if s.previewResolverOverride != nil {
		return s.previewResolverOverride.lookupPreviewURL(ctx, p, voiceID)
	}
	return s.lookupPreviewURL(ctx, p, voiceID)
}
