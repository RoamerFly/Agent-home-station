package station

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"

	"wechat-bot/internal/plugins"
	"wechat-bot/internal/workspacefs"
)

type publicExternalMediaClaim struct {
	TaskID, Registration, Tool, Fingerprint, Kind, URL string
}

func (s *localProjectTools) publicExternalMediaRoot() (*os.Root, error) {
	return workspacefs.EnsurePath(filepath.Join(stationAuthorityDir(s.home.dataDir), "external-public-media"))
}

func (s *localProjectTools) savePublicExternalMedia(taskID, registration, tool, fingerprint, kind, url string) error {
	if !validLocalRVCHex(taskID, 64) || !validLocalRVCHex(registration, 64) || !validLocalRVCHex(fingerprint, 64) ||
		url == "" || len(url) > 2048 {
		return errors.New("外部任务媒体引用无效")
	}
	claim := publicExternalMediaClaim{TaskID: taskID, Registration: registration, Tool: tool, Fingerprint: fingerprint, Kind: kind, URL: url}
	raw, err := json.Marshal(claim)
	if err != nil {
		return err
	}
	root, err := s.publicExternalMediaRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.OpenFile(taskID+".json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		file, err = workspacefs.OpenRegular(root, taskID+".json")
		if err != nil {
			return err
		}
		defer file.Close()
		stored, err := io.ReadAll(io.LimitReader(file, 8<<10))
		if err != nil || string(stored) != string(raw) {
			return errors.New("外部任务媒体引用已变化")
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = file.Write(raw)
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		_ = root.Remove(taskID + ".json")
	}
	return err
}

func (s *localProjectTools) publicExternalTaskMedia(ctx context.Context, fish, registration, name, taskID, fingerprint string) (plugins.MediaResult, error) {
	return s.publicExternalMedia(ctx, fish, registration, name, taskID, fingerprint, true)
}

func (s *localProjectTools) publicExternalHTTPMedia(ctx context.Context, fish, registration, name, taskID, fingerprint string) (plugins.MediaResult, error) {
	return s.publicExternalMedia(ctx, fish, registration, name, taskID, fingerprint, false)
}

func (s *localProjectTools) publicExternalMedia(ctx context.Context, fish, registration, name, taskID, fingerprint string, task bool) (plugins.MediaResult, error) {
	var empty plugins.MediaResult
	if !validLocalRVCHex(taskID, 64) {
		return empty, errors.New("外部媒体任务编号无效")
	}
	host, err := s.forFish(fish)
	if err != nil {
		return empty, err
	}
	installed, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localExternalPackage})
	if err != nil || !slices.Contains(installed.Permissions, "network") {
		return empty, errors.New("工作站外部工具未启用或缺少联网授权")
	}
	configuration, err := host.store.Configuration(ctx, localExternalPackage)
	if err != nil {
		return empty, err
	}
	raw, err := s.externalRegistration()
	if err != nil || externalDigest(raw) != registration {
		return empty, errors.New("工作站外部工具登记已变化")
	}
	_, bridge, err := externalBridgeRegistration(raw)
	if err != nil {
		return empty, err
	}
	var selected plugins.ExternalProtocolTool
	if task {
		definition, taskErr := bridge.Task(name)
		if taskErr != nil {
			return empty, errors.New("工作站媒体工具声明已变化")
		}
		selected = definition.Protocol.Tool
	} else {
		catalog := bridge.Protocol().Tools
		index := slices.IndexFunc(catalog, func(tool plugins.ExternalProtocolTool) bool { return tool.Name == name })
		if index < 0 {
			return empty, errors.New("工作站媒体工具声明已变化")
		}
		selected = catalog[index]
	}
	if selected.Fingerprint != fingerprint {
		return empty, errors.New("工作站媒体工具声明已变化")
	}
	root, err := s.publicExternalMediaRoot()
	if err != nil {
		return empty, err
	}
	defer root.Close()
	file, err := workspacefs.OpenRegular(root, taskID+".json")
	if err != nil {
		return empty, err
	}
	defer file.Close()
	claimRaw, err := io.ReadAll(io.LimitReader(file, 8<<10))
	var claim publicExternalMediaClaim
	if err != nil || len(claimRaw) >= 8<<10 || json.Unmarshal(claimRaw, &claim) != nil ||
		claim.TaskID != taskID || claim.Registration != registration || claim.Tool != name || claim.Fingerprint != fingerprint ||
		claim.Kind != selected.MediaKind {
		return empty, errors.New("外部任务媒体引用不可核验")
	}
	media, err := bridge.FetchResultMedia(ctx, name, claim.URL)
	if err != nil {
		return empty, err
	}
	current, currentErr := installedLocalProject(ctx, host, localProjectOperation{pkg: localExternalPackage})
	configNow, configErr := host.store.Configuration(ctx, localExternalPackage)
	rawNow, rawErr := s.externalRegistration()
	if currentErr != nil || configErr != nil || rawErr != nil || current.Revision != installed.Revision ||
		configNow.Revision != configuration.Revision || externalDigest(rawNow) != registration || ctx.Err() != nil {
		return empty, errors.New("外部媒体领取期间授权或登记已变化")
	}
	return media, nil
}
