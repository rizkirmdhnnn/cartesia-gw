package gateway

import (
	"net/http"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/provider"
)

// handleModels serves the model catalogue in the OpenAI list shape. Ids are
// prefixed with their provider, so a value copied from this list can be sent
// straight back as "model".
func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	catalog := s.providers.Catalog()
	now := time.Now().Unix()
	data := make([]map[string]any, 0, len(catalog))
	for _, id := range catalog {
		owner, model := provider.SplitCatalogName(id)
		data = append(data, map[string]any{
			"id":       id,
			"object":   "model",
			"owned_by": owner,
			"created":  now,
			"name":     model,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}
