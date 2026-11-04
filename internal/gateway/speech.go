package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/cartesia"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

const (
	maxSpeechBody = 1 << 20 // 1 MiB of JSON is already a very long transcript
	maxAudioBytes = 64 << 20
	maxAttempts   = 4
	streamBufSize = 32 << 10
)

// handleSpeech is the OpenAI-compatible endpoint. The body is translated into
// Cartesia's schema; the query parameter ?response_format=json selects the
// base64 envelope the 9router clients expect.
func (s *Server) handleSpeech(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSpeechBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	var req cartesia.SpeechRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	translation, err := cartesia.Translate(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	envelope := wantJSONEnvelope(r)
	s.synthesize(w, r, translation, envelope)
}

// synthesize runs the rotation loop: try a key, park it if the failure is
// about the key, otherwise hand the answer straight back.
func (s *Server) synthesize(w http.ResponseWriter, r *http.Request, t cartesia.Translation, envelope bool) {
	start := time.Now()
	chars := int64(len([]rune(t.Request.Transcript)))
	var (
		lastErrText   string
		lastErrStatus int
		lastRetryIn   time.Duration
		usedKeyID     int64
	)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		key, cooling, err := s.store.PickKey(r.Context())
		if err != nil {
			s.finish(r.Context(), t, 0, chars, 0, http.StatusServiceUnavailable, start, err.Error())
			if errors.Is(err, store.ErrNoKeys) {
				writeError(w, http.StatusServiceUnavailable,
					"no active Cartesia API key: open the dashboard and add one")
				return
			}
			s.logError("pick key", err)
			writeError(w, http.StatusInternalServerError, "key selection failed")
			return
		}
		usedKeyID = key.ID
		if cooling {
			s.logger.Warn("trying a key that is still cooling down", "keyId", key.ID, "label", key.Label)
		}

		resp, err := s.cart.Synthesize(r.Context(), key.Secret, t.Request)
		if err != nil {
			// A transport failure says nothing about the key's validity, but
			// the next attempt should not reuse the same one immediately.
			_ = s.store.MarkFailure(r.Context(), key.ID, 0, err.Error(), 0)
			lastErrStatus, lastErrText = http.StatusBadGateway, err.Error()
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			_ = s.store.MarkSuccess(r.Context(), key.ID)
			s.writeAudio(w, r, resp, t, envelope, usedKeyID, chars, start)
			return
		}

		message, retryIn := drainUpstream(resp)
		lastErrStatus, lastErrText, lastRetryIn = resp.StatusCode, message, retryIn
		s.logger.Warn("cartesia rejected the request",
			"status", resp.StatusCode, "keyId", key.ID, "model", t.Request.ModelID, "error", message)

		if !retryable(resp.StatusCode) {
			// The payload or the voice is wrong; another key cannot fix it.
			s.finish(r.Context(), t, usedKeyID, chars, 0, resp.StatusCode, start, message)
			writeError(w, resp.StatusCode, message)
			return
		}
		_ = s.store.MarkFailure(r.Context(), key.ID, resp.StatusCode, message, retryIn)
	}

	status := clientStatus(lastErrStatus)
	s.finish(r.Context(), t, usedKeyID, chars, 0, status, start, lastErrText)
	if lastRetryIn > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(lastRetryIn.Seconds())+1))
	}
	writeError(w, status, "all configured keys failed: "+lastErrText)
}

// writeAudio streams a successful upstream response to the client.
func (s *Server) writeAudio(w http.ResponseWriter, r *http.Request, resp *http.Response, t cartesia.Translation, envelope bool, keyID, chars int64, start time.Time) {
	defer resp.Body.Close()

	if envelope {
		audio, err := io.ReadAll(io.LimitReader(resp.Body, maxAudioBytes))
		if err != nil {
			s.finish(r.Context(), t, keyID, chars, 0, http.StatusBadGateway, start, err.Error())
			writeError(w, http.StatusBadGateway, "failed to read audio from cartesia")
			return
		}
		payload := base64.StdEncoding.EncodeToString(audio)
		s.finish(r.Context(), t, keyID, chars, int64(len(audio)), http.StatusOK, start, "")
		writeJSON(w, http.StatusOK, map[string]any{"audio": payload, "format": t.Extension})
		return
	}

	w.Header().Set("Content-Type", t.MIME)
	// No Content-Length on purpose: the body is forwarded as it arrives, so
	// the client can start playing before Cartesia has finished generating.
	w.WriteHeader(http.StatusOK)
	counter := &countingWriter{w: w}
	_, err := io.CopyBuffer(counter, resp.Body, make([]byte, streamBufSize))
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	s.finish(r.Context(), t, keyID, chars, counter.n, http.StatusOK, start, errText)
}

// finish records the attempt. It runs with a detached context because a client
// that hung up still deserves the row: the request was made and it was billed.
func (s *Server) finish(ctx context.Context, t cartesia.Translation, keyID, chars, bytes int64, status int, start time.Time, errText string) {
	if len(errText) > 300 {
		errText = errText[:300]
	}
	row := store.RequestRow{
		TS:        time.Now().Unix(),
		Model:     t.Request.ModelID,
		VoiceID:   t.Request.Voice,
		Chars:     chars,
		Bytes:     bytes,
		KeyID:     keyID,
		Status:    status,
		LatencyMS: time.Since(start).Milliseconds(),
		Error:     errText,
	}
	_ = s.store.InsertRequest(context.WithoutCancel(ctx), row)
}

// wantJSONEnvelope reads the 9router-compatible query flag. The body's
// response_format chooses the container; this chooses the envelope.
func wantJSONEnvelope(r *http.Request) bool {
	value := strings.ToLower(r.URL.Query().Get("response_format"))
	return value == "json"
}

// drainUpstream reads an error body and any Retry-After hint.
func drainUpstream(resp *http.Response) (string, time.Duration) {
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

// retryable reports whether a status means "try this on another key".
func retryable(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden,
		http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return status >= 500
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
