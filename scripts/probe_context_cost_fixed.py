"""Capture first model inputs for seven synthetic requests in a disposable checkout.

No provider is called and no WeChat message is sent. The output contains only
numeric sizes and tool names, never synthetic message text or local secrets.
Run the same script against two source commits to compare them.
"""

import argparse
import json
import os
from pathlib import Path
import subprocess


PROBE = r'''package admin
import (
    "context"
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "testing"
    "time"
    "wechat-bot/internal/agenthost"
    "wechat-bot/internal/companion"
    "wechat-bot/internal/mcptools"
)

type fixedContextProbe struct { Requests []map[string]any }
func (m *fixedContextProbe) Chat(ctx context.Context, system string, turns []map[string]string) (string, error) {
    return m.ChatFunctions(ctx, system, turns, nil)
}
func (m *fixedContextProbe) ChatFunctions(_ context.Context, system string, turns []map[string]string, tools []mcptools.Tool) (string, error) {
    raw, _ := json.Marshal(tools)
    components := map[string]int{"system": companion.EstimateTokens(system), "tool_definitions": companion.EstimateTokens(string(raw))}
    bytes := map[string]int{"system": len(system), "tool_definitions": len(raw)}
    history := map[string]int{}
    for _, turn := range turns {
        kind := turn["role"]
        var packet struct { Kind string `json:"kind"` }
        if json.Unmarshal([]byte(turn["content"]), &packet) == nil && packet.Kind != "" { kind = packet.Kind }
        components[kind] += companion.EstimateTokens(turn["content"]) + 8
        bytes[kind] += len(turn["content"])
        if kind == "conversation_context" {
            var packet map[string]json.RawMessage
            if json.Unmarshal([]byte(turn["content"]), &packet) == nil {
                if string(packet["summary"]) != "null" { history["summary_bytes"] += len(packet["summary"]) }
                history["timeline_bytes"] += len(packet["timeline"])
                history["scaffold_bytes"] += len(turn["content"]) - len(packet["summary"]) - len(packet["timeline"])
                var rows []map[string]json.RawMessage
                if json.Unmarshal(packet["timeline"], &rows) == nil {
                    history["timeline_rows"] += len(rows)
                    for _, row := range rows {
                        for key, value := range row {
                            history["field_bytes."+key] += len(key) + len(value) + 3
                            history["field_tokens."+key] += companion.EstimateTokens(`"`+key+`":`+string(value))
                        }
                        var source map[string]json.RawMessage
                        if json.Unmarshal(row["source"], &source) == nil {
                            for key := range source { history["source_key_count."+key]++ }
                            if eventID, ok := source["event_id"]; ok {
                                history["source_event_id_present"]++
                                if string(eventID) == string(row["source_event_id"]) { history["source_event_id_matches_top"]++ }
                            }
                        }
                        for _, key := range []string{"quoted_message", "quoted_source", "quote_chain", "reply_context"} { history["quoted_bytes"] += len(row[key]) }
                        for _, key := range []string{"attachment_delivery", "media_observations", "device_observation"} { history["media_bytes"] += len(row[key]) }
                    }
                }
            }
        }
    }
    total := 0
    for _, value := range components { total += value }
    schemaSizes := make([]map[string]any, 0, len(tools))
    for _, tool := range tools {
        definition, _ := json.Marshal(tool)
        schemaSizes = append(schemaSizes, map[string]any{"name": tool.Name, "definition_bytes": len(definition), "input_schema_bytes": len(tool.InputSchema)})
    }
    m.Requests = append(m.Requests, map[string]any{"estimated_input_tokens": total, "components": components, "component_bytes": bytes, "history_structure": history, "active_tools": len(tools), "tool_sizes": schemaSizes})
    return `{"action":"silent","messages":[],"reason":"synthetic_probe"}`, nil
}

func TestFixedContextCostProbe(t *testing.T) {
    output := os.Getenv("AHS_FIXED_CONTEXT_PROBE_OUTPUT")
    if output == "" { t.Skip("opt-in synthetic cost probe") }
    cases := []struct {
        Name, Text, Mode string
        Group, Quote, Media, Task bool
        History int
    }{
        {"ordinary_group", "周六下午一起聊会儿吗", "social", true, false, false, false, 12},
        {"group_quote", "刚才那条引用说了什么？", "direct", true, true, false, false, 20},
        {"media_receipt", "刚才的图片送到了吗？", "direct", false, false, true, false, 20},
        {"task_continuation", "继续处理原任务", "direct", false, false, false, true, 24},
        {"tool_inventory", "目前有哪些工具可用？", "direct", false, false, false, false, 12},
        {"reminder", "明天早上六点提醒我检查文件", "direct", false, false, false, false, 12},
        {"service_recovery", "音乐桥接停止了，检查并恢复", "direct", false, false, false, false, 12},
    }
    result := make([]map[string]any, 0, len(cases))
    for _, tc := range cases {
        f, access := trustedAgent(t)
        f.manager.mu.Lock()
        f.manager.config.GroupRules = []GroupRule{{ID: "fixture-room@chatroom", Mode: "all"}}
        f.manager.mu.Unlock()
        agent := f.manager.agent.config()
        agent.Enabled, agent.TasksEnabled, agent.CrossConversation = true, true, true
        agent.PrivilegedThreads = []string{"*"}
        agent.Host.Mode = "full"
        base := agent.Host.Roots[0]
        agent.Host.Windows = &agenthost.WindowsConfig{Pythonw: filepath.Join(base, "pythonw.exe"), Helper: filepath.Join(base, "helper.pyw"), Spool: filepath.Join(base, "spool")}
        if err := f.manager.agent.save(agent); err != nil { t.Fatal(err) }
        cfg := f.manager.Config().Companion
        cfg.Enabled = true
        cfg.Permissions.Remember = true
        cfg.Permissions.ReplyText = true
        cfg.Permissions.ObserveGroups = true
        cfg.Permissions.AutonomousGroups = true
        cfg.MemoryMessages = 500
        cfg.QuietStart, cfg.QuietEnd = "00:00", "00:00"
        thread := "wxid_owner"
        if tc.Group { thread = "fixture-room@chatroom" }
        current := companion.Input{Channel: "wechat", Account: "test", Thread: thread, Sender: "wxid_owner", TraceID: "current", Mode: tc.Mode, IsGroup: tc.Group, Text: tc.Text, Directed: tc.Mode == "direct"}
        if tc.Task { current.TaskID, current.OriginTraceID = "synthetic-task", "history-3" }
        stamp := time.Now().Add(-time.Hour)
        for i := 0; i < tc.History; i++ {
            old := current
            old.TraceID = fmt.Sprintf("history-%d", i)
            old.Text = []string{"我喜欢红茶。", "刚才的安排是周六。", "这是上传的示意图片。", "把原文件留着。"}[i%4]
            old.Sender = []string{"wxid_owner", "synthetic-peer"}[i%2]
            old.Source = &companion.SourceRef{MessageID: old.TraceID, Sender: old.Sender, Account: "test", Thread: thread, Integrity: "verified"}
            if err := f.manager.memory.Observe(old, cfg, stamp.Add(time.Duration(i)*time.Second)); err != nil { t.Fatal(err) }
            if tc.Media && i == 3 {
                media := companion.MediaMemory{Key: "synthetic-image", Kind: "image", Title: "示意图", State: "accepted", AcceptedParts: 1, TotalParts: 1, RequestTraceID: old.TraceID, RequesterID: old.Sender}
                if err := f.manager.memory.ObserveMedia(old, cfg, media, stamp.Add(time.Duration(i)*time.Second+time.Millisecond)); err != nil { t.Fatal(err) }
            }
        }
        if tc.Quote { current.QuotedSource = &companion.SourceRef{MessageID: "history-3", Sender: "synthetic-peer", Account: "test", Thread: thread, Integrity: "verified"} }
        access.SetTrace(current)
        model := &fixedContextProbe{}
        engine, err := companion.New(cfg, f.manager.memory, model, "合成角色设定")
        if err != nil { t.Fatal(err) }
        engine.Tools, engine.MaxToolCalls = access, agentTurnToolLimit
        if _, err := engine.Respond(t.Context(), current); err != nil { t.Fatal(tc.Name, err) }
        if len(model.Requests) == 0 { t.Fatal(tc.Name, "no model input captured") }
        row := model.Requests[0]
        row["scenario"] = tc.Name
        row["model_requests"] = len(model.Requests)
        result = append(result, row)
    }
    raw, _ := json.MarshalIndent(map[string]any{"fixture_version": 1, "synthetic_only": true, "provider_calls": 0, "wechat_sends": 0, "cases": result}, "", "  ")
    if err := os.WriteFile(output, raw, 0600); err != nil { t.Fatal(err) }
}
'''


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-root", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--allow-current", action="store_true", help="allow a temporary synthetic test in this working tree")
    args = parser.parse_args()
    root = args.source_root.resolve()
    if root == Path(__file__).resolve().parents[1] and not args.allow_current:
        parser.error("source-root must be a disposable checkout")
    probe = root / "internal/admin/context_cost_fixed_probe_test.go"
    if probe.exists():
        parser.error("probe file already exists")
    output = args.output.resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    try:
        probe.write_text(PROBE, encoding="utf-8")
        subprocess.run(
            ["go", "test", "./internal/admin", "-run", "^TestFixedContextCostProbe$", "-count=1", "-timeout", "180s"],
            cwd=root,
            env=dict(os.environ, AHS_FIXED_CONTEXT_PROBE_OUTPUT=str(output)),
            check=True,
        )
    finally:
        probe.unlink(missing_ok=True)
    data = json.loads(output.read_text(encoding="utf-8"))
    data["source_commit"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
    data["source_dirty"] = bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=root, text=True).strip())
    output.write_text(json.dumps(data, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps({"source_commit": data["source_commit"], "cases": {r["scenario"]: r["estimated_input_tokens"] for r in data["cases"]}}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
