package elevenlabs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/tts"
)

func ptr(v float64) *float64 { return &v }

// The wire shape is the part a proxy must get exactly right: ElevenLabs
// reads the voice from the path, the format from the query, and text/model
// from the body. A body carrying OpenAI fields would be rejected upstream.
func TestBuildBodyUsesElevenLabsShape(t *testing.T) {
	plan, err := TranslatePlan(tts.SpeechRequest{
		Input:          "Halo dunia",
		ResponseFormat: "mp3",
		Speed:          ptr(1.0),
	}, "eleven_v3", "JBFqnCBsd6RMkjVDRZzb", Defaults{})
	if err != nil {
		t.Fatalf("TranslatePlan() error = %v", err)
	}
	body, query, err := BuildBody(plan)
	if err != nil {
		t.Fatalf("BuildBody() error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if decoded["text"] != "Halo dunia" {
		t.Errorf("text = %v, want the input", decoded["text"])
	}
	if decoded["model_id"] != "eleven_v3" {
		t.Errorf("model_id = %v", decoded["model_id"])
	}
	if _, ok := decoded["voice"]; ok {
		t.Error("body carries a voice field; ElevenLabs takes the voice in the path")
	}
	if _, ok := decoded["input"]; ok {
		t.Error("body carries the OpenAI input field")
	}
	if got := query.Get("output_format"); got != "mp3_44100_128" {
		t.Errorf("output_format = %q", got)
	}
}

func TestOutputFormatPerContainer(t *testing.T) {
	cases := map[string]string{
		"mp3":  "mp3_44100_128",
		"wav":  "wav_44100",
		"pcm":  "pcm_44100",
		"opus": "opus_44100_128",
	}
	for container, want := range cases {
		if got := OutputFormat(container); got != want {
			t.Errorf("OutputFormat(%q) = %q, want %q", container, got, want)
		}
	}
}

// Speed is clamped to what ElevenLabs documents, and the caller is told.
func TestSpeedIsClampedAndRecorded(t *testing.T) {
	plan, err := TranslatePlan(tts.SpeechRequest{Input: "hi", Speed: ptr(1.6)}, "eleven_v3", "JBFqnCBsd6RMkjVDRZzb", Defaults{})
	if err != nil {
		t.Fatalf("TranslatePlan() error = %v", err)
	}
	if plan.Speed == nil || *plan.Speed != MaxSpeed {
		t.Fatalf("speed = %v, want clamped to %v", plan.Speed, MaxSpeed)
	}
	if !hasAdjustment(plan, "speed:clamped") {
		t.Errorf("adjustments = %v, want the clamp recorded", plan.Adjustments)
	}
}

// Volume has no ElevenLabs equivalent. Dropping it with a note keeps a
// cross-provider client working instead of failing it.
func TestVolumeIsDroppedNotRefused(t *testing.T) {
	plan, err := TranslatePlan(tts.SpeechRequest{Input: "hi", Volume: ptr(1.4)}, "eleven_v3", "JBFqnCBsd6RMkjVDRZzb", Defaults{})
	if err != nil {
		t.Fatalf("TranslatePlan() error = %v", err)
	}
	if !hasAdjustment(plan, "volume:ignored") {
		t.Errorf("adjustments = %v, want the dropped volume recorded", plan.Adjustments)
	}
}

// A format ElevenLabs has no container for becomes mp3, not a 400.
func TestUnknownFormatFallsBackToMP3(t *testing.T) {
	plan, err := TranslatePlan(tts.SpeechRequest{Input: "hi", ResponseFormat: "flac"}, "eleven_v3", "JBFqnCBsd6RMkjVDRZzb", Defaults{})
	if err != nil {
		t.Fatalf("TranslatePlan() error = %v", err)
	}
	if plan.Container != "mp3" {
		t.Errorf("container = %q, want mp3", plan.Container)
	}
	if !hasAdjustment(plan, "format:flac->mp3") {
		t.Errorf("adjustments = %v", plan.Adjustments)
	}
}

// A language the model does not speak moves the request to a model that does.
func TestLanguageUpgradesTheModel(t *testing.T) {
	plan, err := TranslatePlan(tts.SpeechRequest{Input: "hi", Language: "id"}, "eleven_flash_v2", "JBFqnCBsd6RMkjVDRZzb", Defaults{})
	if err != nil {
		t.Fatalf("TranslatePlan() error = %v", err)
	}
	if plan.Model == "eleven_flash_v2" {
		t.Fatal("the request stayed on an English-only model for an Indonesian text")
	}
	if !SupportsLanguage(plan.Model, "id") {
		t.Errorf("model %q does not support id", plan.Model)
	}
	if !hasAdjustment(plan, "model:") {
		t.Errorf("adjustments = %v, want the substitution recorded", plan.Adjustments)
	}
}

// Emotion has no ElevenLabs field, so it is mapped onto the two controls that
// express delivery.
func TestEmotionBecomesStyleAndStability(t *testing.T) {
	plan, err := TranslatePlan(tts.SpeechRequest{Input: "hi", Emotion: "excited"}, "eleven_v3", "JBFqnCBsd6RMkjVDRZzb", Defaults{})
	if err != nil {
		t.Fatalf("TranslatePlan() error = %v", err)
	}
	body, _, _ := BuildBody(plan)
	if !strings.Contains(string(body), `"style"`) || !strings.Contains(string(body), `"stability"`) {
		t.Errorf("body has no style/stability for an emotion: %s", body)
	}
}

// An explicit setting always beats the one an emotion implies.
func TestExplicitSettingsBeatTheEmotion(t *testing.T) {
	plan, err := TranslatePlan(tts.SpeechRequest{Input: "hi", Emotion: "excited", Style: ptr(0.1)}, "eleven_v3", "JBFqnCBsd6RMkjVDRZzb", Defaults{})
	if err != nil {
		t.Fatalf("TranslatePlan() error = %v", err)
	}
	if plan.Style == nil || *plan.Style != 0.1 {
		t.Errorf("style = %v, want the explicit 0.1", plan.Style)
	}
}

func TestMissingVoiceFallsBackToDefault(t *testing.T) {
	plan, err := TranslatePlan(tts.SpeechRequest{Input: "hi"}, "eleven_v3", "", Defaults{Voice: "21m00Tcm4TlvDq8ikWAM"})
	if err != nil {
		t.Fatalf("TranslatePlan() error = %v", err)
	}
	if plan.Voice != "21m00Tcm4TlvDq8ikWAM" {
		t.Errorf("voice = %q, want the configured default", plan.Voice)
	}
}

func TestInputIsRequired(t *testing.T) {
	if _, err := TranslatePlan(tts.SpeechRequest{}, "eleven_v3", "x", Defaults{}); err == nil {
		t.Fatal("an empty input must be refused")
	}
}

func TestCleanMessageFlattensDetail(t *testing.T) {
	got := cleanMessage(`{"detail":[{"loc":["body","text"],"msg":"field required","type":"missing"}]}`)
	if got != "field required" {
		t.Errorf("cleanMessage() = %q", got)
	}
}

func TestPreviewGuardRejectsForeignHosts(t *testing.T) {
	tests := map[string]bool{
		"https://api.elevenlabs.io/v1/x.mp3":                      true,
		"https://storage.googleapis.com/eleven-public-prod/a.mp3": true,
		"http://api.elevenlabs.io/x.mp3":                          false,
		"https://169.254.169.254/latest/meta-data":                false,
		"https://elevenlabs.io.evil.test/a.mp3":                   false,
	}
	for rawURL, want := range tests {
		err := assertPreviewURL(rawURL)
		if (err == nil) != want {
			t.Errorf("assertPreviewURL(%q) error = %v, want allowed=%v", rawURL, err, want)
		}
	}
}

func hasAdjustment(plan tts.Plan, prefix string) bool {
	for _, adjustment := range plan.Adjustments {
		if strings.HasPrefix(adjustment, prefix) {
			return true
		}
	}
	return false
}
