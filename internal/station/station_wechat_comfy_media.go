package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mediadecode"
	"wechat-bot/internal/workspacefs"
)

func (t *stationAttachmentTools) saveComfyProducts(ctx context.Context, j *stationWechatComfyJob, result publicComfyProduct) error {
	if len(result.Images) == 0 && len(result.Video.Data) == 0 {
		return errors.New("ComfyUI 成品为空")
	}
	if len(result.Images) != 0 && len(result.Video.Data) != 0 {
		return errors.New("ComfyUI 成品类型冲突")
	}
	root, err := workspacefs.EnsurePath(filepath.Join(t.dir, "comfy-jobs", j.ID))
	if err != nil {
		return err
	}
	defer root.Close()
	products := make([]stationWechatComfyProduct, 0, len(result.Images)+1)
	for i, image := range result.Images {
		name := "output-" + fmtComfyIndex(i) + ".png"
		if err := writeComfyProduct(root, name, image.Data, image.SHA256); err != nil {
			return err
		}
		products = append(products, stationWechatComfyProduct{Name: fmt.Sprintf("大肥鱼作品-%02d.png", i+1), Kind: "image", MIME: "image/png", SHA256: image.SHA256, Bytes: int64(len(image.Data))})
	}
	if len(result.Video.Data) != 0 {
		name := "output-00.mp4"
		if err := writeComfyProduct(root, name, result.Video.Data, result.Video.SHA256); err != nil {
			return err
		}
		path := filepath.Join(t.dir, "comfy-jobs", j.ID, name)
		meta, err := mediadecode.Probe(ctx, path)
		if err != nil || meta.Width < 1 || meta.Height < 1 || !localComfyFinite(meta.FPS) || meta.FPS <= 0 {
			return errors.New("ComfyUI 视频成品无法解码")
		}
		frames, _, err := mediadecode.ExtractFramesSized(ctx, path, filepath.Join(t.dir, "comfy-jobs", j.ID), 0, 1, 1, 64)
		if err != nil || len(frames) != 1 {
			return errors.New("ComfyUI 视频首帧无法读取")
		}
		_ = root.Remove("frame-000001.jpg")
		products = append(products, stationWechatComfyProduct{Name: "大肥鱼作品.mp4", Kind: "video", MIME: "video/mp4", SHA256: result.Video.SHA256, Bytes: int64(len(result.Video.Data))})
	}
	if !t.comfyJobAllowed(t.trace, *j) {
		return wechatapp.ErrAttachmentDenied
	}
	j.Products = products
	return nil
}

func writeComfyProduct(root *os.Root, name string, data []byte, expected string) error {
	if len(data) == 0 || len(data) > 64<<20 || expected == "" {
		return errors.New("ComfyUI 成品无效")
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != expected {
		return errors.New("ComfyUI 成品摘要不符")
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		existing, openErr := workspacefs.OpenRegular(root, name)
		if openErr != nil {
			return openErr
		}
		defer existing.Close()
		actual := sha256.New()
		n, readErr := io.Copy(actual, io.LimitReader(existing, int64(len(data))+1))
		if readErr != nil || n != int64(len(data)) || hex.EncodeToString(actual.Sum(nil)) != expected {
			return errors.New("ComfyUI 已有成品与任务不一致")
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		_ = root.Remove(name)
		return err
	}
	return nil
}

func (t *stationAttachmentTools) stageComfy(ctx context.Context, in companion.Input, kind, asset string) (string, error) {
	j, index, ok := t.comfyMediaAllowed(in, asset)
	if !ok || j.Products[index].Kind != kind {
		return "", wechatapp.ErrAttachmentDenied
	}
	root, err := t.comfyJobRoot(j.ID)
	if err != nil {
		return "", err
	}
	defer root.Close()
	ext := ".png"
	if kind == "video" {
		ext = ".mp4"
	}
	file, err := workspacefs.OpenRegular(root, "output-"+fmtComfyIndex(index)+ext)
	if err != nil {
		return "", err
	}
	defer file.Close()
	product := j.Products[index]
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, product.Bytes+1))
	if err != nil || n != product.Bytes || hex.EncodeToString(hash.Sum(nil)) != product.SHA256 {
		return "", errors.New("ComfyUI 成品内容已变化")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return wechatapp.StageMediaUpload(ctx, t.native, wechatapp.MediaUpload{
		Account: in.Account, Filename: product.Name, MIME: product.MIME, Bytes: product.Bytes, SHA256: product.SHA256,
	}, file, func() bool { _, _, allowed := t.comfyMediaAllowed(in, asset); return allowed })
}
