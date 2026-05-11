package webtools

import "testing"

func TestExtractQuery(t *testing.T) {
	if got := ExtractQuery("Perform a web search for the query: DeepSeek V4"); got != "DeepSeek V4" {
		t.Fatalf("ExtractQuery = %q", got)
	}
}

func TestExtractURL(t *testing.T) {
	if got := ExtractURL("fetch https://example.com/page)."); got != "https://example.com/page" {
		t.Fatalf("ExtractURL = %q", got)
	}
}

func TestParseSearchResults(t *testing.T) {
	body := `<a rel="nofollow" href="/l/?kh=-1&uddg=https%3A%2F%2Fexample.com%2Fv4">DeepSeek <b>V4</b></a>`
	results := ParseSearchResults(body, 5)
	if len(results) != 1 || results[0].Title != "DeepSeek V4" || results[0].URL != "https://example.com/v4" {
		t.Fatalf("results = %+v", results)
	}
}

func TestParseHTMLText(t *testing.T) {
	title, text := ParseHTMLText(`<html><title>T</title><script>x</script><body>Hello <b>world</b></body></html>`, "fallback", 100)
	if title != "T" || text != "T Hello world" {
		t.Fatalf("title=%q text=%q", title, text)
	}
}
