package webtools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"github.com/relaycode/relaycode/internal/anthropic"
	"github.com/relaycode/relaycode/internal/sse"
)

type SearchRunner func(context.Context, string) ([]SearchResult, error)
type FetchRunner func(context.Context, string, EgressPolicy) (FetchResult, error)

type Runner struct {
	Search SearchRunner
	Fetch  FetchRunner
}

func DefaultRunner() Runner {
	return Runner{Search: RunWebSearch, Fetch: RunWebFetch}
}

func StreamWebServerToolResponse(ctx context.Context, req *anthropic.Request, b *sse.Builder, policy EgressPolicy, runner Runner) {
	if runner.Search == nil {
		runner.Search = RunWebSearch
	}
	if runner.Fetch == nil {
		runner.Fetch = RunWebFetch
	}
	toolName := ForcedServerToolName(req)
	if toolName == "" || !HasToolNamed(req, toolName) {
		return
	}
	text := ForcedToolTurnText(req)
	toolID := "srvtoolu_" + randomHex(16)
	usageKey := "web_search_requests"
	input := map[string]any{"query": ExtractQuery(text)}
	resultType := "web_search_tool_result"

	if toolName == "web_fetch" {
		usageKey = "web_fetch_requests"
		input = map[string]any{"url": ExtractURL(text)}
		resultType = "web_fetch_tool_result"
	}

	b.Start()
	b.StartServerToolWithInput(toolID, toolName, input)
	b.StopTool(toolID)

	var summary string
	var content any
	if toolName == "web_search" {
		query := fmt.Sprint(input["query"])
		results, err := runner.Search(ctx, query)
		if err != nil {
			log.Printf("web_tool_failure tool=web_search exc_type=%T", err)
			content = map[string]any{"type": "web_search_tool_result_error", "error_code": "unavailable"}
			summary = "Web tool request failed."
		} else {
			items := make([]map[string]any, 0, len(results))
			for _, result := range results {
				items = append(items, map[string]any{"type": "web_search_result", "title": result.Title, "url": result.URL})
			}
			content = items
			summary = searchSummary(query, results)
		}
	} else {
		fetchURL := fmt.Sprint(input["url"])
		result, err := runner.Fetch(ctx, fetchURL, policy)
		if err != nil {
			log.Printf("web_tool_failure tool=web_fetch exc_type=%T", err)
			content = map[string]any{"type": "web_fetch_tool_error", "error_code": "unavailable"}
			summary = "Web tool request failed."
		} else {
			content = map[string]any{
				"type": "web_fetch_result",
				"url":  result.URL,
				"content": map[string]any{
					"type": "document",
					"source": map[string]any{
						"type":       "text",
						"media_type": result.MediaType,
						"data":       result.Data,
					},
					"title":     result.Title,
					"citations": map[string]any{"enabled": true},
				},
				"retrieved_at": time.Now().UTC().Format(time.RFC3339),
			}
			summary = result.Data
		}
	}
	b.EmitServerToolResult(resultType, toolID, content)
	b.EmitText(summary)
	b.SetOutputTokens(sse.EstimateOutputTokens(summary))
	b.AddServerToolUse(usageKey, 1)
	b.Finish()
}

func searchSummary(query string, results []SearchResult) string {
	if len(results) == 0 {
		return "No web search results found for: " + query
	}
	out := "Search results for: " + query
	for i, result := range results {
		out += fmt.Sprintf("\n\n%d. %s\n%s", i+1, result.Title, result.URL)
	}
	return out
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
