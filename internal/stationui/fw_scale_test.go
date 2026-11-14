package stationui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 目标平台是 Windows + WebView2，字体栈落到 Microsoft YaHei UI，只有 Regular(400)
// 与 Bold(700) 两个真实字面。按 CSS Fonts L4 匹配规则 desired 500 命中 400、
// 600 命中 700，中间档在用户端并不存在、不承载语义。本文件就是那条刻度的回归闸门。
//
// 五条断言的分工（前三条是允许集，闭合；后两条是定向报错，信息更好读）：
//  1. TestHomeCSSFontWeightsStayOnScale            所有 font-weight 取值 ∈ 允许集
//  2. TestHomeCSSFontShorthandStaysHarmless        font 简写 ∈ 允许集（它绕得过第 1 条）
//  3. TestHomeCSSFontVariationAxisIsOnScale        "wght" 轴 ∈ 允许集（它绕得过第 1、2 条）
//  4. TestHomeCSSFontWeightTokensAreClosed         --fw-* 名字 ∈ 允许集（封住「定义」而非「使用」）
//  5. TestHomeCSSHasNoMediumFontWeightReference    定向：var(--fw-medium) 的使用
//
// 原则：断言写成「允许集」，不写成「禁止某个写法」。允许集是闭合的，禁止列表永远漏下一种实现。
// 第 4 条正是为此而加：第 5 条只能发现「复活之后被使用」，单写 `--fw-medium:500;` 定义时它不响。
//
// 覆盖面（边界要写清楚，否则会误以为闸门管得更宽）：
//   - 覆盖 static/ 下**所有** .css 的声明式 font-weight 与 font 简写；
//   - 不覆盖 JS / HTML 内联样式（本轮已核：static/ 下 .js 与 .html 里
//     fontWeight / font-weight 零命中，故没有 el.style.fontWeight = 500 这类漏网）；
//   - 不覆盖 @font-face 的 font-weight：那是「字体能力声明」（如 100 900），不是
//     文本字重，长得一样但语义不同。当前仓里没有 @font-face；将来若引入自托管字体，
//     需先剔除 @font-face 块再断言，否则会被误判越界。这是设计边界，不是漏网。
//   - 「能设字重但字面不出现 font-weight」的属性只有三个：font 简写（已封）、
//     font-variation-settings 的 "wght" 轴（本文件 TestHomeCSSFontVariationAxisIsOnScale
//     也已封）、font-synthesis: weight（只能伪造 bold，不能合成 500，不适用）。
//     font-variation-settings 的前提与 @font-face 相同，都得先有可变字体：目标平台的
//     Microsoft YaHei UI 不是可变字体，但它上面还有 MiSans 与 HarmonyOS Sans SC
//     （见 css 的 --font-sans 栈），这两个都是可变字体家族。一旦某台机器真装了它们，
//     font-variation-settings 就是一条能绕过全部断言的设重通道，所以断言与注释都留着。
//
// CSS 属性名大小写不敏感，Font-Weight: 500 与 FONT: 500 12px serif 都合法且生效，
// 所以各条正则都带 (?i)，否则换个编辑器插件把属性名统一大写就能整条绕过。

var (
	// 注释里会出现示例写法，匹配前必须整体剔除。
	cssCommentPattern = regexp.MustCompile(`(?s)/\*.*?\*/`)
	// 抓每处 font-weight 的取值。
	cssFontWeightPattern = regexp.MustCompile(`(?i)font-weight\s*:\s*([^;}]+)`)
	// 抓每处 font 简写。font 是简写属性，font: 500 12px/1.4 sans-serif 会连带设置
	// font-weight: 500，只查 font-weight 会整条漏过。前面要求是行首/分号/花括号/空白，
	// 这样 font-family、font-size、-webkit-font-smoothing 都不会误匹配。
	cssFontShorthandPattern = regexp.MustCompile(`(?i)(?:^|[;{\s])font\s*:\s*([^;}]+)`)
	// 只抓 "wght" 轴标签本身，**不**先抓 font-variation-settings 声明。
	// 按「载体」枚举会漏：轴值可以存进自定义属性（`--axis: "wght" 500;` 然后用
	// font-variation-settings: var(--axis)），那时声明里根本没有轴标签。按「事实」枚举
	// ——"wght" 这个字面在整份 CSS 里除了设字重没有第二种用途——才是闭合的。
	//
	// 捕获组用 [^;},]+ 而不是 [0-9.]+：数字域是黑名单式的，`"wght" var(--fw-strong)`
	// 这类写法连被检查的机会都没有（不是放过，是没看见）。放宽成「到分隔符为止」，
	// 让 var()/calc()/env() 一律进白名单比对、该红的红。当前仓 font-variation-settings
	// 0 处，所以这一步零误报。
	// \s* 而不是 \s+：`"wght"500` 也是合法 CSS，string token 与 number token 之间
	// 不需要空白，\s+ 会漏。
	cssWeightAxisPattern = regexp.MustCompile(`(?i)["']wght["']\s*([^;},]+)`)
	// 历史引用：值的形态是 var(--fw-medium)。同样带 (?i)，因为属性名那半是大小写不敏感的，
	// Font-Weight: var(--fw-medium) 依然会解析成 500 档、依然要拦。
	cssMediumWeightPattern = regexp.MustCompile(`(?i)font-weight\s*:\s*var\(\s*--fw-medium\s*\)`)
	// 抓 --fw-* 自定义属性名。这里**不加** (?i)：自定义属性名大小写敏感，--FW-MEDIUM 是另一个
	// token、也不会被 var(--fw-medium) 引用到，按大小写区分才不会产生噪音。
	cssFontWeightTokenPattern = regexp.MustCompile(`--fw-[a-z0-9-]+`)
)

// allowedFontWeights 是 font-weight 唯一允许的取值集合。
var allowedFontWeights = map[string]bool{
	"var(--fw-regular)": true,
	"var(--fw-strong)":  true,
	"400":               true,
	"700":               true,
}

// allowedFontShorthands 是 font 简写唯一允许的取值集合。当前全仓只有
// `button, input, textarea, select { font: inherit; }` 一处，故只放行 inherit。
// 将来若要用带权重的 font 简写，请改回 font-weight 长写，或先在这里显式扩白名单
// 并说明为什么该权重不违反两档刻度。
var allowedFontShorthands = map[string]bool{
	"inherit": true,
}

// allowedFontWeightTokens 是 --fw-* 命名空间唯一允许出现的两个名字。
// 这是「允许集」写法：它同时封住定义（`--fw-medium:500;`）与使用
// （`font-weight: var(--fw-medium)`），也顺手封住将来可能冒出来的 --fw-light / --fw-semibold。
var allowedFontWeightTokens = map[string]bool{
	"--fw-regular": true,
	"--fw-strong":  true,
}

// normalizeCSSValue 去掉取值里的全部空白（防 var( --fw-regular ) 绕过）并剥离
// !important 后缀（!important 不改变取值语义，不该造成误报）。
func normalizeCSSValue(value string) string {
	return strings.ReplaceAll(strings.Join(strings.Fields(value), ""), "!important", "")
}

// loadStationStylesheets 读取 static/ 下所有样式表，返回「文件名 → 剔除注释后的正文」。
// 用 Glob 而不是写死单个文件名：只查一个文件时，将来拆出第二个样式表会静默不覆盖，
// go test 照绿，那比没有闸门更糟。
func loadStationStylesheets(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob("static/*.css")
	if err != nil {
		t.Fatalf("展开 static/*.css: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("static/ 下没有找到任何 .css，闸门失效")
	}
	sort.Strings(paths)
	sheets := make(map[string]string, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s: %v", path, err)
		}
		sheets[path] = cssCommentPattern.ReplaceAllString(string(content), "")
	}
	return sheets
}

// describeOffsets 把字节偏移翻成「行号 + 该行上下文」，让失败信息能直接定位。
func describeOffsets(css string, offsets []int) string {
	described := make([]string, 0, len(offsets))
	for _, offset := range offsets {
		if offset > len(css) {
			offset = len(css)
		}
		line := strings.Count(css[:offset], "\n") + 1
		start := strings.LastIndexByte(css[:offset], '\n') + 1
		end := len(css)
		if next := strings.IndexByte(css[offset:], '\n'); next >= 0 {
			end = offset + next
		}
		snippet := strings.Join(strings.Fields(css[start:end]), " ")
		if len(snippet) > 180 {
			snippet = snippet[:180] + "…"
		}
		described = append(described, fmt.Sprintf("第 %d 行: %s", line, snippet))
	}
	return strings.Join(described, "\n  ")
}

// TestHomeCSSFontWeightTokensAreClosed 断言 --fw-* 命名空间只有允许的两个名字。
// 与 TestHomeCSSHasNoMediumFontWeightReference 的分工：那条只看得见「复活之后被使用」，
// 单独写一行 `--fw-medium:500;` 定义时它是绿的；这条按名字集合判定，定义与使用一起封。
func TestHomeCSSFontWeightTokensAreClosed(t *testing.T) {
	for name, css := range loadStationStylesheets(t) {
		offScale := make([]string, 0)
		offsets := make([]int, 0)
		for _, loc := range cssFontWeightTokenPattern.FindAllStringIndex(css, -1) {
			token := css[loc[0]:loc[1]]
			if allowedFontWeightTokens[token] {
				continue
			}
			offScale = append(offScale, token)
			offsets = append(offsets, loc[0])
		}
		if len(offScale) == 0 {
			continue
		}
		t.Fatalf("%s: --fw-* 只允许 --fw-regular 与 --fw-strong（定义与使用一并封），越界 %d 处：%s\n  %s",
			name, len(offScale), strings.Join(offScale, "、"), describeOffsets(css, offsets))
	}
}

// TestHomeCSSHasNoMediumFontWeightReference 断言 var(--fw-medium) 已没有任何 font-weight 引用。
func TestHomeCSSHasNoMediumFontWeightReference(t *testing.T) {
	for name, css := range loadStationStylesheets(t) {
		matches := cssMediumWeightPattern.FindAllStringIndex(css, -1)
		if len(matches) == 0 {
			continue
		}
		offsets := make([]int, 0, len(matches))
		for _, match := range matches {
			offsets = append(offsets, match[0])
		}
		t.Fatalf("字重只允许两级，不允许 500 档引用：%s 命中 %d 处 font-weight: var(--fw-medium)\n  %s",
			name, len(matches), describeOffsets(css, offsets))
	}
}

// TestHomeCSSFontWeightsStayOnScale 断言所有 font-weight 取值都落在两级刻度上。
func TestHomeCSSFontWeightsStayOnScale(t *testing.T) {
	for name, css := range loadStationStylesheets(t) {
		offScale := make([]string, 0)
		offsets := make([]int, 0)
		for _, match := range cssFontWeightPattern.FindAllStringSubmatchIndex(css, -1) {
			// raw 用于显示，value 用于比对：normalize 会删掉空白，直接打印会出现
			// "50012px/1.4var(--font-sans)" 这种看不懂的串，而失败信息是本测试唯一的产出。
			raw := strings.TrimSpace(css[match[2]:match[3]])
			if allowedFontWeights[normalizeCSSValue(raw)] {
				continue
			}
			offScale = append(offScale, fmt.Sprintf("%q", raw))
			offsets = append(offsets, match[0])
		}
		if len(offScale) == 0 {
			continue
		}
		t.Fatalf("%s: font-weight 只允许 var(--fw-regular) / var(--fw-strong) / 400 / 700，越界 %d 处：%s\n  %s",
			name, len(offScale), strings.Join(offScale, "、"), describeOffsets(css, offsets))
	}
}

// TestHomeCSSFontShorthandStaysHarmless 断言 font 简写没有夹带字重。
// font 是简写属性，font: 500 12px/1.4 sans-serif 会连带设置 font-weight: 500，
// 只查 font-weight 会整条漏过，所以这一条必须独立守住。
func TestHomeCSSFontShorthandStaysHarmless(t *testing.T) {
	for name, css := range loadStationStylesheets(t) {
		offScale := make([]string, 0)
		offsets := make([]int, 0)
		for _, match := range cssFontShorthandPattern.FindAllStringSubmatchIndex(css, -1) {
			// 同 font-weight 那条：显示原值、比对归一值。
			raw := strings.TrimSpace(css[match[2]:match[3]])
			if allowedFontShorthands[normalizeCSSValue(raw)] {
				continue
			}
			offScale = append(offScale, fmt.Sprintf("%q", raw))
			offsets = append(offsets, match[0])
		}
		if len(offScale) == 0 {
			continue
		}
		t.Fatalf("%s: font 简写只允许 inherit（它会连带设置 font-weight，带权重的简写请改回长写），越界 %d 处：%s\n  %s",
			name, len(offScale), strings.Join(offScale, "、"), describeOffsets(css, offsets))
	}
}

// TestHomeCSSFontVariationAxisIsOnScale 断言 "wght" 轴没有越界。
// 它字面上不出现 font-weight，却能把字重设成任意值，是最后一条「看不见的设重通道」。
// 直接在全文扫轴标签（而不是先找 font-variation-settings 声明），这样轴值存在自定义属性里
// 也拦得住；没有 wght 轴的声明（如只调 opsz/slnt）不会命中，故不误判。
func TestHomeCSSFontVariationAxisIsOnScale(t *testing.T) {
	for name, css := range loadStationStylesheets(t) {
		offScale := make([]string, 0)
		offsets := make([]int, 0)
		for _, axis := range cssWeightAxisPattern.FindAllStringSubmatchIndex(css, -1) {
			weight := strings.TrimSpace(css[axis[2]:axis[3]])
			if weight == "400" || weight == "700" {
				continue
			}
			offScale = append(offScale, fmt.Sprintf(`"wght" %s`, weight))
			offsets = append(offsets, axis[0])
		}
		if len(offScale) == 0 {
			continue
		}
		t.Fatalf(`%s: font-variation-settings 的 "wght" 轴只允许 400 / 700（其余档位在目标平台不存在，且它绕得过 font-weight 断言），越界 %d 处：%s`+"\n  %s",
			name, len(offScale), strings.Join(offScale, "、"), describeOffsets(css, offsets))
	}
}
