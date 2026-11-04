package gateway

import (
	"net/http"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/cartesia"
)

// handleModels serves the model catalogue in the OpenAI list shape.
func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	data := make([]map[string]any, 0, len(cartesia.Models))
	for _, id := range cartesia.Models {
		data = append(data, map[string]any{
			"id":       id,
			"object":   "model",
			"owned_by": "cartesia",
			"created":  time.Now().Unix(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}
