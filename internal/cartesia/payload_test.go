package cartesia

import (
	"strings"
	"testing"
)

func TestTranslate(t *testing.T) {
	speed := 1.25

	tests := []struct {
		name          string
		in            SpeechRequest
		wantModel     string
		wantContainer string
		wantMIME      string
		wantExt       string
		wantLanguage  string
		wantSpeed     float64
		wantVolume    float64
		wantEmotion   string
		wantErr       bool
	}{
		{
			name:          "defaults: sonic-3 and mp3",
			in:            SpeechRequest{Input: "hello", Voice: "v-1"},
			wantModel:     "sonic-3",
			wantContainer: "mp3",
			wantMIME:      "audio/mpeg",
			wantExt:       "mp3",
		},
		{
			name:          "provider prefix is stripped",
			in:            SpeechRequest{Model: "cartesia/sonic-3.6", Input: "hi", Voice: "v-1"},
			wantModel:     "sonic-3.6",
			wantContainer: "mp3",
			wantMIME:      "audio/mpeg",
			wantExt:       "mp3",
		},
		{
			name:          "wav container carries an encoding",
			in:            SpeechRequest{Model: "sonic-3", Input: "hi", Voice: "v-1", ResponseFormat: "wav"},
			wantModel:     "sonic-3",
			wantContainer: "wav",
			wantMIME:      "audio/wav",
			wantExt:       "wav",
		},
		{
			name:          "pcm maps onto the raw container, case-insensitively",
			in:            SpeechRequest{Model: "sonic-3", Input: "hi", Voice: "v-1", ResponseFormat: "PCM"},
			wantModel:     "sonic-3",
			wantContainer: "raw",
			wantMIME:      "audio/pcm",
			wantExt:       "pcm",
		},
		{
			name:          "language is forwarded",
			in:            SpeechRequest{Model: "sonic-3", Input: "hi", Voice: "v-1", Language: "id"},
			wantModel:     "sonic-3",
			wantContainer: "mp3",
			wantMIME:      "audio/mpeg",
			wantExt:       "mp3",
			wantLanguage:  "id",
		},
		{
			name:          "locale stands in for language",
			in:            SpeechRequest{Model: "sonic-3", Input: "hi", Voice: "v-1", Locale: "en-GB"},
			wantModel:     "sonic-3",
			wantContainer: "mp3",
			wantMIME:      "audio/mpeg",
			wantExt:       "mp3",
			wantLanguage:  "en-GB",
		},
		{
			name:          "speed inside the range is forwarded",
			in:            SpeechRequest{Model: "sonic-3", Input: "hi", Voice: "v-1", Speed: &speed},
			wantModel:     "sonic-3",
			wantContainer: "mp3",
			wantMIME:      "audio/mpeg",
			wantExt:       "mp3",
			wantSpeed:     1.25,
		},
		{
			name:          "volume is forwarded as a control",
			in:            SpeechRequest{Model: "sonic-3", Input: "hi", Voice: "v-1", Volume: ptr(0.75)},
			wantModel:     "sonic-3",
			wantContainer: "mp3",
			wantMIME:      "audio/mpeg",
			wantExt:       "mp3",
			wantVolume:    0.75,
		},
		{
			name:          "emotion is normalised to lower case",
			in:            SpeechRequest{Model: "sonic-3", Input: "hi", Voice: "v-1", Emotion: "  Excited "},
			wantModel:     "sonic-3",
			wantContainer: "mp3",
			wantMIME:      "audio/mpeg",
			wantExt:       "mp3",
			wantEmotion:   "excited",
		},
		{
			name: "all three controls travel together",
			in: SpeechRequest{
				Model: "sonic-3", Input: "hi", Voice: "v-1",
				Speed: ptr(1.1), Volume: ptr(1.4), Emotion: "calm",
			},
			wantModel:     "sonic-3",
			wantContainer: "mp3",
			wantMIME:      "audio/mpeg",
			wantExt:       "mp3",
			wantSpeed:     1.1,
			wantVolume:    1.4,
			wantEmotion:   "calm",
		},
		{
			name:    "input is required",
			in:      SpeechRequest{Model: "sonic-3", Voice: "v-1"},
			wantErr: true,
		},
		{
			name:    "whitespace-only input is empty input",
			in:      SpeechRequest{Model: "sonic-3", Input: "   ", Voice: "v-1"},
			wantErr: true,
		},
		{
			name:    "voice is required",
			in:      SpeechRequest{Model: "sonic-3", Input: "hi"},
			wantErr: true,
		},
		{
			name:    "a model/voice pair in voice is refused rather than reinterpreted",
			in:      SpeechRequest{Model: "sonic-3", Input: "hi", Voice: "sonic-3/db6b0ed5"},
			wantErr: true,
		},
		{
			name:    "unshaped model id is refused",
			in:      SpeechRequest{Model: "sonic 3!", Input: "hi", Voice: "v-1"},
			wantErr: true,
		},
		{
			name:    "unsupported container is refused, not silently downgraded",
			in:      SpeechRequest{Model: "sonic-3", Input: "hi", Voice: "v-1", ResponseFormat: "opus"},
			wantErr: true,
		},
		{
			name:    "speed below the range is refused",
			in:      SpeechRequest{Model: "sonic-3", Input: "hi", Voice: "v-1", Speed: ptr(0.5)},
			wantErr: true,
		},
		{
			name:    "speed above the range is refused",
			in:      SpeechRequest{Model: "sonic-3", Input: "hi", Voice: "v-1", Speed: ptr(1.6)},
			wantErr: true,
		},
		{
			name:    "volume below the range is refused",
			in:      SpeechRequest{Model: "sonic-3", Input: "hi", Voice: "v-1", Volume: ptr(0.4)},
			wantErr: true,
		},
		{
			name:    "volume above the range is refused",
			in:      SpeechRequest{Model: "sonic-3", Input: "hi", Voice: "v-1", Volume: ptr(2.5)},
			wantErr: true,
		},
		{
			name:    "an undocumented emotion is refused",
			in:      SpeechRequest{Model: "sonic-3", Input: "hi", Voice: "v-1", Emotion: "smug"},
			wantErr: true,
		},
		{
			name:    "language and locale together are refused",
			in:      SpeechRequest{Model: "sonic-3", Input: "hi", Voice: "v-1", Language: "en", Locale: "en-GB"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Translate(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Translate() = %+v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Translate() error = %v", err)
			}
			if got.Request.ModelID != tt.wantModel {
				t.Errorf("model_id = %q, want %q", got.Request.ModelID, tt.wantModel)
			}
			if got.Container != tt.wantContainer {
				t.Errorf("container = %q, want %q", got.Container, tt.wantContainer)
			}
			if got.MIME != tt.wantMIME {
				t.Errorf("mime = %q, want %q", got.MIME, tt.wantMIME)
			}
			if got.Extension != tt.wantExt {
				t.Errorf("extension = %q, want %q", got.Extension, tt.wantExt)
			}
			if got.Request.OutputFormat.Container != tt.wantContainer {
				t.Errorf("output_format.container = %q, want %q", got.Request.OutputFormat.Container, tt.wantContainer)
			}
			if got.Request.Language != tt.wantLanguage {
				t.Errorf("language = %q, want %q", got.Request.Language, tt.wantLanguage)
			}
			wantGeneration := tt.wantSpeed != 0 || tt.wantVolume != 0 || tt.wantEmotion != ""
			if wantGeneration && got.Request.GenerationConfig == nil {
				t.Fatal("generation_config = nil, want a control block")
			}
			if !wantGeneration && got.Request.GenerationConfig != nil {
				t.Errorf("generation_config = %+v, want nil so the model keeps its own defaults", got.Request.GenerationConfig)
			}
			if tt.wantSpeed != 0 && got.Request.GenerationConfig.Speed != tt.wantSpeed {
				t.Errorf("generation_config.speed = %v, want %v", got.Request.GenerationConfig.Speed, tt.wantSpeed)
			}
			if tt.wantVolume != 0 && got.Request.GenerationConfig.Volume != tt.wantVolume {
				t.Errorf("generation_config.volume = %v, want %v", got.Request.GenerationConfig.Volume, tt.wantVolume)
			}
			if tt.wantEmotion != "" && got.Request.GenerationConfig.Emotion != tt.wantEmotion {
				t.Errorf("generation_config.emotion = %q, want %q", got.Request.GenerationConfig.Emotion, tt.wantEmotion)
			}
			if strings.TrimSpace(got.Request.Transcript) != got.Request.Transcript {
				t.Errorf("transcript %q is not trimmed", got.Request.Transcript)
			}
		})
	}
}

// The output_format object is a required part of Cartesia's schema; a
// container on its own is rejected upstream.
func TestOutputFormatCarriesItsContainerRequirements(t *testing.T) {
	mp3 := OutputFormatFor("mp3")
	if mp3.BitRate == 0 || mp3.SampleRate == 0 {
		t.Errorf("mp3 needs sample_rate and bit_rate, got %+v", mp3)
	}
	for _, container := range []string{"wav", "raw"} {
		format := OutputFormatFor(container)
		if format.Encoding == "" || format.SampleRate == 0 {
			t.Errorf("%s needs encoding and sample_rate, got %+v", container, format)
		}
		if format.BitRate != 0 {
			t.Errorf("%s must not carry bit_rate, got %+v", container, format)
		}
	}
}

func TestNormalizeModelDefaultsAndStrips(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: DefaultModel},
		{in: "   ", want: DefaultModel},
		{in: "cartesia/sonic-latest", want: "sonic-latest"},
		{in: "sonic-3.5", want: "sonic-3.5"},
		{in: "unknown-model-v9", want: "unknown-model-v9"},
		{in: "-leading-dash", wantErr: true},
		{in: "with space", wantErr: true},
	}
	for _, tt := range tests {
		got, err := NormalizeModel(tt.in)
		if (err != nil) != tt.wantErr {
			t.Fatalf("NormalizeModel(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("NormalizeModel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestContainerFor(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: "mp3"},
		{in: "mp3", want: "mp3"},
		{in: "MP3", want: "mp3"},
		{in: "wav", want: "wav"},
		{in: "pcm", want: "raw"},
		{in: "opus", wantErr: true},
		{in: "aac", wantErr: true},
		{in: "flac", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ContainerFor(tt.in)
		if (err != nil) != tt.wantErr {
			t.Fatalf("ContainerFor(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ContainerFor(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAssertCartesiaURLRejectsForeignHosts(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{in: "https://api.cartesia.ai/voice/abc.mp3", wantErr: false},
		{in: "https://cdn.cartesia.ai/a/b.mp3", wantErr: false},
		{in: "http://api.cartesia.ai/a.mp3", wantErr: true},
		{in: "https://169.254.169.254/latest/meta-data", wantErr: true},
		{in: "https://cartesia.ai.evil.test/a.mp3", wantErr: true},
		{in: "not a url", wantErr: true},
	}
	for _, tt := range tests {
		err := assertCartesiaURL(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("assertCartesiaURL(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
	}
}

func TestEmotionsAreAUsableClosedSet(t *testing.T) {
	if len(Emotions) == 0 {
		t.Fatal("Emotions is empty")
	}
	seen := make(map[string]bool, len(Emotions))
	for _, emotion := range Emotions {
		if emotion == "" {
			t.Error("Emotions contains an empty value")
		}
		if emotion != strings.ToLower(strings.TrimSpace(emotion)) {
			t.Errorf("Emotions entry %q is not lowercase and trimmed", emotion)
		}
		if seen[emotion] {
			t.Errorf("Emotions contains %q twice", emotion)
		}
		seen[emotion] = true
	}
	// The dashboard puts the primary six at the top, so each must exist here.
	for _, primary := range PrimaryEmotions {
		if !seen[primary] {
			t.Errorf("primary emotion %q is not in Emotions", primary)
		}
	}
}

func TestPtrHelper(t *testing.T) {
	// ptr is used by the table above; keep it honest.
	if got := *ptr(1.5); got != 1.5 {
		t.Errorf("ptr(1.5) = %v", got)
	}
}

func ptr(v float64) *float64 { return &v }
