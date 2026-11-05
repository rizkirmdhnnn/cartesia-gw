package elevenlabs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/provider"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/tts"
)

// apiHost is the only host a preview URL may point at. The URL comes from
// ElevenLabs, but treating a remote-provided URL as trusted is how a gateway
// turns into an SSRF pivot.
const apiHost = "elevenlabs.io"

// previewHostSuffixes are the storage hosts ElevenLabs serves previews from.
// They are checked in addition to apiHost because the preview_url field points
// at the CDN rather than the API.
var previewHostSuffixes = []string{"elevenlabs.io", "eleven-public-prod.s3.amazonaws.com", "googleapis.com"}

// Client is a thin HTTP client for the ElevenLabs API. It returns raw
// responses so the caller can stream the audio body straight to its client.
type Client struct {
	baseURL string
	http    *http.Client
}

// New builds a client. The timeout is 0 for streaming: a wall-clock deadline
// would cut a long clip mid-sentence. ResponseHeaderTimeout bounds the stall.
func New(baseURL string, timeout time.Duration) *Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Transport: transport, Timeout: timeout},
	}
}

// synthesisBody is the body of POST /v1/text-to-speech/{voice_id}/stream.
type synthesisBody struct {
	Text          string         `json:"text"`
	ModelID       string         `json:"model_id"`
	LanguageCode  string         `json:"language_code,omitempty"`
	VoiceSettings *voiceSettings `json:"voice_settings,omitempty"`
}

type voiceSettings struct {
	Stability       *float64 `json:"stability,omitempty"`
	SimilarityBoost *float64 `json:"similarity_boost,omitempty"`
	Style           *float64 `json:"style,omitempty"`
	Speed           *float64 `json:"speed,omitempty"`
	SpeakerBoost    *bool    `json:"use_speaker_boost,omitempty"`
}

// BuildBody turns a plan into the JSON body and the query string to send.
// It is exported so a test can assert the wire shape without a network hop.
func BuildBody(plan tts.Plan) ([]byte, url.Values, error) {
	body := synthesisBody{
		Text:         plan.Text,
		ModelID:      plan.Model,
		LanguageCode: plan.Language,
	}
	if settings := buildSettings(plan); settings != nil {
		body.VoiceSettings = settings
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("encode elevenlabs request: %w", err)
	}
	query := url.Values{}
	query.Set("output_format", OutputFormat(plan.Container))
	return encoded, query, nil
}

// buildSettings merges the client's explicit voice settings with the ones an
// emotion implies. An explicit value always wins: the client asked for it.
func buildSettings(plan tts.Plan) *voiceSettings {
	settings := &voiceSettings{
		Stability:       plan.Stability,
		SimilarityBoost: plan.SimilarityBoost,
		Style:           plan.Style,
		Speed:           plan.Speed,
		SpeakerBoost:    plan.SpeakerBoost,
	}
	if emotion := strings.ToLower(strings.TrimSpace(plan.Emotion)); emotion != "" {
		if mapped, ok := EmotionStyle[emotion]; ok {
			if settings.Style == nil {
				style := mapped.Style
				settings.Style = &style
			}
			if settings.Stability == nil {
				stability := mapped.Stability
				settings.Stability = &stability
			}
		}
	}
	if *settings == (voiceSettings{}) {
		return nil
	}
	return settings
}

// Synthesize posts a plan to /v1/text-to-speech/{voice_id}/stream. The caller
// owns the response body.
func (c *Client) Synthesize(ctx context.Context, apiKey string, plan tts.Plan) (*http.Response, error) {
	body, query, err := BuildBody(plan)
	if err != nil {
		return nil, err
	}
	endpoint := c.baseURL + "/v1/text-to-speech/" + url.PathEscape(plan.Voice) + "/stream?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build elevenlabs request: %w", err)
	}
	c.setHeaders(req, apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/*")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call elevenlabs text-to-speech: %w", err)
	}
	return resp, nil
}

// ListVoices calls GET /v2/voices.
func (c *Client) ListVoices(ctx context.Context, apiKey string, query url.Values) (provider.VoicesPage, error) {
	endpoint := c.baseURL + "/v2/voices"
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return provider.VoicesPage{}, fmt.Errorf("build elevenlabs voices request: %w", err)
	}
	c.setHeaders(req, apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return provider.VoicesPage{}, fmt.Errorf("call elevenlabs /v2/voices: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return provider.VoicesPage{}, &UpstreamError{Status: resp.StatusCode, Message: readSnippet(resp.Body)}
	}
	var page struct {
		Voices        []map[string]any `json:"voices"`
		HasMore       bool             `json:"has_more"`
		NextPageToken *string          `json:"next_page_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&page); err != nil {
		return provider.VoicesPage{}, fmt.Errorf("decode elevenlabs voices: %w", err)
	}
	out := provider.VoicesPage{Data: page.Voices, HasMore: page.HasMore}
	if page.NextPageToken != nil {
		out.NextPage = *page.NextPageToken
	}
	return out, nil
}

// FetchPreview streams a voice preview file. The URL must point at a host
// ElevenLabs owns — see previewHostSuffixes.
func (c *Client) FetchPreview(ctx context.Context, apiKey, rawURL string) (*http.Response, error) {
	if err := assertPreviewURL(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build preview request: %w", err)
	}
	c.setHeaders(req, apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch voice preview: %w", err)
	}
	return resp, nil
}

// UpstreamError is a non-2xx answer from ElevenLabs.
type UpstreamError struct {
	Status  int
	Message string
	RetryIn time.Duration
}

func (e *UpstreamError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("elevenlabs returned %d", e.Status)
	}
	return fmt.Sprintf("elevenlabs returned %d: %s", e.Status, e.Message)
}

// setHeaders attaches the credential exactly as ElevenLabs documents it.
func (c *Client) setHeaders(req *http.Request, apiKey string) {
	req.Header.Set("xi-api-key", apiKey)
	req.Header.Set("User-Agent", "cartesia-gw")
}

func assertPreviewURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("preview url is not a url: %w", err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("preview url must use https, got %q", parsed.Scheme)
	}
	host := strings.ToLower(parsed.Hostname())
	for _, suffix := range previewHostSuffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return nil
		}
	}
	return fmt.Errorf("preview url host %q is not an elevenlabs host", host)
}

func readSnippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 8<<10))
	return strings.TrimSpace(string(b))
}
