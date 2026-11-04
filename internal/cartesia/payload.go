// Package cartesia speaks the Cartesia text-to-speech API: it builds the
// request payload, calls /tts/bytes, and lists voices.
package cartesia

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Models is the catalogue Cartesia documents today. It drives GET /v1/models
// and the dashboard dropdown; it is not a validation gate, because Cartesia
// ships new models faster than this list can track and its own rejection is
// more accurate than a guess made here.
var Models = []string{"sonic-3.6", "sonic-3.5", "sonic-3", "sonic-latest"}

// DefaultModel is used when a caller omits "model".
const DefaultModel = "sonic-3"

// ProviderPrefix is accepted and stripped, so a client that already speaks the
// 9router "provider/model" convention does not need to be changed.
const ProviderPrefix = "cartesia/"

var modelShape = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{0,63}$`)

// SynthesisRequest is the Cartesia /tts/bytes body.
type SynthesisRequest struct {
	ModelID          string            `json:"model_id"`
	Transcript       string            `json:"transcript"`
	Voice            string            `json:"voice"`
	OutputFormat     OutputFormat      `json:"output_format"`
	Language         string            `json:"language,omitempty"`
	GenerationConfig *GenerationConfig `json:"generation_config,omitempty"`
}

// OutputFormat is an object in Cartesia's schema, never a bare string.
type OutputFormat struct {
	Container  string `json:"container"`
	SampleRate int    `json:"sample_rate,omitempty"`
	BitRate    int    `json:"bit_rate,omitempty"`
	Encoding   string `json:"encoding,omitempty"`
}

// GenerationConfig carries the per-request controls Cartesia exposes on
// sonic-3 and newer. Zero values are omitted so an unset control does not
// override the model's own default.
type GenerationConfig struct {
	Speed   float64 `json:"speed,omitempty"`
	Volume  float64 `json:"volume,omitempty"`
	Emotion string  `json:"emotion,omitempty"`
}

// SpeechRequest is the OpenAI /v1/audio/speech body a client sends here, plus
// the two Cartesia controls OpenAI has no equivalent for.
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
}

// Translation is a validated request turned into Cartesia's shape.
type Translation struct {
	Request   SynthesisRequest
	Container string
	MIME      string
	Extension string
}

// Ranges Cartesia documents. A value outside one is a client error rather
// than something to clamp silently.
const (
	MinSpeed  = 0.6
	MaxSpeed  = 1.5
	MinVolume = 0.5
	MaxVolume = 2.0
)

// Emotions is the closed set Cartesia accepts, in the order its own schema
// lists them. PrimaryEmotions are the six it names first and surfaces most
// prominently; the dashboard puts them at the top of the picker.
var Emotions = []string{
	"neutral", "happy", "excited", "enthusiastic", "elated", "euphoric",
	"triumphant", "amazed", "surprised", "flirtatious", "curious", "content",
	"peaceful", "serene", "calm", "grateful", "affectionate", "trust",
	"sympathetic", "anticipation", "mysterious", "angry", "mad", "outraged",
	"frustrated", "agitated", "threatened", "disgusted", "contempt", "envious",
	"sarcastic", "ironic", "sad", "dejected", "melancholic", "disappointed",
	"hurt", "guilty", "bored", "tired", "rejected", "nostalgic", "wistful",
	"apologetic", "hesitant", "insecure", "confused", "resigned", "anxious",
	"panicked", "alarmed", "scared", "proud", "confident", "distant",
	"skeptical", "contemplative", "determined",
}

// PrimaryEmotions are the six Cartesia lists as the primary controls.
var PrimaryEmotions = []string{"neutral", "calm", "angry", "content", "sad", "scared"}

// Translate validates an OpenAI-shaped request and maps it onto Cartesia's
// payload. Every error returned here is the caller's fault (HTTP 400).
func Translate(req SpeechRequest) (Translation, error) {
	text := strings.TrimSpace(req.Input)
	if text == "" {
		return Translation{}, errors.New("missing required field: input")
	}
	if strings.TrimSpace(req.Voice) == "" {
		return Translation{}, errors.New("missing required field: voice (the Cartesia voice id, a UUID)")
	}
	if strings.Contains(req.Voice, "/") {
		return Translation{}, errors.New("voice must be a Cartesia voice id, not a model/voice pair; set model and voice as separate fields")
	}
	if req.Language != "" && req.Locale != "" {
		return Translation{}, errors.New("set language or locale, never both")
	}

	model, err := NormalizeModel(req.Model)
	if err != nil {
		return Translation{}, err
	}
	container, err := ContainerFor(req.ResponseFormat)
	if err != nil {
		return Translation{}, err
	}
	speed, err := normalizeSpeed(req.Speed)
	if err != nil {
		return Translation{}, err
	}
	volume, err := normalizeVolume(req.Volume)
	if err != nil {
		return Translation{}, err
	}
	emotion, err := normalizeEmotion(req.Emotion)
	if err != nil {
		return Translation{}, err
	}

	language := req.Language
	if language == "" {
		language = req.Locale
	}

	t := Translation{
		Request: SynthesisRequest{
			ModelID:      model,
			Transcript:   text,
			Voice:        strings.TrimSpace(req.Voice),
			OutputFormat: OutputFormatFor(container),
			Language:     language,
		},
		Container: container,
		MIME:      MIMEFor(container),
		Extension: ExtensionFor(container),
	}
	if gen := generationConfig(speed, volume, emotion); gen != nil {
		t.Request.GenerationConfig = gen
	}
	return t, nil
}

// generationConfig omits itself entirely when no control was set, so an
// untouched request keeps the model's own defaults.
func generationConfig(speed, volume *float64, emotion string) *GenerationConfig {
	if speed == nil && volume == nil && emotion == "" {
		return nil
	}
	gen := &GenerationConfig{Emotion: emotion}
	if speed != nil {
		gen.Speed = *speed
	}
	if volume != nil {
		gen.Volume = *volume
	}
	return gen
}

// NormalizeModel strips the accepted provider prefix and defaults an empty
// value. Shape is checked; membership of Models is not.
func NormalizeModel(model string) (string, error) {
	model = strings.TrimSpace(model)
	model = strings.TrimPrefix(model, ProviderPrefix)
	if model == "" {
		return DefaultModel, nil
	}
	if !modelShape.MatchString(model) {
		return "", fmt.Errorf("model %q is not a valid Cartesia model id", model)
	}
	return model, nil
}

// ContainerFor maps the OpenAI container names onto the three Cartesia
// supports. Accepting "opus" and returning mp3 would be a contract lie that
// only shows up downstream.
func ContainerFor(responseFormat string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(responseFormat)) {
	case "", "mp3":
		return "mp3", nil
	case "wav":
		return "wav", nil
	case "pcm":
		return "raw", nil
	default:
		return "", fmt.Errorf("response_format %q is not supported; Cartesia accepts mp3, wav or pcm", responseFormat)
	}
}

// OutputFormatFor builds the required output_format object for a container.
func OutputFormatFor(container string) OutputFormat {
	switch container {
	case "wav":
		return OutputFormat{Container: "wav", SampleRate: 44100, Encoding: "pcm_s16le"}
	case "raw":
		return OutputFormat{Container: "raw", SampleRate: 44100, Encoding: "pcm_s16le"}
	default:
		return OutputFormat{Container: "mp3", SampleRate: 44100, BitRate: 128000}
	}
}

// MIMEFor returns the Content-Type the client receives.
func MIMEFor(container string) string {
	switch container {
	case "wav":
		return "audio/wav"
	case "raw":
		return "audio/pcm"
	default:
		return "audio/mpeg"
	}
}

// ExtensionFor names the container in the JSON envelope.
func ExtensionFor(container string) string {
	if container == "raw" {
		return "pcm"
	}
	return container
}

func normalizeSpeed(speed *float64) (*float64, error) {
	if speed == nil {
		return nil, nil
	}
	v := *speed
	if v < MinSpeed || v > MaxSpeed {
		return nil, fmt.Errorf("speed %.2f is outside the range Cartesia supports (%.1f-%.1f)", v, MinSpeed, MaxSpeed)
	}
	return &v, nil
}

func normalizeVolume(volume *float64) (*float64, error) {
	if volume == nil {
		return nil, nil
	}
	v := *volume
	if v < MinVolume || v > MaxVolume {
		return nil, fmt.Errorf("volume %.2f is outside the range Cartesia supports (%.1f-%.1f)", v, MinVolume, MaxVolume)
	}
	return &v, nil
}

// normalizeEmotion accepts the documented set and nothing else: Cartesia's own
// rejection would arrive as a 400 that says nothing about which value was
// wrong, so the closed set is checked here where the message can name it.
func normalizeEmotion(emotion string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(emotion))
	if e == "" {
		return "", nil
	}
	if !slices.Contains(Emotions, e) {
		return "", fmt.Errorf("emotion %q is not one of the %d emotions Cartesia supports", emotion, len(Emotions))
	}
	return e, nil
}
