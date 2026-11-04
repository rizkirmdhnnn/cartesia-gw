package cartesia

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
)

// apiHost is the only host a preview URL may point at. The URL comes from
// Cartesia, but treating a remote-provided URL as trusted is how a gateway
// turns into an SSRF pivot.
const apiHost = "cartesia.ai"

// Client is a thin HTTP client for the Cartesia API. It returns raw responses
// so the caller can stream the audio body straight to its own client.
type Client struct {
	baseURL string
	version string
	http    *http.Client
}

// New builds a client. The transport is pooled and the overall timeout is 0,
// because a synthesis response streams and a wall-clock timeout would cut it
// mid-sentence; ResponseHeaderTimeout bounds the stall instead.
func New(baseURL, version string, timeout time.Duration) *Client {
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
		version: version,
		http:    &http.Client{Transport: transport, Timeout: timeout},
	}
}

// Synthesize posts a fully built payload to /tts/bytes. The caller owns the
// response body.
func (c *Client) Synthesize(ctx context.Context, apiKey string, req SynthesisRequest) (*http.Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode synthesis request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/tts/bytes", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build synthesis request: %w", err)
	}
	c.setHeaders(httpReq, apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call cartesia /tts/bytes: %w", err)
	}
	return resp, nil
}

// VoicesPage is GET /voices as far as this gateway consumes it.
type VoicesPage struct {
	Data     []Voice `json:"data"`
	HasMore  bool    `json:"has_more"`
	NextPage *string `json:"next_page"`
}

// Voice is one entry of GET /voices.
type Voice struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Description    string  `json:"description"`
	Gender         string  `json:"gender"`
	Language       string  `json:"language"`
	IsOwner        bool    `json:"is_owner"`
	PreviewFileURL *string `json:"preview_file_url"`
}

// ListVoices calls GET /voices. query is forwarded verbatim after validation
// of the keys the dashboard uses.
func (c *Client) ListVoices(ctx context.Context, apiKey string, query url.Values) (VoicesPage, error) {
	endpoint := c.baseURL + "/voices"
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return VoicesPage{}, fmt.Errorf("build voices request: %w", err)
	}
	c.setHeaders(req, apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return VoicesPage{}, fmt.Errorf("call cartesia /voices: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return VoicesPage{}, &UpstreamError{Status: resp.StatusCode, Message: readSnippet(resp.Body)}
	}
	var page VoicesPage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&page); err != nil {
		return VoicesPage{}, fmt.Errorf("decode voices response: %w", err)
	}
	return page, nil
}

// FetchPreview streams a voice preview file. The URL must point at Cartesia —
// see apiHost.
func (c *Client) FetchPreview(ctx context.Context, apiKey, rawURL string) (*http.Response, error) {
	if err := assertCartesiaURL(rawURL); err != nil {
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

// UpstreamError is a non-2xx answer from Cartesia.
type UpstreamError struct {
	Status  int
	Message string
	RetryIn time.Duration
}

func (e *UpstreamError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("cartesia returned %d", e.Status)
	}
	return fmt.Sprintf("cartesia returned %d: %s", e.Status, e.Message)
}

func (c *Client) setHeaders(req *http.Request, apiKey string) {
	// The OpenAPI document declares bearer auth; x-api-key is what upstream
	// 9router sends and what Cartesia has accepted historically. Bearer wins
	// because it is the documented contract.
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Cartesia-Version", c.version)
	req.Header.Set("User-Agent", "cartesia-gw")
}

func assertCartesiaURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("preview url is not a url: %w", err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("preview url must use https, got %q", parsed.Scheme)
	}
	host := strings.ToLower(parsed.Hostname())
	if host != apiHost && !strings.HasSuffix(host, "."+apiHost) {
		return fmt.Errorf("preview url host %q is not cartesia.ai", host)
	}
	return nil
}

func readSnippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 8<<10))
	return strings.TrimSpace(string(b))
}
