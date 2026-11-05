package companion

import (
	"strings"
	"testing"
)

func TestSourceTextClausesExcludeQuotedAndCodeClaimsWithExactOffsets(t *testing.T) {
	text := "🙂她说“语音上传时注明了「这是本人」”，但转写里只提到录音。\n> 上传时注明了不应扫描\n```text\n上传时注明了不应扫描\n```\n    上传时注明了这是一段缩进代码\n`上传时注明了` 与 ``引用含`字符`` 都不算。最后一句可以。"
	clauses := sourceTextClauses(text)
	for _, c := range clauses {
		if c.Text != string([]rune(text)[c.RuneStart:c.RuneEnd]) || c.Text != text[c.ByteStart:c.ByteEnd] {
			t.Fatal("offsets no longer refer to original source", c)
		}
		if strings.Contains(c.Text, "上传时注明") || strings.Contains(c.Text, "引用含") {
			t.Fatal("opaque quote/code became an assertion", c)
		}
	}
	if len(clauses) == 0 || clauses[len(clauses)-1].Text != "最后一句可以" {
		t.Fatal(clauses)
	}
}

func TestSourceTextClausesKeepLocalConditionNegationAndQuestionScope(t *testing.T) {
	text := "如果上传时注明了，那可以引用。没有附言，但上传时注明了来源。上传时注明了吗？他说‘上传时注明了’，这只是引语。it's an ordinary sentence."
	clauses := sourceTextClauses(text)
	byText := map[string]SourceTextClause{}
	for _, c := range clauses {
		byText[c.Text] = c
	}
	if !byText["如果上传时注明了"].Conditional || !byText["那可以引用"].Conditional || byText["没有附言"].Conditional {
		t.Fatal("condition leaked across sentence", clauses)
	}
	if !byText["没有附言"].Negated || byText["但上传时注明了来源"].Negated {
		t.Fatal("negation leaked to another clause", clauses)
	}
	if !byText["上传时注明了吗"].Question {
		t.Fatal("question became asserted caption", clauses)
	}
	if _, ok := byText["it's an ordinary sentence"]; !ok {
		t.Fatal("apostrophe swallowed ordinary text", clauses)
	}
}

func TestSourceTextClausesDoNotJoinAroundOpaqueQuotes(t *testing.T) {
	clauses := sourceTextClauses("上传时‘这是引用’注明了。\"上传时注明了\"不代表存在附言。")
	for _, c := range clauses {
		if strings.Contains(c.Text, "上传时") && strings.Contains(c.Text, "注明了") {
			t.Fatal("disjoint fragments were joined into a new claim", clauses)
		}
	}
}
