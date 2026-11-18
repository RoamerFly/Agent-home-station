package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"wechat-bot/internal/deepseek"
)

func (t *localToolSet) inspectDirectMedia(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Artifact string `json:"artifact_id"`
		Layout   bool   `json:"include_layout"`
		Pages    []int  `json:"pages"`
	}
	if json.Unmarshal(raw, &in) != nil || in.Artifact == "" || t.run == nil || !t.run.Tracked || t.access == nil {
		return "", errors.New("需要本轮授权及有效素材编号")
	}
	if err := t.run.CheckSource(ctx); err != nil {
		return "", err
	}
	grant, err := t.access.resolve()
	if err != nil {
		return "", err
	}
	bound, err := t.access.home.artifacts.bindProjectArtifact(ctx, in.Artifact, t.access, grant)
	if err != nil {
		return "", err
	}
	if bound.artifact.Bytes > 100<<20 {
		return "", errors.New("原文件超过提供方100MiB上限")
	}
	f, err := bound.store.open(ctx, bound.artifact, func() bool { return ctx.Err() == nil && bound.access.current(bound.grant) })
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (100<<20)+1))
	sum := sha256.Sum256(data)
	if err != nil || hex.EncodeToString(sum[:]) != bound.artifact.SHA256 || bound.check(ctx) != nil {
		return "", errors.New("原件内容或授权已变化")
	}
	reused := false
	if strings.HasPrefix(http.DetectContentType(data), "image/") {
		err = deepseek.AttachImage(ctx, bound.artifact.Name, deepseek.Image{Data: data})
	} else {
		// XLSX pictures are part of the workbook's visual content.  Always use
		// the rich path for workbooks so an ordinary document read cannot silently
		// omit embedded product images when the model did not request layout mode.
		rich := in.Layout || strings.EqualFold(filepath.Ext(bound.artifact.Name), ".xlsx")
		reused, err = t.directMedia.AttachDocument(ctx, bound.artifact.Name, data, rich)
	}
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(map[string]any{"attached": true, "mode": "deepseek_original_media", "upload_reused": reused, "artifact_id": in.Artifact, "requested_pages": in.Pages, "coverage": "original_file", "instruction": "原图或原文档将直接出现在下一次模型请求中。pages仅表示重点页；原文件未截取。需要复查时再次调用，不自动重复携带。"})
	return string(b), nil
}
