package station

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/plugins"
	"wechat-bot/internal/strictjson"
	"wechat-bot/internal/workspacefs"
)

const (
	localExternalPackage      = "external-tools"
	localExternalHTTPCap      = "external.http_request"
	localExternalHTTPList     = "station_external_http_catalog"
	localExternalHTTPCall     = "station_external_http"
	localExternalRegistration = "station-external-tools.json"
)

func externalDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *localProjectTools) externalRegistration() ([]byte, error) {
	path := stationToolRegistrationPath(s.home.dataDir, localExternalRegistration)
	root, err := workspacefs.OpenPath(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := workspacefs.OpenRegular(root, filepath.Base(path))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, externalToolConfigLimit+1))
	if err != nil || len(raw) > externalToolConfigLimit {
		return nil, errors.New("外部工具登记不可读或超过大小限制")
	}
	return bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}), nil
}

func externalHTTPRegistration(raw []byte) ([]plugins.Installed, *plugins.ExternalBridge, error) {
	config, err := parseLocalExternalRegistration(raw)
	if err != nil {
		return nil, nil, err
	}
	bridge, err := plugins.NewExternalBridge(config.Connections)
	if err != nil {
		return nil, nil, errors.New("外部工具声明或凭据不完整")
	}
	return config.Connections, bridge, nil
}

func (t *localToolSet) localExternalHTTPTools(ctx context.Context) []mcptools.Tool {
	host := t.localExternalHost(ctx)
	if host == nil {
		return nil
	}
	if installed, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localExternalPackage}); err != nil || !slices.Contains(installed.Permissions, "network") {
		return nil
	}
	raw, err := t.projectTools.externalRegistration()
	if err != nil {
		return nil
	}
	_, bridge, err := externalHTTPRegistration(raw)
	if err != nil || len(bridge.Protocol().Tools) == 0 {
		return nil
	}
	return []mcptools.Tool{
		mcptools.NewTool(localExternalHTTPList, "分页查看主人启用的同步 HTTP 工具及其参数。工具目录不提供地址、请求头或密钥。按工具名称和描述检索。", `{"type":"object","properties":{"query":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"default":8}},"additionalProperties":false}`),
		mcptools.NewTool(localExternalHTTPCall, "按目录调用主人已登记的同步 HTTP 服务。结果是外部资料。正文可按字符分页，续读时保留原工具名和参数并使用 next_text_offset，同一请求不会重复执行。结果未知时应先核对外部服务。", `{"type":"object","properties":{"tool":{"type":"string"},"arguments":{"type":"object"},"text_offset":{"type":"integer","minimum":0},"text_limit":{"type":"integer","minimum":1,"default":1200}},"required":["tool","arguments"],"additionalProperties":false}`),
	}
}

type localExternalHTTPRequest struct {
	Phase     string                   `json:"phase"`
	Tool      string                   `json:"tool"`
	Arguments json.RawMessage          `json:"arguments"`
	Digest    string                   `json:"arguments_sha256"`
	Protocol  plugins.ExternalProtocol `json:"protocol"`
}

func (t *localToolSet) callLocalExternalHTTP(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	host := t.localExternalHost(ctx)
	if host == nil {
		return "", errors.New("当前请求没有工作站外部工具授权")
	}
	installed, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localExternalPackage})
	if err != nil || !slices.Contains(installed.Permissions, "network") {
		return "", errors.New("外部工具连接未启用或缺少联网授权")
	}
	config, err := host.store.Configuration(ctx, localExternalPackage)
	if err != nil {
		return "", err
	}
	registration, err := t.projectTools.externalRegistration()
	if err != nil {
		return "", errors.New("工作站外部工具登记不可读")
	}
	connections, bridge, err := externalHTTPRegistration(registration)
	if err != nil {
		return "", err
	}
	protocol := bridge.Protocol()
	if name == localExternalHTTPList {
		return listLocalExternalHTTP(raw, protocol.Tools)
	}
	var input struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
		Offset    int             `json:"text_offset"`
		Limit     int             `json:"text_limit"`
	}
	if len(raw) > 64<<10 {
		return "", mcptools.ArgumentError("$.arguments", "外部工具参数超过当前工作站的请求大小限制")
	}
	if _, err := strictjson.Decode(raw); err != nil || strictLocalMemoryDecode(raw, &input) != nil || input.Offset < 0 || input.Limit < 0 {
		return "", mcptools.ArgumentError("$", "请提供目录中的工具名及参数对象")
	}
	if input.Limit == 0 {
		input.Limit = 1200
	}
	idx := slices.IndexFunc(protocol.Tools, func(tool plugins.ExternalProtocolTool) bool { return tool.Name == input.Tool })
	if idx < 0 {
		return "", mcptools.ArgumentError("$.tool", "该工具未在工作站启用")
	}
	args, err := strictjson.Decode(input.Arguments)
	object, ok := args.(map[string]any)
	if err != nil || !ok {
		return "", mcptools.ArgumentError("$.arguments", "请按该工具的参数声明提供对象")
	}
	selected := protocol.Tools[idx]
	arguments, err := json.Marshal(object)
	if err != nil {
		return "", err
	}
	grant, err := t.access.resolve()
	if err != nil {
		return "", err
	}
	source, ok := t.store.SourceForInput(companion.Input{Channel: "local", Account: grant.FishID, Thread: grant.OwnerID, Sender: grant.OwnerID, TraceID: t.traceID})
	if !ok || source.EventID == "" {
		return "", errors.New("本轮外部工具请求来源已变化")
	}
	digest := externalDigest(registration)
	request := localExternalHTTPRequest{Phase: "call", Tool: input.Tool, Arguments: arguments, Digest: externalDigest(arguments),
		Protocol: plugins.ExternalProtocol{Version: protocol.Version, Tools: []plugins.ExternalProtocolTool{selected}}}
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	inv := pluginhost.Invocation{Account: grant.FishID, Thread: grant.OwnerID, Requester: grant.OwnerID,
		Purpose: "workstation external HTTP", RequestKey: localCheckpointKey("station-external-http", source.EventID, source.Revision, input.Tool, arguments),
		Authorization: localCheckpointKey(grant.projectJobAuthorization(), digest), SourceVersions: map[string]int{source.EventID: source.Revision}}
	binding := localExternalBinding{tools: t, host: host, installed: installed, configRev: config.Revision, grant: grant, source: source,
		registration: digest, invocation: inv, capability: localExternalHTTPCap, arguments: encoded}
	policy := &localExternalHTTPPolicy{localExternalBinding: binding, request: request, tool: selected, bridge: bridge}
	for _, connection := range connections {
		if strings.HasPrefix(selected.Name, connection.Manifest.ID+"__") {
			for _, secret := range connection.Secrets {
				if secret != "" {
					policy.secrets = append(policy.secrets, secret)
				}
			}
			break
		}
	}
	slices.SortFunc(policy.secrets, func(a, b string) int { return len(b) - len(a) })
	view, output, err := binding.invoke(ctx, policy, localExternalHTTPCall, input.Tool, true, input.Offset > 0)
	if err != nil {
		return "", err
	}
	if output == nil {
		return localProcessJSON(view)
	}
	page, err := localExternalTextPage(output, input.Offset, input.Limit)
	if err != nil {
		return "", err
	}
	view["output"] = page
	view["untrusted_external_content"] = true
	return localProcessJSON(view)
}

func (t *localToolSet) localExternalHost(ctx context.Context) *localProjectToolHost {
	host := t.projectHostForDiscovery()
	if host == nil || t.run.CheckSource(ctx) != nil {
		return nil
	}
	grant, err := t.access.resolve()
	if err != nil {
		return nil
	}
	_, ok := t.store.SourceForInput(companion.Input{Channel: "local", Account: grant.FishID, Thread: grant.OwnerID, Sender: grant.OwnerID, TraceID: t.traceID})
	if !ok {
		return nil
	}
	return host
}

func (b *localExternalBinding) invoke(ctx context.Context, policy pluginruntime.CallPolicy, tool, target string, checkpoint, readOnly bool) (map[string]any, json.RawMessage, error) {
	var result pluginruntime.CallResult
	var err error
	recorded := slices.ContainsFunc(b.tools.run.Operations, func(op companion.LocalToolCheckpoint) bool {
		return op.Tool == tool && op.Key == b.invocation.RequestKey
	})
	if recorded || checkpoint && b.tools.inspectOnly || readOnly {
		job, err := b.host.store.JobByRequest(ctx, localExternalPackage, b.invocation.Thread, b.invocation.RequestKey)
		if err != nil || readOnly && job.State != "completed" || b.Check(ctx, job, pluginhost.Capability{ID: b.capability}) != nil {
			return nil, nil, errors.New("没有可续读的外部工具结果，请先核对原请求")
		}
		result = pluginruntime.CallResult{Job: job, Reused: true}
	} else {
		if err := b.host.controller.EnsureStarted(ctx, localExternalPackage, b.installed.Revision); err != nil {
			return nil, nil, errors.New("工作站外部工具包无法启动")
		}
		op := companion.LocalToolCheckpoint{Key: b.invocation.RequestKey, Tool: tool, Grant: b.invocation.Authorization, Target: target}
		if checkpoint {
			if err := b.tools.run.Reserve(ctx, op); err != nil {
				return nil, nil, errors.New("外部请求检查点未保存，请重新核对原请求")
			}
			b.tools.run.Operations = append(b.tools.run.Operations, op)
		}
		result, err = b.host.controller.Invoke(ctx, pluginruntime.CallRequest{Plugin: localExternalPackage,
			Capability: b.capability, Context: b.invocation, Arguments: b.arguments}, policy)
		if err != nil {
			if checkpoint {
				b.tools.inspectOnly = true
			}
			return nil, nil, err
		}
		if checkpoint && result.Job.ID != "" {
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			saveErr := b.tools.run.RecordResult(saveCtx, b.invocation.RequestKey, result.Job.ID)
			cancel()
			if saveErr != nil {
				b.tools.inspectOnly = true
				return nil, nil, errors.New("外部请求已登记，请从原请求检查结果")
			}
		}
	}
	state := result.Job.State
	view := map[string]any{"tool": target, "task_id": result.Job.ID, "state": state, "reused": result.Reused}
	if state != "completed" {
		if checkpoint {
			b.tools.inspectOnly = true
		}
		view["instruction"] = "外部请求尚无已核验结果，请核对原服务状态后再决定后续操作。"
		return view, nil, nil
	}
	output, err := b.host.store.PrivateJobResult(ctx, result.Job, func(ctx context.Context, job pluginhost.Job) error {
		return b.Check(ctx, job, pluginhost.Capability{ID: b.capability})
	})
	if err != nil {
		return nil, nil, err
	}
	return view, output, nil
}

func localExternalTextPage(result json.RawMessage, offset, limit int) (map[string]any, error) {
	var fields map[string]any
	if json.Unmarshal(result, &fields) != nil {
		return nil, errors.New("外部工具结果格式无效")
	}
	body, _ := fields["text"].(string)
	text := []rune(body)
	if offset > len(text) {
		return nil, mcptools.ArgumentError("$.text_offset", "正文位置超出结果范围")
	}
	// Leave space for JSON escaping and the runner's result metadata. Explicit
	// larger ranges continue at the returned offset instead of being dropped.
	end := offset + min(limit, len(text)-offset, 3000)
	for end > offset {
		encoded, _ := json.Marshal(string(text[offset:end]))
		if len(encoded) <= 3400 {
			break
		}
		end = offset + (end-offset)*3/4
	}
	fields["text"] = string(text[offset:end])
	fields["text_offset"], fields["text_limit"], fields["text_total_characters"] = offset, limit, len(text)
	if end < len(text) {
		fields["next_text_offset"] = end
	}
	return fields, nil
}

func listLocalExternalHTTP(raw json.RawMessage, tools []plugins.ExternalProtocolTool) (string, error) {
	var input struct {
		Query  string `json:"query"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if strictLocalMemoryDecode(raw, &input) != nil || input.Offset < 0 || input.Limit < 0 {
		return "", mcptools.ArgumentError("$", "请提供有效的查询和非负分页参数")
	}
	if input.Limit == 0 {
		input.Limit = 8
	}
	query := strings.ToLower(strings.TrimSpace(input.Query))
	matches := make([]mcptools.Tool, 0, len(tools))
	for _, tool := range tools {
		if strings.Contains(strings.ToLower(tool.Name+" "+tool.Description), query) {
			matches = append(matches, mcptools.Tool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
		}
	}
	if input.Offset > len(matches) {
		return "", mcptools.ArgumentError("$.offset", "起点超出结果范围")
	}
	end := input.Offset + min(input.Limit, len(matches)-input.Offset)
	return localProcessJSON(map[string]any{"tools": matches[input.Offset:end], "total": len(matches), "next_offset": end, "has_more": end < len(matches)})
}

type localExternalBinding struct {
	tools        *localToolSet
	host         *localProjectToolHost
	installed    pluginhost.InstalledPackage
	configRev    int
	grant        localWorkspaceGrant
	source       *companion.SourceRef
	registration string
	invocation   pluginhost.Invocation
	capability   string
	arguments    json.RawMessage
	extraCheck   func(context.Context) error
}

type localExternalHTTPPolicy struct {
	localExternalBinding
	request localExternalHTTPRequest
	tool    plugins.ExternalProtocolTool
	bridge  *plugins.ExternalBridge
	secrets []string

	mu       sync.Mutex
	started  bool
	body     []byte
	observed plugins.ExternalHTTPObservation
}

func (p *localExternalBinding) Check(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if job.Plugin != localExternalPackage || job.Tool != p.capability || cap.ID != p.capability ||
		job.PackageDigest != p.installed.Digest || job.ConfigRevision != strconv.Itoa(p.configRev) ||
		!reflect.DeepEqual(job.Context, p.invocation) || !strictjson.Matches(job.Arguments, p.arguments) {
		return errors.New("外部工具调用来源或参数已变化")
	}
	if !p.tools.access.current(p.grant) || p.tools.run.CheckSource(ctx) != nil {
		return errors.New("外部工具请求授权已变化")
	}
	current, err := installedLocalProject(ctx, p.host, localProjectOperation{pkg: localExternalPackage})
	if err != nil || current.Revision != p.installed.Revision || !slices.Contains(current.Permissions, "network") {
		return errors.New("外部工具已停用或联网授权已变化")
	}
	config, err := p.host.store.Configuration(ctx, localExternalPackage)
	if err != nil || config.Revision != p.configRev {
		return errors.New("外部工具配置已变化")
	}
	raw, err := p.tools.projectTools.externalRegistration()
	if err != nil || externalDigest(raw) != p.registration {
		return errors.New("工作站外部工具登记已变化")
	}
	if job.ID != "" {
		current, err := p.host.store.Job(ctx, job.ID)
		if err != nil || current.CancelRequested || current.Revision != job.Revision {
			return errors.New("外部工具任务已取消或变化")
		}
	}
	if p.extraCheck != nil {
		return p.extraCheck(ctx)
	}
	return nil
}

func (p *localExternalHTTPPolicy) Service(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started || job.ID == "" || method != "wechat/external_request" || !strictjson.Matches(raw, map[string]string{
		"phase": "call", "tool": p.request.Tool, "arguments_sha256": p.request.Digest,
	}) {
		return nil, errors.New("外部工具请求未获授权或已提交")
	}
	if err := p.Check(ctx, job, cap); err != nil {
		return nil, err
	}
	p.started = true
	bound, cancel := p.watchedContext(ctx, job, cap)
	defer cancel()
	reply, err := p.bridge.Execute(bound, plugins.ExternalRequest{Phase: "call", Tool: p.request.Tool, Arguments: p.request.Arguments})
	if err != nil {
		return nil, errors.New("外部请求结果未确认，请核对原服务")
	}
	if err := p.Check(bound, job, cap); err != nil {
		return nil, err
	}
	body, err := base64.StdEncoding.Strict().DecodeString(reply.BodyBase64)
	if err != nil {
		return nil, err
	}
	p.body = body
	p.observed.ContentType = reply.ContentType
	if plugins.ExternalHTTPIsJSON(body, reply.ContentType) {
		p.observed.BodyBase64 = reply.BodyBase64
	} else {
		p.observed.BinaryRef = &plugins.ExternalHTTPBlobRef{SHA256: externalDigest(body), Size: int64(len(body))}
	}
	return json.Marshal(p.observed)
}

func (p *localExternalBinding) watchedContext(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability) (context.Context, context.CancelFunc) {
	bound, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-bound.Done():
				return
			case <-ticker.C:
				if p.Check(bound, job, cap) != nil {
					cancel()
					return
				}
			}
		}
	}()
	return bound, cancel
}

func (p *localExternalHTTPPolicy) Validate(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.Check(ctx, job, cap); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	var result plugins.ExternalHTTPWireResult
	if !p.started || len(p.body) == 0 || strictjson.DecodeKnownFields(raw, &result) != nil || !reflect.DeepEqual(result.Observation, p.observed) {
		return pluginruntime.VerifiedResult{}, errors.New("外部工具结果未通过实际响应核验")
	}
	var ref plugins.ExternalHTTPBlobRef
	if p.observed.BinaryRef != nil {
		ref = *p.observed.BinaryRef
	}
	output, err := plugins.MaterializeExternalHTTPOutput(p.tool, result, p.body, ref, p.observed.ContentType)
	if err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	return p.saveOutput(ctx, job, cap, p.tool.Name, output, p.secrets, nil)
}

func (p *localExternalBinding) saveOutput(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability, label string, output plugins.Output, secrets []string, metadata map[string]any) (pluginruntime.VerifiedResult, error) {
	for _, secret := range secrets {
		output.Text = strings.ReplaceAll(output.Text, secret, "[已隐藏]")
		for i := range output.Media {
			output.Media[i].URL = strings.ReplaceAll(output.Media[i].URL, secret, "[已隐藏]")
		}
	}
	var assets []string
	private := map[string]any{"text": output.Text}
	for name, value := range metadata {
		private[name] = value
	}
	var artifacts []map[string]any
	var mediaRefs []plugins.MediaResult
	for index, media := range output.Media {
		if len(media.Data) == 0 {
			mediaRefs = append(mediaRefs, media)
			continue
		}
		if p.tools.access.home.artifacts == nil {
			return pluginruntime.VerifiedResult{}, errors.New("外部文件已收到，但成品登记不可用")
		}
		dir, err := os.MkdirTemp(p.tools.projectTools.home.dataDir, ".external-result-")
		if err != nil {
			return pluginruntime.VerifiedResult{}, err
		}
		path := filepath.Join(dir, "external-response-"+strconv.Itoa(index+1)+".bin")
		defer os.Remove(dir)
		defer os.Remove(path)
		if err = os.WriteFile(path, media.Data, 0600); err != nil {
			return pluginruntime.VerifiedResult{}, err
		}
		artifact, err := p.tools.access.home.artifacts.publishFile(ctx, localArtifactInput{
			Key: job.ID, Expected: externalDigest(media.Data), project: localExternalPackage,
		}, p.source, p.tools.access, p.grant, path, nil)
		if err != nil {
			return pluginruntime.VerifiedResult{}, err
		}
		view := artifact.view()
		view["content_type"] = media.MIMEType
		artifacts = append(artifacts, view)
		assets = append(assets, artifact.ID)
	}
	if err := p.Check(ctx, job, cap); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	if len(artifacts) == 1 {
		private["artifact"] = artifacts[0]
		private["content_type"] = artifacts[0]["content_type"]
	} else if len(artifacts) > 1 {
		private["artifacts"] = artifacts
	}
	if len(mediaRefs) != 0 {
		private["media"] = mediaRefs
	}
	public, _ := json.Marshal(map[string]any{"tool": label, "host_observed": true})
	data, _ := json.Marshal(private)
	return pluginruntime.VerifiedResult{Result: public, Private: data, Assets: assets}, nil
}
