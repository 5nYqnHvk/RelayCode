package webtools

import (
	"html"
	"net/url"
	"regexp"
	"strings"

	"github.com/relaycode/relaycode/internal/anthropic"
)

type SearchResult struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

func ContentText(content anthropic.Content) string {
	blocks := content.AsBlocks()
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

var queryPattern = regexp.MustCompile(`(?is)query:\s*(.+)`)

func ExtractQuery(text string) string {
	match := queryPattern.FindStringSubmatch(text)
	if len(match) == 2 {
		return strings.Trim(strings.TrimSpace(match[1]), `"'`)
	}
	return strings.TrimSpace(text)
}

var urlPattern = regexp.MustCompile(`https?://\S+`)

func ExtractURL(text string) string {
	match := urlPattern.FindString(text)
	if match == "" {
		return strings.TrimSpace(text)
	}
	return strings.TrimRight(match, ").,]")
}

var anchorPattern = regexp.MustCompile(`(?is)<a\s+[^>]*href=["']([^"']+)["'][^>]*>(.*?)</a>`)
var tagPattern = regexp.MustCompile(`(?is)<[^>]+>`)

func ParseSearchResults(body string, maxResults int) []SearchResult {
	matches := anchorPattern.FindAllStringSubmatch(body, -1)
	results := make([]SearchResult, 0, maxResults)
	seen := map[string]bool{}
	for _, match := range matches {
		if len(match) != 3 || !strings.Contains(match[1], "uddg=") {
			continue
		}
		parsed, err := url.Parse(html.UnescapeString(match[1]))
		if err != nil {
			continue
		}
		uddg := parsed.Query().Get("uddg")
		if uddg == "" || seen[uddg] {
			continue
		}
		title := strings.Join(strings.Fields(html.UnescapeString(tagPattern.ReplaceAllString(match[2], " "))), " ")
		if title == "" {
			continue
		}
		seen[uddg] = true
		results = append(results, SearchResult{Title: title, URL: uddg})
		if len(results) >= maxResults {
			break
		}
	}
	return results
}

var titlePattern = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
var scriptBlockPattern = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
var styleBlockPattern = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
var noscriptBlockPattern = regexp.MustCompile(`(?is)<noscript[^>]*>.*?</noscript>`)

func ParseHTMLText(body, fallbackTitle string, maxChars int) (string, string) {
	title := fallbackTitle
	if match := titlePattern.FindStringSubmatch(body); len(match) == 2 {
		candidate := strings.Join(strings.Fields(html.UnescapeString(tagPattern.ReplaceAllString(match[1], " "))), " ")
		if candidate != "" {
			title = candidate
		}
	}
	cleaned := scriptBlockPattern.ReplaceAllString(body, " ")
	cleaned = styleBlockPattern.ReplaceAllString(cleaned, " ")
	cleaned = noscriptBlockPattern.ReplaceAllString(cleaned, " ")
	cleaned = tagPattern.ReplaceAllString(cleaned, " ")
	text := strings.Join(strings.Fields(html.UnescapeString(cleaned)), " ")
	if maxChars > 0 && len(text) > maxChars {
		text = text[:maxChars]
	}
	return title, text
}
