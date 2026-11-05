package cartesia

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/tts"
)

// TranslatePlan validates a request that has already been routed to Cartesia
// and turns it into the plan the client sends.
//
// A request is served rather than refused whenever the gateway can make it
// work: an out-of-range speed is clamped to the range Cartesia documents, an
// unknown emotion is dropped, and a response format Cartesia has no container
// for becomes mp3. Each such correction is appended to plan.Adjustments so the
// operator can see it in history. Only a request that cannot mean anything —
// no text — is an error.
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
		return tts.Plan{}, errors.New("missing required field: voice (the Cartesia voice id, a UUID)")
	}

	plan := tts.Plan{
		Provider: "cartesia",
		Model:    model,
		Voice:    voice,
		Text:     text,
		Language: language(req.Language, req.Locale),
	}

	container, adjust := containerFor(req.ResponseFormat)
	plan.Container = container
	plan.MIME = MIMEFor(cartesiaContainer(container))
	plan.Extension = ExtensionFor(cartesiaContainer(container))
	plan.Adjust(adjust)

	plan.Speed = clamp(req.Speed, MinSpeed, MaxSpeed, "speed", &plan)
	plan.Volume = clamp(req.Volume, MinVolume, MaxVolume, "volume", &plan)
	plan.Emotion = emotion(req.Emotion, &plan)
	return plan, nil
}

// Defaults are the fallbacks the gateway applies when the request leaves a
// value out.
type Defaults struct {
	Voice string
}

// language prefers language over locale; setting both is a client choice, not
// a contradiction worth refusing.
func language(language, locale string) string {
	if language = strings.TrimSpace(language); language != "" {
		return language
	}
	return strings.TrimSpace(locale)
}

// containerFor maps the OpenAI container names onto what Cartesia serves. A
// format Cartesia has no container for becomes mp3 instead of an error, so a
// client written for another provider still gets audio.
func containerFor(responseFormat string) (container, adjustment string) {
	switch strings.ToLower(strings.TrimSpace(responseFormat)) {
	case "", "mp3":
		return "mp3", ""
	case "wav":
		return "wav", ""
	case "pcm", "raw":
		return "pcm", ""
	default:
		value := strings.TrimSpace(responseFormat)
		return "mp3", fmt.Sprintf("format:%s->mp3", value)
	}
}

// cartesiaContainer is the container name Cartesia's output_format uses.
func cartesiaContainer(container string) string {
	if container == "pcm" {
		return "raw"
	}
	return container
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

// emotion accepts the documented set and drops anything else: Cartesia would
// refuse an unknown emotion with a 400 that does not say which value was
// wrong, and one wrong value should not cost a whole request.
func emotion(value string, plan *tts.Plan) string {
	e := strings.ToLower(strings.TrimSpace(value))
	if e == "" {
		return ""
	}
	if !slices.Contains(Emotions, e) {
		plan.Adjust("emotion:dropped " + e)
		return ""
	}
	return e
}
