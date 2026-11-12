package station

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"wechat-bot/internal/identity"
	"wechat-bot/internal/stationservices"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

const wechatTaskOutputsFile = "task-outputs.json"

type wechatTaskOutput struct {
	Artifact string    `json:"artifact_id"`
	Account  string    `json:"account"`
	Thread   string    `json:"thread"`
	Message  string    `json:"message"`
	Added    time.Time `json:"added"`
}

func readWechatTaskOutputs(worker *stationWechatWorker) ([]wechatTaskOutput, error) {
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	raw, err := readLegacyImportFile(root, wechatTaskOutputsFile, 128<<10)
	if errors.Is(err, os.ErrNotExist) {
		return []wechatTaskOutput{}, nil
	}
	if err != nil {
		return nil, err
	}
	var items []wechatTaskOutput
	if json.Unmarshal(raw, &items) != nil || len(items) > 500 {
		return nil, errors.New("task output binding invalid")
	}
	return items, nil
}

func (s *stationWechat) bindTaskOutput(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("connection")
	var input struct {
		Artifact string `json:"artifact_id"`
	}
	if !websession.Decode(w, r, &input) || !identity.ValidPathID(id) || !identity.ValidPathID(input.Artifact) {
		websession.Fail(w, 400, "任务成品绑定参数无效")
		return
	}
	worker, detail, err := s.taskDetail(r)
	if err != nil || worker == nil {
		websession.Fail(w, 404, "任务不存在或当前连接无法核对")
		return
	}
	connection, err := s.stationWechatNative.Inspect(r.Context(), id)
	if err != nil || connection.ID != id {
		websession.Fail(w, 404, "微信连接无法核对")
		return
	}
	artifact, err := s.artifacts.ReadArtifact(input.Artifact)
	if err != nil || artifact.SourceAccount == "" || artifact.SourceAccount != connection.AccountID {
		websession.Fail(w, 404, "成品不存在或来源无法核对")
		return
	}
	if artifact.SourceThread != detail.Task.Thread {
		websession.Fail(w, 409, "成品来源与任务会话不一致")
		return
	}
	found := false
	for _, source := range detail.Sources {
		if source.MessageID == artifact.SourceMessage {
			found = true
			break
		}
	}
	if !found {
		websession.Fail(w, 409, "成品没有绑定到当前任务来源")
		return
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	items, err := readWechatTaskOutputs(worker)
	if err != nil {
		websession.Fail(w, 503, "任务成品绑定暂不可用")
		return
	}
	for _, item := range items {
		if item.Artifact == artifact.ID {
			websession.Respond(w, 200, map[string]any{"artifact": artifact.View(), "bound": true})
			return
		}
	}
	items = append(items, wechatTaskOutput{Artifact: artifact.ID, Account: artifact.SourceAccount, Thread: artifact.SourceThread, Message: artifact.SourceMessage, Added: time.Now().UTC()})
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		websession.Fail(w, 503, "任务成品绑定目录不可用")
		return
	}
	defer root.Close()
	data, _ := json.Marshal(items)
	temp := ".task-outputs-" + identity.RandomToken()
	defer root.Remove(temp)
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		_, err = f.Write(data)
		err = errors.Join(err, f.Close())
	}
	if err == nil {
		err = root.Rename(temp, wechatTaskOutputsFile)
	}
	if err != nil {
		websession.Fail(w, 503, "任务成品绑定未保存")
		return
	}
	websession.Respond(w, 200, map[string]any{"artifact": artifact.View(), "bound": true})
}

var _ stationservices.Artifact
