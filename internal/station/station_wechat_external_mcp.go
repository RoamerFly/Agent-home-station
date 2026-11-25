package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/plugins"
	"wechat-bot/internal/strictjson"
	"wechat-bot/internal/workspacefs"
)

type stationWechatExternalMCPMedia struct {
	SHA256 string
	MIME   string
	Kind   string
	Bytes  int64
}

func (j stationWechatExternalHTTPJob) mcpMedia(index int) companion.GeneratedMedia {
	return companion.GeneratedMedia{ID: fmt.Sprintf("reply-external-mcp-%s-%d", j.ID, index),
		AssetID: fmt.Sprintf("external-mcp-%s-%d", j.ID, index), Kind: "file",
		Title: fmt.Sprintf("工作站 MCP 结果 %d.bin", index+1), CachedMIME: j.MCPMedia[index].MIME}
}

func (t *stationAttachmentTools) callPublicExternalMCP(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if t.externalMu == nil {
		return "", errors.New("微信 MCP 工具暂不可用")
	}
	t.externalMu.Lock()
	defer t.externalMu.Unlock()
	if len(raw) > 1<<20 || t.store == nil || t.projects == nil || t.dir == "" ||
		t.projectOwnerAllowed == nil || !t.projectOwnerAllowed(t.trace) || !t.allowed() {
		return "", wechatapp.ErrAttachmentDenied
	}
	var input localExternalMCPInput
	if _, err := strictjson.Decode(raw); err != nil || strictjson.DecodeKnownFields(raw, &input) != nil ||
		input.PageOffset < 0 || input.PageLimit < 0 || input.TextOffset < 0 || input.TextLimit < 0 {
		return "", mcptools.ArgumentError("$", "MCP 参数或分页范围无效")
	}
	owner, err := t.projects.home.identity.Read()
	if err != nil || owner.LocalOwner == nil {
		return "", errors.New("工作站身份不可用")
	}
	_, _, servers, err := t.projects.publicExternalCatalog(ctx, owner.FishID)
	if err != nil {
		return "", err
	}
	index := slices.IndexFunc(servers, func(server mcptools.ServerConfig) bool { return server.ID == input.Server })
	if index < 0 {
		return "", mcptools.ArgumentError("$.server_id", "该 MCP 连接未启用")
	}
	registration, err := t.projects.externalRegistration()
	if err != nil {
		return "", errors.New("工作站 MCP 登记不可读")
	}
	source, ok := t.store.SourceForInput(t.trace)
	if !ok {
		return "", errors.New("微信原消息来源已变化")
	}
	base := stationWechatExternalHTTPJob{Account: t.trace.Account, Thread: t.trace.Thread, Sender: t.trace.Sender,
		Trace: t.trace.TraceID, Fish: owner.FishID, Source: *source, Grant: t.grants[t.trace.Thread],
		MediaGrant: t.replies[t.trace.Thread], Registration: externalDigest(registration)}
	if name == localExternalMCPList {
		return t.listPublicExternalMCP(ctx, input, servers[index], base)
	}
	return t.invokePublicExternalMCP(ctx, input, servers[index], base)
}

func (t *stationAttachmentTools) mcpPageJob(base stationWechatExternalHTTPJob, serverID, cursor string) stationWechatExternalHTTPJob {
	base.Tool, base.Cursor, base.RequestHash, base.State = serverID, cursor, localCheckpointKey("mcp-list", serverID, cursor), "cursor"
	base.ID = base.expectedID()
	return base
}

func (t *stationAttachmentTools) listPublicExternalMCP(ctx context.Context, in localExternalMCPInput, server mcptools.ServerConfig, base stationWechatExternalHTTPJob) (string, error) {
	if in.Tool != "" || len(in.Arguments) != 0 || in.SchemaTool == "" && (in.TextOffset != 0 || in.TextLimit != 0) ||
		in.SchemaTool != "" && (in.PageOffset != 0 || in.PageLimit != 0) {
		return "", mcptools.ArgumentError("$", "读取目录只接受连接和分页参数")
	}
	var page stationWechatExternalHTTPJob
	var err error
	if in.Cursor == "" {
		page = t.mcpPageJob(base, server.ID, "")
	} else {
		if !validLocalRVCHex(in.Cursor, 64) {
			return "", mcptools.ArgumentError("$.cursor", "目录游标无效")
		}
		page, err = t.readExternalHTTPJob(in.Cursor)
		if err != nil || page.Tool != server.ID || page.RequestHash != localCheckpointKey("mcp-list", server.ID, page.Cursor) ||
			page.ID != t.mcpPageJob(base, server.ID, page.Cursor).ID {
			return "", mcptools.ArgumentError("$.cursor", "目录游标不属于当前请求")
		}
	}
	if !t.externalHTTPAllowed(t.trace, page) || page.Fish != base.Fish || page.Registration != base.Registration {
		return "", wechatapp.ErrAttachmentDenied
	}
	if page.State != "completed" {
		if page.State != "cursor" {
			return "", errors.New("MCP 目录页状态无效")
		}
		if err := t.writeExternalHTTPJob(page, nil); err != nil {
			return "", err
		}
		result, err := t.projects.publicExternalMCP(ctx, base.Fish, base.Registration, server.ID, "list", "", page.Cursor, nil)
		if err != nil {
			return "", err
		}
		page.State, page.Tools, page.NextCursor = "completed", result.Tools, result.NextCursor
		if page.NextCursor != "" {
			next := t.mcpPageJob(base, server.ID, page.NextCursor)
			if prior, err := t.readExternalHTTPJob(next.ID); errors.Is(err, os.ErrNotExist) {
				if err := t.writeExternalHTTPJob(next, nil); err != nil {
					return "", err
				}
			} else if err != nil || !t.externalHTTPAllowed(t.trace, prior) || prior.Registration != base.Registration {
				return "", errors.New("MCP 下一页游标记录无效")
			}
		}
		if err := t.writeExternalHTTPJob(page, nil); err != nil {
			return "", err
		}
	}
	if in.PageOffset > len(page.Tools) {
		return "", mcptools.ArgumentError("$.offset", "目录偏移超过当前页")
	}
	view := map[string]any{"server_id": server.ID, "cursor": page.ID, "untrusted_external_content": true}
	if in.SchemaTool != "" {
		index := slices.IndexFunc(page.Tools, func(tool mcptools.Tool) bool { return tool.Name == in.SchemaTool })
		if index < 0 {
			return "", mcptools.ArgumentError("$.schema_tool", "本目录页没有这个工具")
		}
		limit := in.TextLimit
		if limit == 0 {
			limit = 1200
		}
		fields, _ := json.Marshal(map[string]string{"text": string(page.Tools[index].InputSchema)})
		view["schema"], err = localExternalTextPage(fields, in.TextOffset, limit)
		if err != nil {
			return "", err
		}
		return localProcessJSON(view)
	}
	limit := in.PageLimit
	if limit == 0 {
		limit = 8
	}
	end, size := in.PageOffset, 0
	tools := append([]mcptools.Tool(nil), page.Tools...)
	for end < len(tools) && end-in.PageOffset < limit {
		data, _ := json.Marshal(tools[end])
		if size+len(data) > 3800 && end > in.PageOffset {
			break
		}
		if len(data) > 3800 {
			tools[end].Description = "用 schema_tool 传这个工具名读取完整参数声明。"
			tools[end].InputSchema = nil
			size += 160
		} else {
			size += len(data)
		}
		end++
	}
	view["tools"], view["offset"], view["total_in_page"] = tools[in.PageOffset:end], in.PageOffset, len(tools)
	if end < len(tools) {
		view["next_offset"] = end
	} else if page.NextCursor != "" {
		view["next_cursor"] = t.mcpPageJob(base, server.ID, page.NextCursor).ID
	}
	return localProcessJSON(view)
}

func (t *stationAttachmentTools) invokePublicExternalMCP(ctx context.Context, in localExternalMCPInput, server mcptools.ServerConfig, base stationWechatExternalHTTPJob) (string, error) {
	tool := strings.TrimPrefix(in.Tool, server.ID+"__")
	if !plugins.ExternalMCPName.MatchString(tool) || in.Tool != server.ID+"__"+tool ||
		len(server.Tools) != 0 && !slices.Contains(server.Tools, tool) || in.Cursor != "" || in.PageOffset != 0 ||
		in.PageLimit != 0 || in.SchemaTool != "" || in.TextOffset < 0 || in.TextLimit < 0 {
		return "", mcptools.ArgumentError("$.tool", "该 MCP 工具未获当前连接授权")
	}
	value, err := strictjson.Decode(in.Arguments)
	object, ok := value.(map[string]any)
	if err != nil || !ok {
		return "", mcptools.ArgumentError("$.arguments", "请提供工具参数对象")
	}
	arguments, err := json.Marshal(object)
	if err != nil {
		return "", err
	}
	if len(arguments) > 1<<20 {
		return "", mcptools.ArgumentError("$.arguments", "MCP 请求超过 1 MiB")
	}
	if !t.replyAllowed(t.trace) {
		return "", wechatapp.ErrAttachmentDenied
	}
	base.Tool, base.RequestHash, base.State = in.Tool, localCheckpointKey("mcp-call", in.Tool, arguments), "submitting"
	base.ID = base.expectedID()
	previous, err := t.readExternalHTTPJob(base.ID)
	if err == nil {
		if !t.externalHTTPAllowed(t.trace, previous) || previous.Fish != base.Fish || !previous.MediaGrant.Equal(base.MediaGrant) {
			return "", wechatapp.ErrAttachmentDenied
		}
		if previous.Registration != base.Registration {
			return localProcessJSON(map[string]any{"task_id": previous.ID, "state": "needs_review", "reused": true, "instruction": "MCP 登记已变化，请核对原请求。"})
		}
		if previous.State == "submitting" {
			previous.State = "needs_review"
		}
		return t.externalHTTPView(previous, in.TextOffset, in.TextLimit, true)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if in.TextOffset != 0 {
		return "", mcptools.ArgumentError("$.text_offset", "首次调用请从正文起点读取")
	}
	if err := t.writeExternalHTTPJob(base, nil); err != nil {
		return "", err
	}
	bound, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-bound.Done():
				return
			case <-ticker.C:
				if !t.externalHTTPAllowed(t.trace, base) || !t.replyAllowed(t.trace) {
					cancel()
					return
				}
			}
		}
	}()
	result, callErr := t.projects.publicExternalMCP(bound, base.Fish, base.Registration, server.ID, "call", tool, "", arguments)
	cancel()
	<-done
	if callErr != nil {
		base.State = "needs_review"
	} else {
		base.State, base.Text, base.IsError = "completed", result.Output.Text, result.IsError
	}
	if base.State == "completed" && len(result.Output.Media) != 0 {
		root, err := t.externalHTTPRoot(base.ID)
		if err != nil {
			return "", err
		}
		for index, media := range result.Output.Media {
			item := stationWechatExternalMCPMedia{SHA256: externalDigest(media.Data), Kind: media.Kind, Bytes: int64(len(media.Data))}
			item.MIME, _, _ = mime.ParseMediaType(media.MIMEType)
			if item.MIME == "" {
				item.MIME = "application/octet-stream"
			}
			if len(media.Data) == 0 || writeComfyProduct(root, fmt.Sprintf("output-%d.bin", index), media.Data, item.SHA256) != nil {
				_ = root.Close()
				return "", errors.New("MCP 媒体保存失败，请核对原请求")
			}
			base.MCPMedia = append(base.MCPMedia, item)
		}
		if err := root.Close(); err != nil {
			return "", err
		}
	}
	if err := t.writeExternalHTTPJob(base, nil); err != nil {
		return "", err
	}
	if !t.externalHTTPAllowed(t.trace, base) || !t.replyAllowed(t.trace) {
		return "", wechatapp.ErrAttachmentDenied
	}
	return t.externalHTTPView(base, 0, in.TextLimit, false)
}

func (t *stationAttachmentTools) externalMCPMediaAllowed(in companion.Input, asset string) (stationWechatExternalHTTPJob, int, bool) {
	var empty stationWechatExternalHTTPJob
	const prefix = "external-mcp-"
	if !strings.HasPrefix(asset, prefix) {
		return empty, 0, false
	}
	id, suffix, ok := strings.Cut(asset[len(prefix):], "-")
	if !ok || !validLocalRVCHex(id, 64) {
		return empty, 0, false
	}
	index, err := strconv.Atoi(suffix)
	if err != nil || index < 0 {
		return empty, 0, false
	}
	j, err := t.readExternalHTTPJob(id)
	if err != nil || j.State != "completed" || index >= len(j.MCPMedia) || j.MCPMedia[index].Bytes <= 0 ||
		!validLocalRVCHex(j.MCPMedia[index].SHA256, 64) || !strings.Contains(j.Tool, "__") ||
		!j.MediaGrant.Equal(t.replies[in.Thread]) || !t.replyAllowed(in) || !t.externalHTTPAllowed(in, j) {
		return empty, 0, false
	}
	return j, index, true
}

func (t *stationAttachmentTools) stageExternalMCP(ctx context.Context, in companion.Input, asset string) (string, error) {
	j, index, ok := t.externalMCPMediaAllowed(in, asset)
	if !ok {
		return "", wechatapp.ErrAttachmentDenied
	}
	root, err := t.externalHTTPRoot(j.ID)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := workspacefs.OpenRegular(root, fmt.Sprintf("output-%d.bin", index))
	if err != nil {
		return "", err
	}
	defer file.Close()
	item := j.MCPMedia[index]
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, item.Bytes+1))
	if err != nil || n != item.Bytes || hex.EncodeToString(hash.Sum(nil)) != item.SHA256 {
		return "", errors.New("MCP 媒体成品内容已变化")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	upload, err := wechatapp.StageMediaUpload(ctx, t.native, wechatapp.MediaUpload{
		Account: in.Account, Filename: j.mcpMedia(index).Title, MIME: item.MIME, Bytes: item.Bytes, SHA256: item.SHA256,
	}, file, func() bool { _, _, allowed := t.externalMCPMediaAllowed(in, asset); return allowed })
	if err != nil {
		return "", err
	}
	if _, _, ok := t.externalMCPMediaAllowed(in, asset); !ok {
		return "", wechatapp.ErrAttachmentDenied
	}
	return upload, nil
}
