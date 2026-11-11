package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

type stationMemoryArchiveInput struct {
	Database    string `json:"source_database"`
	Account     string `json:"source_account"`
	Thread      string `json:"thread_id"`
	Digest      string `json:"digest"`
	SameAccount bool   `json:"same_account"`
}

type stationMemoryArchiveConversation struct {
	Thread         string `json:"thread_id"`
	Events         int    `json:"event_count"`
	Memories       int    `json:"memory_count"`
	HistoryGranted bool   `json:"history_granted"`
}

type stationMemoryArchiveView struct {
	Status        string                             `json:"status"`
	Digest        string                             `json:"digest,omitempty"`
	SourceAccount string                             `json:"source_account"`
	TargetAccount string                             `json:"target_account"`
	SourceSHA256  string                             `json:"source_sha256"`
	Conversations []stationMemoryArchiveConversation `json:"conversations"`
	Applied       bool                               `json:"applied"`
	Accounts      []string                           `json:"source_accounts,omitempty"`
}

type memoryArchiveProblem string

func (p memoryArchiveProblem) Error() string { return string(p) }

// Administrative, memory-only import. The source is an immutable file selected
// by the authenticated owner, never a model argument or a new native login.
// No queues, delivery keys, permissions, owner identities or live DB rows move.
func (s *stationWechat) memoryArchiveRoute(session *websession.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var in stationMemoryArchiveInput
		if !websession.Decode(w, r, &in) {
			return
		}
		if !filepath.IsAbs(in.Database) || in.Account != "" && !identity.ValidPathID(in.Account) || in.Thread != "" && !identity.ValidPathID(in.Thread) ||
			in.Digest != "" && (!in.SameAccount || in.Account == "" || in.Thread == "" || !legacyMemoryDigestValid(in.Digest)) {
			websession.Fail(w, 400, "请选择旧记忆库、来源账号和会话；确认时须核对同一微信账号及预览摘要")
			return
		}
		key, _, ok := session.Lookup(r)
		if !ok || s.ownerReader == nil {
			websession.Fail(w, 401, "请先登录本机主人会话")
			return
		}
		owner, err := s.ownerReader()
		if err != nil {
			websession.Fail(w, 409, "本机主人暂不可核对")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		current := func() bool {
			fresh, err := s.ownerReader()
			live, _, ok := session.Lookup(r)
			return err == nil && fresh == owner && ok && key == live && ctx.Err() == nil && s.ctx.Err() == nil
		}
		view, err := s.importMemoryArchive(ctx, r.PathValue("connection"), in, current)
		if err != nil {
			message := "旧记忆接续受阻：请核对只读源库、原账号、目标在线状态、会话历史授权及最新预览；已有记录不会覆盖"
			var problem memoryArchiveProblem
			if errors.As(err, &problem) {
				message = problem.Error()
			}
			websession.Fail(w, 409, message)
			return
		}
		if !current() {
			websession.Fail(w, 401, "主人会话已变化，结果未返回")
			return
		}
		websession.Respond(w, 200, view)
	}
}

type stationMemoryArchiveSource struct {
	root         *os.Root
	file         *os.File
	name, digest string
	info         os.FileInfo
}

func openStationMemoryArchive(ctx context.Context, database string) (*stationMemoryArchiveSource, error) {
	root, err := workspacefs.OpenPath(filepath.Dir(database))
	if err != nil {
		return nil, err
	}
	a := &stationMemoryArchiveSource{root: root, name: filepath.Base(database)}
	if !a.quiet() {
		root.Close()
		return nil, memoryArchiveProblem("旧记忆库仍有未写回的日志，请先停止旧服务并保存完整快照。不要删除日志文件。")
	}
	a.file, err = workspacefs.OpenRegular(root, a.name)
	if err != nil {
		root.Close()
		return nil, err
	}
	a.info, err = a.file.Stat()
	if err == nil && (a.info.Size() < 1 || a.info.Size() > 512<<20) {
		err = memoryArchiveProblem("所选旧记忆库为空或超过单个快照512 MiB的读取上限，请保留原件并准备分片快照。")
	}
	if err == nil {
		a.digest, err = hashArchiveSource(ctx, a.file)
	}
	if err == nil {
		err = a.check(ctx)
	}
	if err != nil {
		a.close()
		return nil, err
	}
	return a, nil
}

func (a *stationMemoryArchiveSource) close() { a.file.Close(); a.root.Close() }
func (a *stationMemoryArchiveSource) quiet() bool {
	for _, suffix := range []string{"-wal", "-journal"} {
		info, err := a.root.Lstat(a.name + suffix)
		if err == nil && (!info.Mode().IsRegular() || info.Size() != 0) || err != nil && !errors.Is(err, os.ErrNotExist) {
			return false
		}
	}
	return true
}
func hashArchiveSource(ctx context.Context, file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	_, err := io.Copy(attachmentGrantWriter{allow: func() bool { return ctx.Err() == nil }, out: hash}, file)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func (a *stationMemoryArchiveSource) check(ctx context.Context) error {
	current, err := a.root.Lstat(a.name)
	if err != nil || !sameLegacyMemoryFile(a.info, current) || !a.quiet() {
		return memoryArchiveProblem("旧记忆库在核对期间发生变化，请停止旧服务后重新预览。")
	}
	digest, err := hashArchiveSource(ctx, a.file)
	if err != nil || digest != a.digest {
		return memoryArchiveProblem("旧记忆库摘要已变化，请重新预览。")
	}
	current, err = a.root.Lstat(a.name)
	if err != nil || !sameLegacyMemoryFile(a.info, current) || !a.quiet() {
		return memoryArchiveProblem("旧记忆库在核对期间发生变化，请重新预览。")
	}
	_, err = a.file.Seek(0, io.SeekStart)
	return err
}

func (s *stationWechat) importMemoryArchive(ctx context.Context, id string, in stationMemoryArchiveInput, current func() bool) (stationMemoryArchiveView, error) {
	var view stationMemoryArchiveView
	if !current() || s.ownerReader == nil || in.Account != "" && !identity.ValidPathID(in.Account) || in.Thread != "" && !identity.ValidPathID(in.Thread) ||
		in.Digest != "" && (!in.SameAccount || in.Account == "" || in.Thread == "" || !legacyMemoryDigestValid(in.Digest)) ||
		!filepath.IsAbs(in.Database) || legacyImportOverlap(in.Database, s.dataDir) {
		return view, apphost.ErrObservationForbidden
	}
	native, err := s.WorkerSender(ctx, id)
	if err != nil || wechatapp.ConnectionID(native.Account()) != id {
		return view, apphost.ErrUnavailable
	}
	status, err := native.Status(ctx)
	if err != nil || status.AccountID != native.Account() || status.StorageMode != "managed" || wechatapp.TargetLoginState(status, native.Account()) != "online" {
		return view, apphost.ErrObservationForbidden
	}
	daemon, err := native.DaemonStatus(ctx)
	if err != nil || !wechatapp.SameNativeOwnership(ctx, native, daemon) {
		return view, apphost.ErrUnavailable
	}
	owner, err := s.ownerReader()
	if err != nil || !current() {
		return view, apphost.ErrObservationForbidden
	}
	source, err := openStationMemoryArchive(ctx, in.Database)
	if err != nil {
		return view, err
	}
	defer source.close()
	accounts, err := companion.LegacyMemoryAccounts(ctx, source.file)
	if err != nil || len(accounts) == 0 {
		return view, memoryArchiveProblem("所选旧库没有可核对身份的微信资料，请重新选择旧记忆库。")
	}
	for _, account := range accounts {
		if !identity.ValidPathID(account) {
			return view, companion.ErrLegacyMemoryUnsupported
		}
	}
	if in.Account == "" {
		if len(accounts) > 1 {
			if in.Digest != "" || in.Thread != "" {
				return view, memoryArchiveProblem("旧库含有多个账号，请先选择来源账号再预览会话。")
			}
			if err := source.check(ctx); err != nil {
				return view, err
			}
			return stationMemoryArchiveView{Status: "choose_account", TargetAccount: native.Account(), SourceSHA256: source.digest, Accounts: accounts, Conversations: []stationMemoryArchiveConversation{}}, nil
		}
		in.Account = accounts[0]
	}
	inventory, err := companion.LegacyMemoryInventory(ctx, source.file, in.Account)
	if err != nil || inventory.MatchedScopes == 0 || inventory.UnmappedScopes != 0 {
		return view, memoryArchiveProblem("无法在旧记忆库核对该本地账号编号，或存在身份不明确的会话。请使用旧资料中的账号编号。")
	}
	view = stationMemoryArchiveView{Status: "preview", SourceAccount: in.Account, TargetAccount: native.Account(), SourceSHA256: source.digest, Conversations: []stationMemoryArchiveConversation{}}
	w, err := s.worker(id, false)
	if err != nil {
		return view, err
	}
	var selection companion.LegacyMemorySelection
	for _, row := range inventory.Conversations {
		if in.Thread != "" && row.ThreadID != in.Thread {
			continue
		}
		if !identity.ValidPathID(row.ThreadID) {
			return view, errors.New("invalid source conversation")
		}
		if _, err := source.file.Seek(0, io.SeekStart); err != nil {
			return view, err
		}
		value, err := companion.ReadRetainedLegacyMemorySelection(ctx, source.file, in.Account, row.ThreadID)
		if err != nil {
			return view, err
		}
		view.Conversations = append(view.Conversations, stationMemoryArchiveConversation{Thread: row.ThreadID, Events: len(value.Events), Memories: len(value.Memories)})
		if in.Thread != "" {
			selection = value
		}
	}
	if len(view.Conversations) == 0 {
		return view, apphost.ErrNotFound
	}
	if err := source.check(ctx); err != nil {
		return view, err
	}
	if w == nil {
		if in.Thread == "" {
			return view, nil
		}
		return view, memoryArchiveProblem("请先在会话设置保存所需会话的历史读取授权，再核对此会话。")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.run != nil || w.policy.Enabled && !w.policy.Paused || !current() {
		return view, memoryArchiveProblem("请先暂停当前微信连接并等候工作器退出，再接续旧档案。")
	}
	for index := range view.Conversations {
		view.Conversations[index].HistoryGranted = !w.policy.HistoryGrants[view.Conversations[index].Thread].IsZero()
	}
	if in.Thread == "" {
		return view, nil
	}
	grant := w.policy.HistoryGrants[in.Thread]
	if grant.IsZero() || len(selection.Events) == 0 {
		return view, memoryArchiveProblem("该会话尚无有效历史读取授权，或没有可核对来源的原话。请先核对会话授权与预览数量。")
	}
	item := stationLegacyMemory{Version: 1, Account: native.Account(), Thread: in.Thread, Grant: grant, Selection: selection, SourceAccount: in.Account, SourceSHA256: source.digest}
	item.TargetDataset = daemon.ServerID
	item.TargetOwner = localCheckpointKey("memory-archive-owner-v1", s.installation, owner)
	item.Digest = localCheckpointKey("wechat-memory-only-import-v1", s.installation, owner, id, item)
	view.Digest, view.Status = localCheckpointKey("wechat-memory-only-preview-v1", item.Digest, daemon), "ready"
	if in.Digest != "" && (!in.SameAccount || in.Digest != view.Digest) {
		return view, memoryArchiveProblem("预览已失效，请重新核对会话并确认新旧资料属于同一个微信账号。")
	}
	root, err := workspacefs.OpenPath(w.dir)
	if err != nil {
		return view, err
	}
	defer root.Close()
	prior, err := readStationLegacyMemoryAt(root, item.Account, item.Thread, grant)
	if err == nil {
		if prior.Digest != item.Digest {
			return view, memoryArchiveProblem("该会话已有不同的旧档案，接续不会覆盖它。请先核对已有档案及其历史读取授权。")
		}
		view.Status, view.Applied = "applied", true
		return view, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return view, err
	}
	if in.Digest == "" {
		return view, nil
	}
	// Recheck the live clear barrier without observing any imported event.
	allowed := func() bool {
		if memoryDir, err := workspacefs.OpenExistingDirectory(root, "memory"); err == nil {
			memoryDir.Close()
			memory, err := companion.NewStore(filepath.Join(w.dir, "memory"))
			if err != nil {
				return false
			}
			allowed := memory.LegacyHistoryAllowed(item.Account, item.Thread)
			memory.Close()
			return allowed
		} else {
			return errors.Is(err, os.ErrNotExist)
		}
	}
	if !allowed() {
		return view, memoryArchiveProblem("当前会话已有遗忘记录，不能通过旧档案恢复已遗忘内容。")
	}
	if err := source.check(ctx); err != nil {
		return view, err
	}
	if !current() || !wechatapp.SameNativeOwnership(ctx, native, daemon) {
		return view, apphost.ErrObservationForbidden
	}
	// Initialize only the empty recall/forget ledger for a never-started worker.
	// Archived originals remain in their separate file, never Observe'd as new chat.
	memoryDir, err := workspacefs.EnsureDirectory(root, "memory")
	if err != nil {
		return view, err
	}
	memoryDir.Close()
	memory, err := companion.NewStore(filepath.Join(w.dir, "memory"))
	if err != nil {
		return view, err
	}
	if err := memory.Close(); err != nil {
		return view, err
	}
	filename := ".memory-archive-" + identity.RandomToken()
	defer root.Remove(filename)
	raw, err := json.Marshal(item)
	if err != nil || len(raw) >= legacyImportArchiveLimit {
		return view, memoryArchiveProblem("此会话档案超过24 MiB存储上限，原件保留；需分片接续，当前尚未支持自动分片。")
	}
	if err := writeLegacyStagedFile(root, filename, item); err != nil {
		return view, err
	}
	status, err = native.Status(ctx)
	if err != nil || status.AccountID != native.Account() || status.StorageMode != "managed" || wechatapp.TargetLoginState(status, native.Account()) != "online" || !current() || !allowed() || !wechatapp.SameNativeOwnership(ctx, native, daemon) {
		return view, apphost.ErrObservationForbidden
	}
	if err := source.check(ctx); err != nil {
		return view, err
	}
	if err := root.Link(filename, stationLegacyMemoryFile(item.Account, item.Thread)); err != nil {
		return view, err
	}
	view.Status, view.Applied = "applied", true
	return view, nil
}

// Older native-profile imports keep their original proof. New alias mappings
// additionally bind every read to this host owner and the native dataset.
func (s *stationWechat) mappedArchiveAllowed(ctx context.Context, native *linuxnative.Sender, item stationLegacyMemory) bool {
	if item.SourceAccount == "" {
		return true
	}
	if s == nil || s.ownerReader == nil || native == nil || item.Account != native.Account() {
		return false
	}
	owner, err := s.ownerReader()
	if err != nil || item.TargetOwner != localCheckpointKey("memory-archive-owner-v1", s.installation, owner) {
		return false
	}
	daemon, err := native.DaemonStatus(ctx)
	return err == nil && daemon.ServerID == item.TargetDataset && ctx.Err() == nil
}
