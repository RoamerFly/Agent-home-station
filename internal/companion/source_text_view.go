package companion

import (
	"strings"
	"unicode"
)

// SourceTextClause is an actual contiguous, unquoted part of the original
// output. Offsets are half-open; text is never joined across an opaque quote.
// Scope flags are conservative lexical hints, not a semantic entailment test.
type SourceTextClause struct {
	Text                           string
	RuneStart, RuneEnd             int
	ByteStart, ByteEnd             int
	Conditional, Negated, Question bool
}

func sourceTextClauses(text string) []SourceTextClause {
	runes := []rune(text)
	bytes := make([]int, 0, len(runes)+1)
	for i := range text {
		bytes = append(bytes, i)
	}
	bytes = append(bytes, len(text))
	visible := make([]bool, len(runes))
	for i := range visible {
		visible[i] = true
	}
	stack := []rune{}
	var fence rune
	fenceSize := 0
	lineStart := true
	for i := 0; i < len(runes); {
		if lineStart {
			j := i
			for j < len(runes) && (runes[j] == ' ' || runes[j] == '\t') {
				j++
			}
			end := j
			for end < len(runes) && runes[end] != '\n' {
				end++
			}
			if fence != 0 {
				count := 0
				for j+count < end && runes[j+count] == fence {
					count++
				}
				for k := i; k < end; k++ {
					visible[k] = false
				}
				if count >= fenceSize {
					fence = 0
					fenceSize = 0
				}
				i = end
				lineStart = false
				continue
			}
			if len(stack) == 0 && j < len(runes) {
				if j-i >= 4 || strings.ContainsRune(string(runes[i:j]), '\t') {
					for k := i; k < end; k++ {
						visible[k] = false
					}
					i = end
					lineStart = false
					continue
				}
				if runes[j] == '>' {
					for k := i; k < end; k++ {
						visible[k] = false
					}
					i = end
					lineStart = false
					continue
				}
				if runes[j] == '`' || runes[j] == '~' {
					count := 0
					for j+count < end && runes[j+count] == runes[j] {
						count++
					}
					if count >= 3 {
						fence, fenceSize = runes[j], count
						for k := i; k < end; k++ {
							visible[k] = false
						}
						i = end
						lineStart = false
						continue
					}
				}
			}
			lineStart = false
		}
		r := runes[i]
		if r == '\n' {
			lineStart = true
		}
		if r == '`' && len(stack) == 0 {
			count := 1
			for i+count < len(runes) && runes[i+count] == '`' {
				count++
			}
			end := i + count
			for end < len(runes) {
				if runes[end] != '`' {
					end++
					continue
				}
				n := 1
				for end+n < len(runes) && runes[end+n] == '`' {
					n++
				}
				if n == count {
					end += n
					break
				}
				end += n
			}
			for j := i; j < end; j++ {
				visible[j] = false
			}
			i = end
			continue
		}
		escaped := false
		for j := i - 1; j >= 0 && runes[j] == '\\'; j-- {
			escaped = !escaped
		}
		apostrophe := r == '\'' && i > 0 && i+1 < len(runes) && unicode.IsLetter(runes[i-1]) && unicode.IsLetter(runes[i+1])
		if len(stack) > 0 {
			visible[i] = false
			if !escaped && !apostrophe {
				if r == stack[len(stack)-1] {
					stack = stack[:len(stack)-1]
				} else if close := sourceQuoteClose(r); close != 0 && r != '"' && r != '\'' && r != '`' {
					stack = append(stack, close)
				}
			}
		} else if !escaped && !apostrophe {
			if close := sourceQuoteClose(r); close != 0 {
				visible[i] = false
				stack = append(stack, close)
			}
		}
		i++
	}
	out := []SourceTextClause{}
	conditional := false
	start := -1
	emit := func(end int, question bool) {
		if start < 0 {
			return
		}
		a, b := start, end
		for a < b && unicode.IsSpace(runes[a]) {
			a++
		}
		for b > a && unicode.IsSpace(runes[b-1]) {
			b--
		}
		if a < b {
			part := string(runes[a:b])
			low := strings.ToLower(part)
			if sourceConditional(low) {
				conditional = true
			}
			out = append(out, SourceTextClause{Text: part, RuneStart: a, RuneEnd: b, ByteStart: bytes[a], ByteEnd: bytes[b], Conditional: conditional, Negated: sourceNegated(low), Question: question || strings.Contains(part, "是否") || strings.HasSuffix(part, "吗")})
		}
		start = -1
	}
	for i, r := range runes {
		if !visible[i] {
			emit(i, false)
			continue
		}
		if strings.ContainsRune("，,；;：:。.!！?？\n", r) {
			emit(i, r == '?' || r == '？')
			if strings.ContainsRune("。.!！?？\n", r) {
				conditional = false
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	emit(len(runes), false)
	return out
}

func sourceQuoteClose(r rune) rune {
	switch r {
	case '“':
		return '”'
	case '‘':
		return '’'
	case '「':
		return '」'
	case '『':
		return '』'
	case '«':
		return '»'
	case '‹':
		return '›'
	case '《':
		return '》'
	case '"', '\'', '`':
		return r
	}
	return 0
}

func sourceConditional(s string) bool {
	for _, word := range []string{"如果", "假如", "假若", "假设", "假定", "倘若", "要是", "只要", "一旦", "即使", "就算", "若是", "若上传", "若发送", "若这", "若那", "若你", "若您", "若他", "若她", "若我", "若该", "if ", "unless ", "suppose "} {
		if strings.Contains(s, word) {
			return true
		}
	}
	return false
}

func sourceNegated(s string) bool {
	for _, word := range []string{"没有", "并未", "并非", "不是", "未曾", "不曾", "没写", "没说", "没注明", "未注明", "不能", "不代表", "不等于", "不一定", "不说明", "not ", "never ", "no caption"} {
		if strings.Contains(s, word) {
			return true
		}
	}
	return false
}
