package companion

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode"
	"wechat-bot/internal/identity"
)

// ValidateReplyText shares ordinary source/authorization validation with a
// finite output contract: an unquoted, explicit known member vocative must
// name a real target of this bubble. It never silently changes that target.
func (e *Engine) ValidateReplyText(ctx context.Context, in Input, r ReplyContext, text string) error {
	if err := e.ValidateReplyContext(ctx, in, r); err != nil {
		return err
	}
	if err := e.validateNamedAddress(in, r, text); err != nil {
		return err
	}
	return e.validateSourceReferenceText(ctx, in, r, text)
}

func (e *Engine) validateNamedAddress(in Input, r ReplyContext, text string) error {
	if !in.IsGroup || in.SourceOnlyContext || in.ConversationDelivery {
		return nil
	}
	owners := map[string]map[string]bool{}
	add := func(id, name string) {
		if id == "" {
			return
		}
		for _, label := range []string{id, identity.Clean(name)} {
			if label == "" {
				continue
			}
			if owners[label] == nil {
				owners[label] = map[string]bool{}
			}
			owners[label][id] = true
		}
	}
	add(in.Sender, in.SenderName)
	for _, id := range in.Addressees {
		add(id, "")
	}
	if in.QuotedSource != nil {
		add(in.QuotedSource.Sender, in.QuotedSource.SenderName)
	}
	for _, turn := range e.groundingScope(in).Messages {
		if turn.Role == "user" {
			add(turn.Sender, turn.SenderName)
		}
	}
	labels := []string{}
	for label, ids := range owners {
		if len(ids) == 1 {
			labels = append(labels, label)
		}
	}
	sort.Slice(labels, func(i, j int) bool { return len(labels[i]) > len(labels[j]) })
	targets := map[string]bool{}
	for _, id := range r.TargetIDs {
		targets[id] = true
	}
	// The transport may prepend its fixed AI label after model generation.
	// Ignore only that presentation tag; validate the actual following address.
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "[AI]") {
		text = strings.TrimSpace(strings.TrimPrefix(text, "[AI]"))
	}
	for _, sentence := range unquotedReplySentences(text) {
		prefix := strings.TrimLeftFunc(sentence, unicode.IsSpace)
		for _, label := range labels {
			for _, candidate := range []string{label, "@" + label} {
				if !strings.HasPrefix(prefix, candidate) {
					continue
				}
				tail := strings.TrimLeftFunc(strings.TrimPrefix(prefix, candidate), unicode.IsSpace)
				if tail == "" || !strings.ContainsRune(",，:：", []rune(tail)[0]) {
					continue
				}
				for id := range owners[label] {
					if !targets[id] {
						return errors.New("reply_named_address_target_mismatch")
					}
				}
			}
		}
	}
	return nil
}

// Quoted clauses are opaque; a sentence outside quotes starts only after an
// actual sentence boundary. A reported name in ordinary narration is not a
// vocative. Markdown block quotes are likewise not an outgoing address.
func unquotedReplySentences(text string) []string {
	runes := []rune(text)
	stack := []rune{}
	starts := []string{}
	start := 0
	markdownQuote := false
	lineStart := true
	for i, r := range runes {
		apostrophe := r == '\'' && i > 0 && i+1 < len(runes) && unicode.IsLetter(runes[i-1]) && unicode.IsLetter(runes[i+1])
		if lineStart && r != ' ' && r != '\t' {
			markdownQuote = r == '>'
			lineStart = false
		}
		if r == '\n' {
			lineStart, markdownQuote = true, false
		}
		if markdownQuote {
			continue
		}
		if len(stack) > 0 {
			if r == stack[len(stack)-1] && !apostrophe {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		switch r {
		case '“':
			stack = append(stack, '”')
		case '‘':
			stack = append(stack, '’')
		case '「':
			stack = append(stack, '」')
		case '『':
			stack = append(stack, '』')
		case '"', '`':
			stack = append(stack, r)
		case '\'':
			if !apostrophe {
				stack = append(stack, r)
			}
		}
		if len(stack) > 0 {
			continue
		}
		if strings.ContainsRune("。！？!?\n", r) {
			starts = append(starts, string(runes[start:i+1]))
			start = i + 1
		}
	}
	if start < len(runes) {
		starts = append(starts, string(runes[start:]))
	}
	return starts
}
