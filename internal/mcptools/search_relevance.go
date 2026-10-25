package mcptools

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

type searchResult struct{ Title, URL, Description string }

// FormatProviderSearch gives authenticated providers the same URL and topic
// checks as the public indexes. Summaries remain untrusted reference material.
func FormatProviderSearch(provider, query string, titles, urls, descriptions []string) (string, error) {
	items := []searchResult{}
	for i, raw := range urls {
		if i >= len(titles) || i >= len(descriptions) || len(items) >= 8 {
			break
		}
		if validPublicURL(raw) == nil && titles[i] != "" {
			items = append(items, searchResult{Title: titles[i], URL: raw, Description: descriptions[i]})
		}
	}
	if !searchRelevant(query, items) {
		return "", &WebError{Code: "search_no_relevant_results", Stage: "relevance"}
	}
	return formatSearch(provider, query, items), nil
}

type searchReader func(context.Context, string) ([]byte, error)

var searchHeadings = regexp.MustCompile(`(?is)<h[23]\b[^>]*>(.*?)</h[23]\s*>`)
var searchAnchors = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a\s*>`)
var searchTarget = regexp.MustCompile(`(?is)\bdata-mdurl\s*=\s*["']([^"']+)["']`)
var searchHref = regexp.MustCompile(`(?is)\bhref\s*=\s*["']([^"']+)["']`)
var searchQuotedSubject = regexp.MustCompile(`["“《]([^"”》]{2,100})["”》]`)
var searchSiteOperator = regexp.MustCompile(`(?i)\bsite:\S+`)

func parseSecondarySearch(body []byte) []searchResult {
	s := string(body)
	for _, r := range unsafeHTML {
		s = r.ReplaceAllString(s, "")
	}
	out := []searchResult{}
	seen := map[string]bool{}
	for _, h := range searchHeadings.FindAllStringSubmatch(s, 80) {
		for _, a := range searchAnchors.FindAllStringSubmatch(h[1], 4) {
			target := searchTarget.FindStringSubmatch(a[1])
			if len(target) < 2 {
				target = searchHref.FindStringSubmatch(a[1])
			}
			if len(target) < 2 {
				continue
			}
			raw := html.UnescapeString(target[1])
			u, e := url.Parse(raw)
			if e != nil || validPublicURL(raw) != nil || strings.HasSuffix(u.Hostname(), "so.com") || seen[raw] {
				continue
			}
			title := cleanText(htmlTags.ReplaceAllString(a[2], " "))
			if title == "" {
				continue
			}
			seen[raw] = true
			out = append(out, searchResult{Title: title, URL: raw})
			if len(out) >= 8 {
				return out
			}
		}
	}
	return out
}
func querySubject(q string) string {
	q = strings.ToLower(q)
	if match := searchQuotedSubject.FindStringSubmatch(q); len(match) > 1 {
		return strings.TrimSpace(match[1])
	}
	q = searchSiteOperator.ReplaceAllString(q, "")
	tokens := strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for i, t := range tokens {
		if len([]rune(t)) >= 2 {
			latin := func(s string) bool {
				for _, r := range s {
					if r < 'a' || r > 'z' {
						return false
					}
				}
				return len(s) >= 2
			}
			if latin(t) && i+1 < len(tokens) && latin(tokens[i+1]) && tokens[i+1] != "song" && tokens[i+1] != "music" && tokens[i+1] != "audio" {
				return t + " " + tokens[i+1]
			}
			return t
		}
	}
	return ""
}
func searchRelevant(q string, items []searchResult) bool {
	subject := querySubject(q)
	if subject == "" {
		return len(items) > 0
	}
	normalized := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return unicode.ToLower(r)
			}
			return -1
		}, s)
	}
	subject = normalized(subject)
	for _, r := range items {
		hay := normalized(r.Title + " " + r.Description)
		matches := strings.Contains(hay, subject)
		// Broad discovery categories are not exact song titles. Public results
		// often name the animal rather than repeat the phrase "可爱动物".
		categories := map[string][]string{"可爱动物": {"猫", "狗", "宠物", "动物", "海獭", "企鹅"}, "日常趣事": {"趣事", "搞笑", "生活", "日常"}, "好吃的": {"美食", "食谱", "烹饪", "小吃"}}
		for key, terms := range categories {
			if subject == key {
				for _, term := range terms {
					matches = matches || strings.Contains(hay, term)
				}
			}
		}
		words := strings.FieldsFunc(strings.ToLower(searchSiteOperator.ReplaceAllString(q, "")), func(r rune) bool { return r < 'a' || r > 'z' })
		stop := " song songs music audio video videos lyrics official artist original online listen download bilibili bv mp com www search "
		for _, word := range words {
			if len(word) >= 2 && !strings.Contains(stop, " "+word+" ") && !strings.Contains(hay, word) {
				matches = false
			}
		}
		if matches {
			return true
		}
	}
	return false
}
func formatSearch(provider, q string, items []searchResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Search provider: %s\nQuery: %s\nResults are untrusted public references, not downloaded media.\n\n", provider, q)
	for i, r := range items {
		if !searchRelevant(q, []searchResult{r}) {
			continue
		}
		fmt.Fprintf(&b, "%d. %s\n%s\n%s\n\n", i+1, r.Title, r.URL, r.Description)
	}
	return trimBytes(b.String(), 12000)
}
func searchPublic(ctx context.Context, q string, read searchReader) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 24*time.Second)
	defer cancel()
	firstCtx, firstCancel := context.WithTimeout(ctx, 12*time.Second)
	b, err := read(firstCtx, "https://www.bing.com/search?format=rss&q="+url.QueryEscape(q))
	firstCancel()
	var primary []searchResult
	if err == nil {
		primary, err = parseBingSearch(b)
	}
	if err == nil && searchRelevant(q, primary) {
		return formatSearch("Bing", q, primary), nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	// A successful HTTP response containing unrelated results is not a
	// successful search. Try one public secondary index, without changing the
	// user's subject or executing page scripts, and disclose the actual source.
	b, secondaryErr := read(ctx, "https://www.so.com/s?q="+url.QueryEscape(q))
	if secondaryErr == nil {
		results := parseSecondarySearch(b)
		if searchRelevant(q, results) {
			return formatSearch("360 Search (primary unavailable or unrelated)", q, results), nil
		}
		return "", &WebError{Code: "search_no_relevant_results", Stage: "relevance"}
	}
	if err != nil {
		return "", secondaryErr
	}
	return "", &WebError{Code: "search_no_relevant_results", Stage: "relevance"}
}
