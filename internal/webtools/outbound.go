package webtools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxSearchResults         = 5
	maxResponseBytes         = 1 << 20
	maxFetchChars            = 20_000
	maxRedirects             = 5
	redirectBodyCapBytes     = 4096
	requestTimeout           = 15 * time.Second
	defaultOutboundUserAgent = "Mozilla/5.0 (compatible; RelayCodeWebTools/1.0)"
)

type FetchResult struct {
	URL       string
	Title     string
	MediaType string
	Data      string
}

var httpClient = &http.Client{Timeout: requestTimeout}

func RunWebSearch(ctx context.Context, query string) ([]SearchResult, error) {
	endpoint, _ := url.Parse("https://lite.duckduckgo.com/lite/")
	params := endpoint.Query()
	params.Set("q", query)
	endpoint.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	setWebToolHeaders(req)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("web_search upstream %d", resp.StatusCode)
	}
	body, err := readBodyCapped(resp.Body, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	return ParseSearchResults(string(body), maxSearchResults), nil
}

func RunWebFetch(ctx context.Context, rawURL string, policy EgressPolicy) (FetchResult, error) {
	current := rawURL
	for redirects := 0; ; redirects++ {
		parsed, err := policy.ValidateURL(current)
		if err != nil {
			return FetchResult{}, err
		}
		if err := policy.ValidateHost(parsed.Hostname()); err != nil {
			return FetchResult{}, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return FetchResult{}, err
		}
		setWebToolHeaders(req)
		client := *httpClient
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
		resp, err := client.Do(req)
		if err != nil {
			return FetchResult{}, err
		}
		if isRedirect(resp.StatusCode) {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, redirectBodyCapBytes))
			resp.Body.Close()
			if redirects >= maxRedirects {
				return FetchResult{}, EgressViolation{Message: "web_fetch exceeded maximum redirects"}
			}
			location := strings.TrimSpace(resp.Header.Get("Location"))
			if location == "" {
				return FetchResult{}, EgressViolation{Message: "web_fetch redirect response missing Location header"}
			}
			next, err := parsed.Parse(location)
			if err != nil {
				return FetchResult{}, EgressViolation{Message: "web_fetch redirect URL is invalid"}
			}
			current = next.String()
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return FetchResult{}, fmt.Errorf("web_fetch upstream %d", resp.StatusCode)
		}
		body, err := readBodyCapped(resp.Body, maxResponseBytes)
		if err != nil {
			return FetchResult{}, err
		}
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "text/plain"
		}
		finalURL := resp.Request.URL.String()
		data := string(body)
		title := finalURL
		if strings.Contains(strings.ToLower(contentType), "html") {
			title, data = ParseHTMLText(data, finalURL, maxFetchChars)
		} else if len(data) > maxFetchChars {
			data = data[:maxFetchChars]
		}
		return FetchResult{URL: finalURL, Title: title, MediaType: "text/plain", Data: data}, nil
	}
}

func readBodyCapped(r io.Reader, maxBytes int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, maxBytes))
}

func setWebToolHeaders(req *http.Request) {
	req.Header.Set("User-Agent", defaultOutboundUserAgent)
	req.Header.Set("Accept", "text/html,text/plain,*/*;q=0.8")
}

func isRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}
