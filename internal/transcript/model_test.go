package transcript

import (
	"context"
	"os"
	"testing"
)

func TestPreserveWords(t *testing.T) {
	for _, tc := range []struct {
		raw, formatted string
		ok             bool
	}{
		{"今天晴天要出门吗", "今天晴天，要出门吗？", true},
		{"价格3.14元", "价格3.14元。", true},
		{"明天回去", "今天回去。", false},
		{"new york", "newyork。", false},
		{"价格314元", "价格3.14元。", false},
	} {
		_, ok := preserveWords(tc.raw, tc.formatted)
		if ok != tc.ok {
			t.Errorf("%q => %q: accepted=%v", tc.raw, tc.formatted, ok)
		}
	}
}
func TestOriginalPunctuationWins(t *testing.T) {
	text, ok := preserveWords("妈妈，我到了", "妈妈，，我到了。")
	if !ok || text != "妈妈，我到了。" {
		t.Fatal(text)
	}
}

func TestInstalledPunctuation(t *testing.T) {
	root := os.Getenv("WECHAT_TEST_PUNCTUATION_DIR")
	if root == "" {
		t.Skip("optional offline model not requested")
	}
	raw := "你今天回家吗我已经到了我们晚上见"
	text, ok := formatModel(context.Background(), raw, root)
	if !ok || text == raw {
		t.Fatalf("offline punctuation failed: %q", text)
	}
}
