package gateway

import (
	"net/http"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/provider"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

// handleRouting answers GET /api/routing: everything the traffic visualiser
// draws, in one response. It is deliberately one snapshot — clients, keys,
// shares and "next up" — so the two sides of the graph always describe the
// same traffic window and the same picker state.
//
// Keys are grouped by provider, and each provider's share is computed against
// its own providers' traffic: a Cartesia key never shows a share of
// ElevenLabs' requests, because rotation is per provider.
func (s *Server) handleRouting(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since := time.Now().Add(-24 * time.Hour).Unix()

	strategy, err := s.store.RotationStrategy(ctx)
	if err != nil {
		s.logError("routing: strategy", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	keys, err := s.store.OrderedKeys(ctx, strategy)
	if err != nil {
		s.logError("routing: list keys", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	clients, err := s.store.ListClientKeys(ctx)
	if err != nil {
		s.logError("routing: list clients", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	usage, err := s.store.Usage(ctx)
	if err != nil {
		s.logError("routing: usage", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	byKey, byClient, err := s.store.TrafficSince(ctx, since)
	if err != nil {
		s.logError("routing: traffic", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	gate, err := s.store.Gate(ctx)
	if err != nil {
		s.logError("routing: gate", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}

	// Totals first, so each side's share adds up to 1 per provider.
	keyTotal := map[string]int64{}
	for _, key := range keys {
		keyTotal[key.Provider] += byKey[key.ID].Requests
	}
	var clientTotal int64
	for _, traffic := range byClient {
		clientTotal += traffic.Requests
	}

	now := time.Now()
	nextUp := map[string]int64{}
	for _, p := range s.providers.Providers() {
		id, err := s.store.PickPreviewFor(ctx, p.Name())
		if err != nil {
			s.logError("routing: pick preview", err)
			continue
		}
		nextUp[p.Name()] = id
	}

	keyNodes := make([]store.KeyView, 0, len(keys))
	for _, key := range keys {
		traffic := byKey[key.ID]
		share := 0.0
		if keyTotal[key.Provider] > 0 {
			share = float64(traffic.Requests) / float64(keyTotal[key.Provider])
		}
		keyNodes = append(keyNodes, store.KeyView{
			ID:            key.ID,
			Label:         key.Label,
			Provider:      key.Provider,
			MaskedSecret:  maskSecret(key.Secret),
			IsActive:      key.IsActive,
			Priority:      key.Priority,
			Strikes:       key.Strikes,
			CooldownUntil: key.CooldownUntil,
			Cooling:       key.Cooling(now),
			LastError:     key.LastError,
			LastUsedAt:    key.LastUsedAt,
			CreatedAt:     key.CreatedAt,
			Requests24h:   traffic.Requests,
			Share:         share,
			Eligible:      key.Eligible(now),
			NextUp:        nextUp[key.Provider] != 0 && key.ID == nextUp[key.Provider],
			Success24h:    traffic.Success,
			Chars24h:      traffic.Chars,
			Latency24h:    traffic.AvgLatency,
		})
	}

	type clientNode struct {
		clientKeyView
		Share      float64 `json:"share"`
		Chars24h   int64   `json:"chars24h"`
		LastSeenTS int64   `json:"lastSeenTs"`
	}
	clientNodes := make([]clientNode, 0, len(clients))
	for _, key := range clients {
		traffic := byClient[key.ID]
		share := 0.0
		if clientTotal > 0 {
			share = float64(traffic.Requests) / float64(clientTotal)
		}
		clientNodes = append(clientNodes, clientNode{
			clientKeyView: toClientKeyView(key, now),
			Share:         share,
			Chars24h:      traffic.Chars,
			LastSeenTS:    traffic.LastSeenTS,
		})
	}

	// Per-provider request counts, so the console can say who answered.
	providerRows := make([]map[string]any, 0, len(s.providers.Providers()))
	for _, p := range s.providers.Providers() {
		stats := usage.Providers[p.Name()]
		total, active := 0, 0
		for _, key := range keys {
			if key.Provider != p.Name() {
				continue
			}
			total++
			if key.IsActive {
				active++
			}
		}
		providerRows = append(providerRows, map[string]any{
			"name":        p.Name(),
			"label":       p.Label(),
			"requests24h": stats.Requests,
			"success24h":  stats.Success,
			"chars24h":    stats.Chars,
			"keysTotal":   total,
			"keysActive":  active,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"generatedAt": time.Now().Unix(),
		"windowHours": 24,
		"strategy":    strategy.String(),
		"strategies":  store.StrategyNames(),
		"rotation":    strategy.String(),
		"nextKeyId":   nextUp[s.defaultProviderName()],
		"providers":   providerRows,
		"gateway": map[string]any{
			"requests24h":      usage.Requests24h,
			"success24h":       usage.Success24h,
			"chars24h":         usage.CharsToday,
			"avgLatencyMs24h":  usage.AvgLatencyMS24h,
			"keysTotal":        len(keys),
			"clientsTotal":     len(clients),
			"clientKeysActive": countActiveClients(clients, now),
			"promptLogin":      gate.PromptLogin,
		},
		"clients": clientNodes,
		"keys":    keyNodes,
	})
}

func (s *Server) defaultProviderName() string {
	if p := s.providers.Default(); p != nil {
		return p.Name()
	}
	return ""
}

func countActiveClients(clients []store.ClientKey, now time.Time) int {
	active := 0
	for _, key := range clients {
		if key.IsActive && !key.Expired(now) {
			active++
		}
	}
	return active
}

var _ provider.Provider
