package admin

import (
	"encoding/json"
	"testing"
)

func TestWebReadResultRequiresBoundedBodyCoverage(t *testing.T) {
	const digest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	args := json.RawMessage(`{"url":"https://example.test/page","offset":2,"limit":3,"expected_sha256":"` + digest + `","max_bytes":196608,"timeout_seconds":45}`)
	result := serviceWebResult{
		OK: true, Untrusted: true, URL: "https://example.test/page", Text: "鱼🐳文",
		SHA:    digest,
		Offset: 2, RequestedLimit: 3, ReturnedCharacters: 3, TotalCharacters: 6,
		NextOffset: func() *int { n := 5; return &n }(), Complete: false,
		ReadCoverage: &serviceWebReadCoverage{Kind: "page_body", StartOffset: 2, EndOffset: 5, TotalCharacters: 6, Complete: false, Excerpt: "鱼🐳文"},
	}
	if err := validateWebReadResult(args, result, map[string]bool{result.URL: true}); err != nil {
		t.Fatal(err)
	}
	redirected := result
	redirected.URL = "https://example.test/final"
	if err := validateWebReadResult(args, redirected, map[string]bool{redirected.URL: true}); err != nil {
		t.Fatalf("observed public redirect rejected: %v", err)
	}
	if err := validateWebReadResult(json.RawMessage(`{"url":"https://example.test/page","offset":2,"limit":3,"expected_sha256":""}`), result, map[string]bool{result.URL: true}); err != nil {
		t.Fatalf("empty optional checksum rejected: %v", err)
	}
	for name, mutate := range map[string]func(*serviceWebResult){
		"title-only":       func(r *serviceWebResult) { r.ReadCoverage.Kind = "title_only" },
		"missing":          func(r *serviceWebResult) { r.ReadCoverage = nil },
		"invented-range":   func(r *serviceWebResult) { r.ReadCoverage.EndOffset++ },
		"invented-excerpt": func(r *serviceWebResult) { r.ReadCoverage.Excerpt = "标题不是正文" },
		"short-page": func(r *serviceWebResult) {
			r.Text, r.ReturnedCharacters, r.ReadCoverage.Excerpt = "鱼🐳", 2, "鱼🐳"
		},
		"page-beyond-total": func(r *serviceWebResult) {
			r.TotalCharacters, r.ReadCoverage.TotalCharacters = 4, 4
		},
		"checksum-mismatch": func(r *serviceWebResult) { r.SHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := result
			coverage := *result.ReadCoverage
			copy.ReadCoverage = &coverage
			mutate(&copy)
			if err := validateWebReadResult(args, copy, map[string]bool{result.URL: true}); err == nil {
				t.Fatal("unverified body coverage accepted")
			}
		})
	}
}

func TestWebReadCoverageUpgradeKeepsLegacyResultReadableButUnproven(t *testing.T) {
	if webReadCoverageRequired("0.3.3") || webReadCoverageRequired("0.3.3-legacy") || !webReadCoverageRequired("0.3.4") || !webReadCoverageRequired("invalid") {
		t.Fatal("unexpected web read coverage version boundary")
	}
	legacy := serviceWebResult{OK: true, Untrusted: true, URL: "https://example.test/page", Text: "old verified result", SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	legacyArgs := json.RawMessage(`{"url":"https://example.test/page","offset":0,"limit":10000,"expected_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","max_bytes":196608,"timeout_seconds":45}`)
	if err := validateLegacyWebReadResult(legacyArgs, legacy, map[string]bool{legacy.URL: true}); err != nil {
		t.Fatal(err)
	}
	legacy.MetadataOnly = true
	if err := validateLegacyWebReadResult(legacyArgs, legacy, map[string]bool{legacy.URL: true}); err == nil {
		t.Fatal("metadata-only legacy result became a reading")
	}
}
