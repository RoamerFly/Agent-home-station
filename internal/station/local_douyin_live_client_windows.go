//go:build windows

package station

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"wechat-bot/internal/fishidentity"
	"wechat-bot/internal/nativeclient"
	"wechat-bot/internal/websession"
)

const douyinLiveClientInspectRoute = "/api/local/apps/douyin/live-client/inspect"

// Inspection is a private owner action, separate from the website login and
// from LiveProvider. Even a trusted main executable does not establish an
// account, capture readiness or permission to begin a broadcast.
func (h *localHomeService) douyinLiveClientRoutes(owner *websession.Store, mux *http.ServeMux, service context.Context) {
	mux.HandleFunc("POST "+douyinLiveClientInspectRoute, owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		identity, err := h.identity.Read()
		if err != nil || identity.LocalOwner == nil || !fishidentity.ValidFishID(identity.FishID) {
			websession.Fail(w, http.StatusConflict, "请先准备本机主人身份")
			return
		}
		var in struct {
			Executable string `json:"executable"`
			Discover   bool   `json:"discover"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		if !websession.Decode(w, r, &in) {
			return
		}
		if len(in.Executable) > 4096 || in.Discover == (strings.TrimSpace(in.Executable) != "") {
			websession.Fail(w, http.StatusBadRequest, "请选择已安装的直播伴侣主程序 EXE")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		stop := context.AfterFunc(service, cancel)
		defer stop()
		if in.Discover {
			candidates, err := discoverDouyinLiveCompanion(ctx, douyinInstalledRoots(), nativeclient.Inspect)
			if err != nil {
				websession.Fail(w, http.StatusRequestTimeout, "本机检查未完成，可稍后重新检查")
				return
			}
			if len(candidates) != 1 {
				reason := "没有找到可验证的直播伴侣主程序。可在指定程序路径中手动核对。"
				if len(candidates) > 1 {
					reason = "找到多个已验证的版本。可在指定程序路径中选择要核对的主程序。"
				}
				websession.Respond(w, http.StatusOK, map[string]any{"state": "manual_selection", "control_available": false, "reason": reason})
				return
			}
			candidate := candidates[0]
			writeDouyinLiveInspection(w, ctx, candidate.path, candidate.file, nil)
			return
		}
		info, err := nativeclient.Inspect(ctx, in.Executable)
		switch {
		case err == nil || errors.Is(err, nativeclient.ErrVersion):
			writeDouyinLiveInspection(w, ctx, in.Executable, info, err)
		case errors.Is(err, nativeclient.ErrInput):
			websession.Fail(w, http.StatusBadRequest, "程序路径无效，请选择本机已安装的 EXE 文件")
		case errors.Is(err, nativeclient.ErrInvalid):
			websession.Fail(w, http.StatusUnprocessableEntity, "该文件不是可核对的 Windows 程序，请勿选择快捷方式或 DLL")
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			websession.Fail(w, http.StatusRequestTimeout, "程序检查未完成，可在文件可用后重新检查")
		default:
			websession.Fail(w, http.StatusServiceUnavailable, "无法读取程序，请检查文件是否存在、是否被占用及本机访问权限")
		}
	}))
}

func writeDouyinLiveInspection(w http.ResponseWriter, ctx context.Context, path string, info nativeclient.Info, err error) {
	state := "inspected"
	identity, reason := douyinLiveClientIdentity(info)
	if err != nil {
		state = "metadata_missing"
		reason = "版本资料不完整。请选择已安装的直播伴侣主程序。"
	}
	process := "unavailable"
	if err == nil && identity == "main" {
		process = douyinLiveProcessState(ctx, path)
	}
	websession.Respond(w, http.StatusOK, map[string]any{"state": state, "file": info, "client_identity": identity,
		"process_state": process, "observed_at": time.Now().UTC(), "control_available": false, "reason": reason})
}

func douyinLiveClientIdentity(info nativeclient.Info) (string, string) {
	signature := info.Signature
	switch signature.State {
	case "unsigned":
		return "unverified", "此程序没有可验证的 Authenticode 签名。请选择官方安装的直播伴侣主程序。"
	case "untrusted":
		return "unverified", "Windows 拒绝信任此程序的签名。"
	case "trusted":
	default:
		return "unverified", "本机信任缓存不足或无法核对签名，发布者尚未确认。"
	}
	const publisher = "Douyin Vision Co., Ltd."
	if !strings.EqualFold(signature.Publisher, publisher) || !strings.EqualFold(signature.Organization, publisher) {
		return "other_publisher", "Windows 已核验此文件的签名。签名发布者与已知直播伴侣发布者不匹配。"
	}
	if info.ProductName == "直播伴侣" {
		switch info.FileDescription {
		case "直播伴侣":
			if info.Architecture == "x64" {
				return "main", "已核验直播伴侣主程序。账号登录、画面输入及直播控制仍待核对。"
			}
		case "直播伴侣 Launcher":
			return "launcher", "已核验直播伴侣启动器。继续检查时请选择版本目录里的主程序。"
		}
	}
	return "unrecognized_binary", "签名发布者匹配，但文件的程序类型未识别。请选择直播伴侣主程序。"
}
