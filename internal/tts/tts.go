// Package tts holds the request types both speech providers share: the
// OpenAI-shaped body a client sends, and the plan a provider turns it into.
package tts

import "strings"

// SpeechRequest is the OpenAI /v1/audio/speech body a client sends here, plus
// the Cartesia and ElevenLabs controls OpenAI has no equivalent for. Unknown
// fields are ignored, so a client built for either provider keeps working.
type SpeechRequest struct {
	Model          string   `json:"model"`
	Input          string   `json:"input"`
	Voice          string   `json:"voice"`
	ResponseFormat string   `json:"response_format"`
	Speed          *float64 `json:"speed"`
	Volume         *float64 `json:"volume"`
	Emotion        string   `json:"emotion"`
	Language       string   `json:"language"`
	Locale         string   `json:"locale"`
	// ElevenLabs voice settings, passed through when the provider supports
	// them. Pointers so "unset" stays distinct from "0".
	Stability       *float64 `json:"stability"`
	SimilarityBoost *float64 `json:"similarity_boost"`
	Style           *float64 `json:"style"`
	SpeakerBoost    *bool    `json:"use_speaker_boost"`
}

// Plan is a validated request turned into one provider's shape. The provider
// that built it knows how to send it.
type Plan struct {
	Provider string
	Model    string
	// Voice is the identifier the upstream provider expects: a Cartesia UUID
	// or an ElevenLabs voice id.
	Voice    string
	Text     string
	Language string
	// Container is the normalised output container: "mp3", "wav", "pcm" or
	// "opus".
	Container string
	MIME      string
	Extension string
	Speed     *float64
	Volume    *float64
	Emotion   string

	Stability       *float64
	SimilarityBoost *float64
	Style           *float64
	SpeakerBoost    *bool

	// Adjustments records what the gateway changed to keep the request
	// servable, e.g. ["speed:clamped 1.6->1.2"]. It is shown in history so a
	// silently corrected call is still visible.
	Adjustments []string
}

// Adjust appends one note to the plan's adjustment log.
func (p *Plan) Adjust(note string) {
	if note == "" {
		return
	}
	p.Adjustments = append(p.Adjustments, note)
}

// Trim returns the request's text with surrounding whitespace removed.
func (r SpeechRequest) Trim() string { return strings.TrimSpace(r.Input) }
