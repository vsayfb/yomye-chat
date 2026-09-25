package attachment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/vsayfb/gig-platform-chat-service/pkg/objectstorage/r2"
)

var ErrMediaRejected = errors.New("media validation rejected the attachment")
var ErrProcessorUnavailable = errors.New("media validator unavailable")

type ProcessedMedia struct {
	MIME           string `json:"mime"`
	SizeBytes      int64  `json:"size_bytes"`
	DurationMillis int64  `json:"duration_ms"`
}

type Processor interface {
	Process(context.Context, string, *r2.UploadAuthorization, string, int64, int64) (*ProcessedMedia, error)
}

type unixProcessor struct{ client *http.Client }

func NewUnixProcessor(socket string) Processor {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &unixProcessor{client: &http.Client{Transport: transport, Timeout: 3 * time.Minute}}
}

func (p *unixProcessor) Process(ctx context.Context, sourceURL string, destination *r2.UploadAuthorization, mime string, maxBytes, maxDurationMS int64) (*ProcessedMedia, error) {
	if destination == nil || destination.Method != http.MethodPut {
		return nil, ErrProcessorUnavailable
	}
	requestBody, err := json.Marshal(struct {
		SourceURL         string `json:"source_url"`
		ExpectedMIME      string `json:"expected_mime"`
		MaxBytes          int64  `json:"max_bytes"`
		MaxDurationMillis int64  `json:"max_duration_ms"`
		Destination       struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
		} `json:"destination"`
	}{SourceURL: sourceURL, ExpectedMIME: mime, MaxBytes: maxBytes,
		MaxDurationMillis: maxDurationMS, Destination: struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
		}{URL: destination.URL, Method: destination.Method, Headers: destination.Headers}})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://media-validator/v1/process", bytes.NewReader(requestBody))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		slog.WarnContext(ctx, "media validator request failed", "stage", "transport")
		return nil, ErrProcessorUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnprocessableEntity {
		return nil, ErrMediaRejected
	}
	if response.StatusCode != http.StatusOK {
		// The validator's response body is a fixed, non-sensitive reason. Never log
		// the request or its signed R2 URLs.
		reason, _ := io.ReadAll(io.LimitReader(response.Body, 128))
		slog.WarnContext(ctx, "media validator rejected request", "validator_status", response.StatusCode,
			"validator_reason", string(bytes.TrimSpace(reason)))
		return nil, ErrProcessorUnavailable
	}
	var body struct {
		Approved bool `json:"approved"`
		ProcessedMedia
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&body); err != nil || !body.Approved || body.MIME == "" || body.SizeBytes <= 0 {
		slog.WarnContext(ctx, "media validator returned invalid approval")
		return nil, ErrProcessorUnavailable
	}
	return &body.ProcessedMedia, nil
}
