package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/cartesia"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/elevenlabs"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/provider"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/tts"
)

const (
	maxSpeechBody = 1 << 20 // 1 MiB of JSON is already a very long transcript
	maxAudioBytes = 64 << 20
	maxAttempts   = 4
	streamBufSize = 32 << 10
)

// handleSpeech is the OpenAI-compatible endpoint. The model string decides
// which provider answers; see provider.ParseModel. The body is translated into
// that provider's schema; the query parameter ?response_format=json selects
// the base64 envelope the 9router clients expect.
func (s *Server) handleSpeech(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSpeechBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	var req tts.SpeechRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	envelope := wantJSONEnvelope(r)
	plan, providerName, err := s.planFor(r.Context(), req)
	if err != nil {
		s.writePlanError(w, err)
		return
	}
	w.Header().Set("X-Gateway-Provider", providerName)
	w.Header().Set("X-Gateway-Requested-Model", strings.TrimSpace(req.Model))
	s.synthesize(w, r, plan, envelope)
}

// planError is a request that cannot be served, with the status the client
// should see. It carries its own status so a missing key (a setup problem) is
// not reported as a client error.
type planError struct {
	Status  int
	Message string
}

func (e *planError) Error() string { return e.Message }

// planFor turns a client request into one provider's plan: it picks the
// provider from the model string, confirms that provider has a key, and then
// lets the provider translate, clamping and correcting rather than refusing.
func (s *Server) planFor(ctx context.Context, req tts.SpeechRequest) (tts.Plan, string, error) {
	model, err := provider.ParseModel(req.Model)
	if err != nil {
		return tts.Plan{}, "", &planError{Status: http.StatusBadRequest, Message: err.Error()}
	}
	p, name, err := s.providers.Resolve(model)
	if err != nil || p == nil {
		message := "no speech provider configured"
		if err != nil {
			message = err.Error()
		}
		return tts.Plan{}, "", &planError{Status: http.StatusServiceUnavailable, Message: message}
	}
	if model.Explicit && !strings.EqualFold(model.Name, p.DefaultModel()) && !provider.Known(p, name) {
		// An explicit provider with a model it does not serve is a typo worth
		// naming: falling through to the default would still cost a request.
		return tts.Plan{}, name, &planError{Status: http.StatusBadRequest,
			Message: fmt.Sprintf("model %q is not a %s model; try %s/%s", model.Name, p.Label(), p.Name(), p.DefaultModel())}
	}

	// Rotation is about keys, not about the provider: a request that names a
	// provider the operator has added no key for is a setup problem and says
	// so (424). An empty database falls through to the rotation loop, which
	// answers 503 and points at the dashboard.
	if model.Explicit {
		count, err := s.probeKeys(ctx, p.Name())
		if err != nil {
			return tts.Plan{}, name, &planError{Status: http.StatusInternalServerError, Message: "database unavailable"}
		}
		if count == 0 && s.anyKeys(ctx) {
			return tts.Plan{}, name, &planError{Status: http.StatusFailedDependency,
				Message: fmt.Sprintf("model %s/%s needs an %s API key: add one under Upstream keys in the dashboard",
					p.Name(), name, p.Label())}
		}
	}

	// The body's voice wins over the model string's third segment, which is how
	// the 9router clients already behave.
	requested := strings.TrimSpace(req.Voice)
	if requested == "" || isPlaceholderVoice(requested) {
		requested = model.Voice
	}
	defaults := s.defaultsFor(ctx, p, req)
	voice, adjustment := s.resolveVoice(ctx, p, requested, defaults.Voice)
	defaults.Voice = voice

	req.Voice = voice
	plan := tts.Plan{}
	switch p.Name() {
	case "cartesia":
		plan, err = cartesia.TranslatePlan(req, name, defaults.Voice, cartesia.Defaults{})
	case "elevenlabs":
		plan, err = elevenlabs.TranslatePlan(req, name, defaults.Voice, elevenlabs.Defaults{})
	default:
		err = &planError{Status: http.StatusNotImplemented,
			Message: fmt.Sprintf("provider %q cannot translate speech requests yet", p.Name())}
	}
	if err != nil {
		var planErr *planError
		if errors.As(err, &planErr) {
			return tts.Plan{}, name, err
		}
		return tts.Plan{}, name, &planError{Status: http.StatusBadRequest, Message: err.Error()}
	}
	plan.Adjust(adjustment)
	plan.Provider = p.Name()
	return plan, p.Name(), nil
}

// anyKeys reports whether any provider has a key. It separates "nothing set up
// yet" (503 with setup instructions) from "this provider is missing a key"
// (424 naming the provider).
func (s *Server) anyKeys(ctx context.Context) bool {
	stats, err := s.store.CountKeys(ctx)
	if err != nil {
		return true
	}
	return stats.Total > 0
}

// isPlaceholderVoice recognises the OpenAI voice names clients keep sending
// ("alloy", "nova") even though no provider here has them. Treating them as
// unset is what lets a client written for OpenAI keep working.
func isPlaceholderVoice(voice string) bool {
	switch strings.ToLower(strings.TrimSpace(voice)) {
	case "alloy", "echo", "fable", "onyx", "nova", "shimmer", "default", "alloy-2":
		return true
	}
	return false
}

// defaultsFor returns the configured fallback voice for a provider.
func (s *Server) defaultsFor(ctx context.Context, p provider.Provider, req tts.SpeechRequest) ttsDefaults {
	voice, err := s.store.DefaultVoice(ctx, p.Name())
	if err != nil {
		s.logError("read default voice", err)
	}
	return ttsDefaults{Voice: voice}
}

// ttsDefaults is the per-request fallback set the gateway hands a provider.
type ttsDefaults struct {
	Voice string
}

func (s *Server) writePlanError(w http.ResponseWriter, err error) {
	var planErr *planError
	if errors.As(err, &planErr) {
		writeError(w, planErr.Status, planErr.Message)
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

// synthesize runs the rotation loop: try a key of the plan's provider, park it
// if the failure is about the key, otherwise hand the answer straight back.
func (s *Server) synthesize(w http.ResponseWriter, r *http.Request, plan tts.Plan, envelope bool) {
	start := time.Now()
	chars := int64(len([]rune(plan.Text)))
	var (
		lastErrText   string
		lastErrStatus int
		lastRetryIn   time.Duration
		usedKeyID     int64
	)

	p, _ := s.providers.Get(plan.Provider)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		key, cooling, err := s.store.PickKeyFor(r.Context(), plan.Provider)
		if err != nil {
			s.finish(r.Context(), plan, 0, chars, 0, http.StatusServiceUnavailable, start, err.Error())
			if errors.Is(err, store.ErrNoKeys) {
				writeError(w, http.StatusServiceUnavailable,
					"no active "+providerLabel(s.providers, plan.Provider)+" API key: open the dashboard and add one")
				return
			}
			s.logError("pick key", err)
			writeError(w, http.StatusInternalServerError, "key selection failed")
			return
		}
		usedKeyID = key.ID
		if cooling {
			s.logger.Warn("trying a key that is still cooling down",
				"provider", plan.Provider, "keyId", key.ID, "label", key.Label)
		}

		resp, err := p.Synthesize(r.Context(), key.Secret, plan)
		if err != nil {
			// A transport failure says nothing about the key's validity, but
			// the next attempt should not reuse the same one immediately.
			_ = s.store.MarkFailure(r.Context(), key.ID, 0, err.Error(), 0)
			lastErrStatus, lastErrText = http.StatusBadGateway, err.Error()
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			_ = s.store.MarkSuccess(r.Context(), key.ID)
			s.logger.Info("synthesis succeeded", "provider", plan.Provider, "keyId", key.ID, "model", plan.Model)
			s.writeAudio(w, r, resp, plan, envelope, usedKeyID, chars, start)
			return
		}

		rawMessage, retryIn := readUpstream(resp)
		message, retryable := p.Classify(resp.StatusCode, rawMessage)
		if message == "" {
			message = resp.Status
		}
		lastErrStatus, lastErrText, lastRetryIn = resp.StatusCode, message, retryIn
		s.logger.Warn(plan.Provider+" rejected the request",
			"status", resp.StatusCode, "keyId", key.ID, "model", plan.Model, "error", message)

		if !retryable {
			// The payload or the voice is wrong; another key cannot fix it.
			s.finish(r.Context(), plan, usedKeyID, chars, 0, resp.StatusCode, start, message)
			writeError(w, resp.StatusCode, message)
			return
		}
		_ = s.store.MarkFailure(r.Context(), key.ID, resp.StatusCode, message, retryIn)
	}

	status := clientStatus(lastErrStatus)
	s.finish(r.Context(), plan, usedKeyID, chars, 0, status, start, lastErrText)
	if lastRetryIn > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(lastRetryIn.Seconds())+1))
	}
	writeError(w, status, "all configured keys failed: "+lastErrText)
}

func providerLabel(registry *provider.Registry, name string) string {
	if p, ok := registry.Get(name); ok {
		return p.Label()
	}
	return name
}

// writeAudio streams a successful upstream response to the client.
func (s *Server) writeAudio(w http.ResponseWriter, r *http.Request, resp *http.Response, plan tts.Plan, envelope bool, keyID, chars int64, start time.Time) {
	defer resp.Body.Close()

	if envelope {
		audio, err := io.ReadAll(io.LimitReader(resp.Body, maxAudioBytes))
		if err != nil {
			s.finish(r.Context(), plan, keyID, chars, 0, http.StatusBadGateway, start, err.Error())
			writeError(w, http.StatusBadGateway, "failed to read audio from "+plan.Provider)
			return
		}
		payload := base64.StdEncoding.EncodeToString(audio)
		s.finish(r.Context(), plan, keyID, chars, int64(len(audio)), http.StatusOK, start, "")
		writeJSON(w, http.StatusOK, map[string]any{"audio": payload, "format": plan.Extension})
		return
	}

	// Headers go out before the audio body; the net/http server writes the
	// status on the first Write, so nothing may be set after that point.
	if resp.ContentLength > 0 {
		w.Header().Set("X-Audio-Bytes", strconv.FormatInt(resp.ContentLength, 10))
	}
	w.Header().Set("Content-Type", plan.MIME)
	// No Content-Length on purpose: the body is forwarded as it arrives, so
	// the client can start playing before the provider has finished generating.
	w.WriteHeader(http.StatusOK)
	counter := &countingWriter{w: w}
	_, err := io.CopyBuffer(counter, resp.Body, make([]byte, streamBufSize))
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	s.finish(r.Context(), plan, keyID, chars, counter.n, http.StatusOK, start, errText)
}

// finish records the attempt. It runs with a detached context because a client
// that hung up still deserves the row: the request was made and it was billed.
// The client id is read from the request context by clientIDFrom, so the row
// is attributed even though the write happens after the response.
func (s *Server) finish(ctx context.Context, plan tts.Plan, keyID, chars, bytes int64, status int, start time.Time, errText string) {
	if len(errText) > 300 {
		errText = errText[:300]
	}
	row := store.RequestRow{
		TS:          time.Now().Unix(),
		Provider:    plan.Provider,
		Model:       plan.Model,
		VoiceID:     plan.Voice,
		Chars:       chars,
		Bytes:       bytes,
		KeyID:       keyID,
		ClientID:    clientIDFromContext(ctx),
		Status:      status,
		LatencyMS:   time.Since(start).Milliseconds(),
		Error:       errText,
		Adjustments: plan.Adjustments,
	}
	_ = s.store.InsertRequest(context.WithoutCancel(ctx), row)
}

// wantJSONEnvelope reads the 9router-compatible query flag. The body's
// response_format chooses the container; this chooses the envelope.
func wantJSONEnvelope(r *http.Request) bool {
	value := strings.ToLower(r.URL.Query().Get("response_format"))
	return value == "json"
}

// readUpstream reads an error body and any Retry-After hint.
func readUpstream(resp *http.Response) (string, time.Duration) {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	message := strings.TrimSpace(string(raw))
	if message == "" {
		message = resp.Status
	}
	return message, parseRetryAfter(resp.Header.Get("Retry-After"))
}

func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		return time.Until(when)
	}
	return 0
}

// clientStatus maps an upstream status onto what this client should be told.
// A rejected upstream credential is not a client credential problem, so it
// becomes 502 rather than 401.
func clientStatus(upstream int) int {
	switch upstream {
	case http.StatusPaymentRequired, http.StatusTooManyRequests:
		return upstream
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		return upstream
	default:
		return http.StatusBadGateway
	}
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
