package provider

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/relaycode/relaycode/internal/config"
)

// PostStream issues a POST with the given JSON body, returns an SSE line reader.
// Caller is responsible for closing the returned io.Closer.
func PostStream(ctx context.Context, baseURL, path, apiKey, authHeader string, body []byte) (*bufio.Reader, io.Closer, error) {
	return PostStreamWithHeaders(ctx, baseURL, path, apiKey, authHeader, nil, body)
}

func PostStreamWithHeaders(ctx context.Context, baseURL, path, apiKey, authHeader string, extraHeaders map[string]string, body []byte) (*bufio.Reader, io.Closer, error) {
	return PostStreamWithClient(ctx, http.DefaultClient, 0, baseURL, path, apiKey, authHeader, extraHeaders, body)
}

func PostStreamWithClient(ctx context.Context, client *http.Client, maxRetries int, baseURL, path, apiKey, authHeader string, extraHeaders map[string]string, body []byte) (*bufio.Reader, io.Closer, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(baseURL, "/")+path, strings.NewReader(string(body)))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if apiKey != "" {
		if authHeader == "" {
			authHeader = "Authorization"
		}
		if strings.EqualFold(authHeader, "Authorization") {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		} else {
			req.Header.Set(authHeader, apiKey)
		}
	}
	for name, value := range extraHeaders {
		if strings.TrimSpace(value) != "" {
			req.Header.Set(name, value)
		}
	}
	if client == nil {
		client = http.DefaultClient
	}
	attempts := maxRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		req2 := req.Clone(ctx)
		req2.Body = io.NopCloser(strings.NewReader(string(body)))
		resp, err := client.Do(req2)
		if err != nil {
			lastErr = err
		} else if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			resp.Body.Close()
			lastErr = fmt.Errorf("upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
		} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			resp.Body.Close()
			return nil, nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
		} else {
			return bufio.NewReaderSize(resp.Body, 1<<15), resp.Body, nil
		}
		if attempt+1 < attempts {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 250 * time.Millisecond):
			}
		}
	}
	return nil, nil, lastErr
}

func HTTPClient(pc config.ProviderConfig) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if pc.HTTPProxy != "" {
		proxyURL, err := url.Parse(pc.HTTPProxy)
		if err != nil {
			return nil, fmt.Errorf("http_proxy: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	timeout := 0 * time.Second
	if pc.HTTPTimeoutSeconds > 0 {
		timeout = time.Duration(pc.HTTPTimeoutSeconds) * time.Second
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

// SSEEvent is one parsed Server-Sent Event.
type SSEEvent struct {
	Event string
	Data  string
}

// IterSSE reads SSE events from r and invokes fn for each. fn may return
// ErrStopSSE to stop early.
func IterSSE(r *bufio.Reader, fn func(SSEEvent) error) error {
	var event string
	var data strings.Builder
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if data.Len() > 0 || event != "" {
					out := SSEEvent{Event: event, Data: data.String()}
					data.Reset()
					event = ""
					if err := fn(out); err != nil {
						if err == ErrStopSSE {
							return nil
						}
						return err
					}
				}
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(line[len("event:"):])
			case strings.HasPrefix(line, "data:"):
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(strings.TrimSpace(line[len("data:"):]))
			case strings.HasPrefix(line, ":"):
				// comment, ignore
			}
		}
		if err != nil {
			if err == io.EOF {
				if data.Len() > 0 {
					fn(SSEEvent{Event: event, Data: data.String()})
				}
				return nil
			}
			return err
		}
	}
}

// ErrStopSSE is returned by the IterSSE callback to stop iteration cleanly.
var ErrStopSSE = stopSSE{}

type stopSSE struct{}

func (stopSSE) Error() string { return "sse: stop" }
