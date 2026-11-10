package mediaobserve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	"wechat-bot/internal/pluginhost"
)

// SourceCheck revalidates application authority and the selected media tool
// without retaining the tool's configuration values or secret material.
type SourceCheck func(context.Context) error

func BindSource(store *pluginhost.Store, installed pluginhost.InstalledPackage, configurationRevision int, origin SourceCheck) SourceCheck {
	tool, revision, digest := installed.Package.ID, installed.Revision, installed.Digest
	return func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if origin == nil || store == nil {
			return NewFailure("media_source_check_unavailable", "无法核对本次媒体来源和授权。", nil)
		}
		if err := origin(ctx); err != nil {
			return err
		}
		p, err := store.Installed(ctx, tool)
		if err != nil || !p.Enabled || p.Removed || p.Revision != revision || p.Digest != digest {
			return NewFailure("media_plugin_version_changed", "读取期间媒体工具版本、授权或启用状态已变更。", nil)
		}
		config, err := store.Configuration(ctx, tool)
		if err != nil || config.Revision != configurationRevision {
			return NewFailure("media_plugin_configuration_changed", "读取期间媒体理解配置已变更。", nil)
		}
		return ctx.Err()
	}
}

func (check SourceCheck) CheckJob(ctx context.Context, _ pluginhost.Job, _ pluginhost.Capability) error {
	return check(ctx)
}

var ErrSourceIntegrity = errors.New("media source length or checksum mismatch")

// CopyVerifiedSource copies only the expected read-only bytes into a
// caller-owned private destination. The cheap local grant is checked during
// the copy and again before the caller may use the verified result.
func CopyVerifiedSource(ctx context.Context, dst io.Writer, src io.Reader, size int64, sha string, grant func() error) error {
	if size <= 0 || grant == nil {
		return ErrSourceIntegrity
	}
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return grant()
	}
	if err := check(); err != nil {
		return err
	}
	hash := sha256.New()
	writer := sourceWriter{check: check, dst: io.MultiWriter(dst, hash)}
	n, err := io.Copy(writer, io.LimitReader(src, size))
	if err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	if n != size || hex.EncodeToString(hash.Sum(nil)) != sha {
		return ErrSourceIntegrity
	}
	var extra [1]byte
	_, err = io.ReadFull(src, extra[:])
	if err == nil {
		return ErrSourceIntegrity
	}
	if !errors.Is(err, io.EOF) {
		return err
	}
	return check()
}

type sourceWriter struct {
	check func() error
	dst   io.Writer
}

func (w sourceWriter) Write(p []byte) (int, error) {
	if err := w.check(); err != nil {
		return 0, err
	}
	return w.dst.Write(p)
}
