// Package elevenlabs speaks the ElevenLabs text-to-speech API: it builds the
// request for /v1/text-to-speech/{voice_id}/stream, lists voices and fetches
// voice previews.
//
// ElevenLabs differs from Cartesia in three places that matter for a proxy:
// the credential travels in xi-api-key, the voice is a path segment rather
// than a body field, and the audio format is a query parameter. Everything
// here exists to put the client's OpenAI-shaped request into that shape.
package elevenlabs

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/tts"
)

// Models is the catalogue ElevenLabs documents for text to speech, newest
// first. It drives GET /v1/models and the console; it is not a gate, because
// the provider ships models faster than this list and its own answer is more
// accurate.
var Models = []string{
	"eleven_v3",
	"eleven_multilingual_v2",
	"eleven_flash_v2_5",
	"eleven_flash_v2",
	"eleven_turbo_v2_5",
	"eleven_turbo_v2",
}

// DefaultModel is used when a request names none. It is the model ElevenLabs
// itself defaults to.
const DefaultModel = "eleven_multilingual_v2"

// DefaultVoice is the voice ElevenLabs uses when a request names none, or
// names one it cannot find. Rachel is the voice on the account every key
// starts with, so it is the one least likely to be missing.
const DefaultVoice = "21m00Tcm4TlvDq8ikWAM"

// Ranges ElevenLabs documents for voice_settings.speed. Narrower than
// Cartesia's, which is why a value is clamped rather than forwarded.
const (
	MinSpeed = 0.7
	MaxSpeed = 1.2
)

// maxRunRunes caps one request. ElevenLabs documents 40,000 characters for
// flash_v2_5 and 5,000-10,000 for the rest; the highest bound is enforced so a
// runaway input is refused here instead of arriving as a truncation upstream.
const maxRunRunes = 40000

// languageModel lists the languages a model can synthesise, where the
// documentation says so. A model absent from this map is assumed to support
// every language and the request is forwarded unchanged.
var languageModel = map[string][]string{
	"eleven_multilingual_v2": {"en", "ja", "zh", "de", "hi", "fr", "ko", "pt", "it", "es",
		"id", "nl", "tr", "fil", "pl", "sv", "bg", "ro", "ar", "cs", "el", "fi", "hr", "ms",
		"sk", "da", "ta", "uk", "ru"},
	"eleven_flash_v2_5": {"en", "ja", "zh", "de", "hi", "fr", "ko", "pt", "it", "es",
		"id", "nl", "tr", "fil", "pl", "sv", "bg", "ro", "ar", "cs", "el", "fi", "hr", "ms",
		"sk", "da", "ta", "uk", "ru", "hu", "no", "vi"},
	"eleven_flash_v2":            {"en"},
	"eleven_turbo_v2":            {"en"},
	"eleven_turbo_v2_5":          nil, // documented as equivalent to flash_v2_5
	"eleven_v3":                  nil,
	"eleven_multilingual_sts_v2": nil,
}

// ModelsForLanguage returns the models that can speak language, best first.
// "en" (or nothing) always has an answer.
func ModelsForLanguage(language string) []string {
	code := broadCode(language)
	out := make([]string, 0, len(Models))
	for _, model := range Models {
		languages, known := languageModel[model]
		if !known || languages == nil {
			out = append(out, model)
			continue
		}
		if code == "" || slices.Contains(languages, code) {
			out = append(out, model)
		}
	}
	return out
}

// SupportsLanguage reports whether model documents support for language. A
// model without a documented list is assumed to support everything.
func SupportsLanguage(model, language string) bool {
	languages, known := languageModel[model]
	if !known || languages == nil {
		return true
	}
	code := broadCode(language)
	if code == "" {
		return true
	}
	return slices.Contains(languages, code)
}

// broadCode reduces "en-GB" and "id-ID" to their primary subtag, because the
// documented lists use primary subtags only.
func broadCode(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	if idx := strings.IndexAny(language, "-_"); idx > 0 {
		language = language[:idx]
	}
	return language
}

// TranslatePlan validates a request already routed to ElevenLabs and turns it
// into the plan sent upstream.
//
// Nothing here refuses a request that can be served: a speed outside
// 0.7-1.2 is clamped, volume is dropped (ElevenLabs has no such control), an
// unsupported response_format becomes mp3, and a model that cannot speak the
// requested language is replaced by the best model that can. Every correction
// lands in plan.Adjustments.
func TranslatePlan(req tts.SpeechRequest, model, voice string, defaults Defaults) (tts.Plan, error) {
	text := strings.TrimSpace(req.Input)
	if text == "" {
		return tts.Plan{}, errors.New("missing required field: input")
	}
	voice = strings.TrimSpace(voice)
	if voice == "" {
		voice = defaults.Voice
	}
	if voice == "" {
		return tts.Plan{}, errors.New("missing required field: voice (the ElevenLabs voice id)")
	}

	plan := tts.Plan{
		Provider: "elevenlabs",
		Model:    model,
		Voice:    voice,
		Text:     text,
		Language: language(req.Language, req.Locale),
	}

	if runes := len([]rune(text)); runes > maxRunRunes {
		return tts.Plan{}, fmt.Errorf("input is %d characters; ElevenLabs accepts at most %d per request, so split it first", runes, maxRunRunes)
	}

	if plan.Language != "" && !SupportsLanguage(model, plan.Language) {
		if replacement := ModelsForLanguage(plan.Language)[0]; replacement != "" {
			plan.Adjust(fmt.Sprintf("model:%s->%s for language %s", model, replacement, plan.Language))
			plan.Model = replacement
		}
	}

	container, adjustment := containerFor(req.ResponseFormat)
	plan.Container = container
	plan.MIME = MIMEFor(container)
	plan.Extension = ExtensionFor(container)
	plan.Adjust(adjustment)

	plan.Speed = clamp(req.Speed, MinSpeed, MaxSpeed, "speed", &plan)
	if req.Volume != nil {
		plan.Adjust(fmt.Sprintf("volume:ignored %.2f (ElevenLabs has no volume control)", *req.Volume))
	}
	plan.Emotion = req.Emotion
	plan.Stability = req.Stability
	plan.SimilarityBoost = req.SimilarityBoost
	plan.Style = req.Style
	plan.SpeakerBoost = req.SpeakerBoost
	return plan, nil
}

// Defaults are the fallbacks the gateway applies when the request leaves a
// value out.
type Defaults struct {
	Voice string
}

func language(language, locale string) string {
	if language = strings.TrimSpace(language); language != "" {
		return language
	}
	return strings.TrimSpace(locale)
}

// containerFor maps the OpenAI format names onto what ElevenLabs serves.
func containerFor(responseFormat string) (container, adjustment string) {
	switch strings.ToLower(strings.TrimSpace(responseFormat)) {
	case "", "mp3":
		return "mp3", ""
	case "wav":
		return "wav", ""
	case "pcm", "raw":
		return "pcm", ""
	case "opus":
		return "opus", ""
	default:
		return "mp3", fmt.Sprintf("format:%s->mp3", strings.TrimSpace(responseFormat))
	}
}

// Content types ElevenLabs answers with, one per container.
func MIMEFor(container string) string {
	switch container {
	case "wav":
		return "audio/wav"
	case "pcm":
		return "audio/pcm"
	case "opus":
		return "audio/ogg"
	default:
		return "audio/mpeg"
	}
}

// ExtensionFor names the container in the JSON envelope.
func ExtensionFor(container string) string {
	return container
}

// OutputFormat is the query value that asks for a container at 44.1 kHz.
func OutputFormat(container string) string {
	switch container {
	case "wav":
		return "wav_44100"
	case "pcm":
		return "pcm_44100"
	case "opus":
		return "opus_44100_128"
	default:
		return "mp3_44100_128"
	}
}

// clamp keeps a value inside the provider's documented range and says so.
func clamp(value *float64, min, max float64, field string, plan *tts.Plan) *float64 {
	if value == nil {
		return nil
	}
	v := *value
	if v >= min && v <= max {
		return value
	}
	original := v
	if v < min {
		v = min
	} else {
		v = max
	}
	plan.Adjust(fmt.Sprintf("%s:clamped %.2f->%.2f", field, original, v))
	return &v
}

// EmotionStyle maps a Cartesia emotion name onto ElevenLabs' two nearest
// controls. ElevenLabs has no emotion vocabulary; style and stability are the
// documented way to aim a delivery, and this table is the translation the
// console's emotion picker needs to stay meaningful on both providers.
//
// Values are (style, stability). A stability of 0 keeps the voice's own
// default: only expressive emotions pin it.
var EmotionStyle = map[string]struct{ Style, Stability float64 }{
	"neutral": {0.0, 0.5}, "calm": {0.1, 0.75}, "peaceful": {0.1, 0.75}, "serene": {0.1, 0.75},
	"content": {0.2, 0.6}, "grateful": {0.3, 0.5}, "affectionate": {0.4, 0.4}, "trust": {0.2, 0.6},
	"happy": {0.5, 0.4}, "excited": {0.7, 0.3}, "enthusiastic": {0.7, 0.3}, "elated": {0.8, 0.25},
	"euphoric": {0.9, 0.2}, "triumphant": {0.8, 0.3}, "amazed": {0.7, 0.3}, "surprised": {0.6, 0.3},
	"curious": {0.4, 0.5}, "anticipation": {0.4, 0.5}, "confident": {0.4, 0.5}, "proud": {0.5, 0.4},
	"determined": {0.5, 0.4}, "mysterious": {0.3, 0.5}, "contemplative": {0.2, 0.6},
	"skeptical": {0.3, 0.5}, "sarcastic": {0.6, 0.3}, "ironic": {0.6, 0.3}, "flirtatious": {0.6, 0.35},
	"angry": {0.9, 0.2}, "mad": {0.9, 0.2}, "outraged": {1.0, 0.15}, "frustrated": {0.8, 0.25},
	"agitated": {0.8, 0.25}, "threatened": {0.7, 0.3}, "scared": {0.8, 0.25}, "panicked": {0.9, 0.2},
	"alarmed": {0.8, 0.25}, "anxious": {0.6, 0.35}, "insecure": {0.3, 0.5}, "hesitant": {0.3, 0.5},
	"sad": {0.5, 0.4}, "dejected": {0.5, 0.45}, "melancholic": {0.4, 0.5}, "disappointed": {0.4, 0.5},
	"hurt": {0.4, 0.5}, "guilty": {0.3, 0.5}, "apologetic": {0.3, 0.55}, "rejected": {0.4, 0.5},
	"bored": {0.2, 0.6}, "tired": {0.1, 0.65}, "resigned": {0.2, 0.6}, "nostalgic": {0.3, 0.55},
	"wistful": {0.3, 0.55}, "confused": {0.3, 0.5}, "disgusted": {0.7, 0.3}, "contempt": {0.6, 0.35},
	"envious": {0.5, 0.4}, "sympathetic": {0.3, 0.55}, "distant": {0.2, 0.65},
}
