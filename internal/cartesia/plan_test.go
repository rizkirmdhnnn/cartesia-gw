package cartesia

import (
	"strings"
	"testing"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/tts"
)

// TranslatePlan is the lenient path the gateway uses: a request that can be
// served is served, and every correction is recorded.
func TestTranslatePlanServesAndRecords(t *testing.T) {
	plan, err := TranslatePlan(tts.SpeechRequest{
		Input:          "halo",
		ResponseFormat: "opus",
		Speed:          ptr(2.0),
		Volume:         ptr(0.2),
		Emotion:        "smug",
		Language:       "id",
		Locale:         "id-ID",
	}, "sonic-3.6", "5c5e3a1c-1b2d-4e5f-8a9b-0c1d2e3f4a5b", Defaults{})
	if err != nil {
		t.Fatalf("TranslatePlan() error = %v", err)
	}
	if plan.Container != "mp3" {
		t.Errorf("container = %q, want mp3 for a format Cartesia has no container for", plan.Container)
	}
	if plan.Speed == nil || *plan.Speed != MaxSpeed {
		t.Errorf("speed = %v, want clamped to %v", plan.Speed, MaxSpeed)
	}
	if plan.Volume == nil || *plan.Volume != MinVolume {
		t.Errorf("volume = %v, want clamped to %v", plan.Volume, MinVolume)
	}
	if plan.Emotion != "" {
		t.Errorf("emotion = %q, want an undocumented emotion dropped", plan.Emotion)
	}
	if plan.Language != "id" {
		t.Errorf("language = %q, want language to win over locale", plan.Language)
	}
	for _, want := range []string{"format:opus->mp3", "speed:clamped", "volume:clamped", "emotion:dropped"} {
		if !hasAdjustment(plan, want) {
			t.Errorf("adjustments = %v, want %q", plan.Adjustments, want)
		}
	}
}

func TestTranslatePlanKeepsValidValuesUntouched(t *testing.T) {
	plan, err := TranslatePlan(tts.SpeechRequest{
		Input: "ok", Voice: "v-1", Speed: ptr(1.2), Emotion: "calm",
	}, "sonic-3", "v-1", Defaults{})
	if err != nil {
		t.Fatalf("TranslatePlan() error = %v", err)
	}
	if len(plan.Adjustments) != 0 {
		t.Errorf("adjustments = %v, want none", plan.Adjustments)
	}
	if plan.Speed == nil || *plan.Speed != 1.2 || plan.Emotion != "calm" {
		t.Errorf("plan = %+v, want the values as sent", plan)
	}
}

func TestTranslatePlanRequiresTextAndVoice(t *testing.T) {
	if _, err := TranslatePlan(tts.SpeechRequest{}, "sonic-3", "v-1", Defaults{}); err == nil {
		t.Error("an empty input must be refused")
	}
	if _, err := TranslatePlan(tts.SpeechRequest{Input: "hi"}, "sonic-3", "", Defaults{}); err == nil {
		t.Error("a missing voice with no default must be refused")
	}
}

// PlanToRequest is the bridge the client sends: the payload must be Cartesia's
// schema, with the container mapped back to its own vocabulary.
func TestPlanToRequestBuildsCartesiaPayload(t *testing.T) {
	plan := tts.Plan{
		Model: "sonic-3.6", Voice: "v-1", Text: "halo", Container: "pcm", Language: "id",
		Speed: ptr(1.1), Volume: ptr(1.2), Emotion: "calm",
	}
	req := PlanToRequest(plan)
	if req.ModelID != "sonic-3.6" || req.Transcript != "halo" || req.Voice != "v-1" {
		t.Errorf("request = %+v", req)
	}
	if req.OutputFormat.Container != "raw" {
		t.Errorf("output_format.container = %q, want raw for pcm", req.OutputFormat.Container)
	}
	if req.GenerationConfig == nil || req.GenerationConfig.Speed != 1.1 || req.GenerationConfig.Emotion != "calm" {
		t.Errorf("generation_config = %+v", req.GenerationConfig)
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
