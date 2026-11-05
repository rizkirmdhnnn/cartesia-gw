package gateway

import (
	"net/http"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

// handleRouting answers GET /api/routing: everything the traffic visualiser
// draws, in one response. It is deliberately one snapshot — clients, keys,
// shares and "next up" — so the two sides of the graph always describe the
// same traffic window and the same picker state.
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
	nextID, err := s.store.PickPreview(ctx)
	if err != nil {
		s.logError("routing: pick preview", err)
	}
	gate, err := s.store.Gate(ctx)
	if err != nil {
		s.logError("routing: gate", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}

	// Totals first, so each side's share adds up to 1.
	var keyTotal, clientTotal int64
	for _, traffic := range byKey {
		keyTotal += traffic.Requests
	}
	for _, traffic := range byClient {
		clientTotal += traffic.Requests
	}

	now := time.Now()
	type keyNode struct {
		keyView
		Success24h int64 `json:"success24h"`
		Chars24h   int64 `json:"chars24h"`
		Latency24h int64 `json:"avgLatencyMs24h"`
		NextUp     bool  `json:"nextUp"`
	}
	keyNodes := make([]keyNode, 0, len(keys))
	for _, key := range keys {
		traffic := byKey[key.ID]
		share := 0.0
		if keyTotal > 0 {
			share = float64(traffic.Requests) / float64(keyTotal)
		}
		node := keyNode{
			keyView:    toKeyView(key, now),
			Success24h: traffic.Success,
			Chars24h:   traffic.Chars,
			Latency24h: traffic.AvgLatency,
		}
		node.Requests24h = traffic.Requests
		node.Share = share
		node.Eligible = key.Eligible(now)
		node.NextUp = key.ID == nextID
		keyNodes = append(keyNodes, node)
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

	writeJSON(w, http.StatusOK, map[string]any{
		"generatedAt": time.Now().Unix(),
		"windowHours": 24,
		"strategy":    strategy.String(),
		"strategies":  store.StrategyNames(),
		"rotation":    strategy.String(),
		"nextKeyId":   nextID,
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

func countActiveClients(clients []store.ClientKey, now time.Time) int {
	active := 0
	for _, key := range clients {
		if key.IsActive && !key.Expired(now) {
			active++
		}
	}
	return active
}
