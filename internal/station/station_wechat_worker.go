package station

import (
	"context"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/stationservices"
	"wechat-bot/internal/workspacefs"
)

type stationWechatNative interface {
	apphost.Provider
	apphost.AccountSetup
	WorkerSender(context.Context, string) (*linuxnative.Sender, error)
}

type stationWechat struct {
	stationWechatNative
	root, installation     string
	dataDir, workspaceRoot string
	modelPersona           func() (string, error)
	model                  companion.LanguageModel
	ctx                    context.Context
	lifecycle              *wechatapp.WorkerLifecycle
	mu                     sync.Mutex
	workers                map[string]*stationWechatWorker
	platformFences         map[string]*sync.RWMutex
	artifacts              stationservices.Artifacts
	shareOptions           pluginruntime.Options
	localASR               stationLocalASR
	localVision            stationLocalVision
	documents              stationDocumentTools
	ownerReader            stationservices.OwnerReader
	stationConversation    func() (*companion.Store, companion.Input, error)
	localChat              *localChatService
	contactCursor          cipher.AEAD
}

type stationWechatPolicy struct {
	Enabled bool `json:"enabled"`
	Paused  bool `json:"paused"`
	// Behavior is the owner-controlled legacy bot policy.  Keep it as a
	// pointer so an explicit all-false configuration is distinguishable from
	// an old policy file that predates this field; the adapter supplies the
	// legacy defaults for the latter.
	Behavior           *bot.Options                   `json:"behavior,omitempty"`
	Grants             map[string]time.Time           `json:"reply_grants"`
	AttachmentGrants   map[string]time.Time           `json:"attachment_grants,omitempty"`
	StationLocalAudio  bool                           `json:"station_local_audio,omitempty"`
	StationVideoVision bool                           `json:"station_video_vision,omitempty"`
	StationImageVision bool                           `json:"station_image_vision,omitempty"`
	AutoImagesSince    time.Time                      `json:"auto_images_since,omitzero"`
	AutoVideosSince    time.Time                      `json:"auto_videos_since,omitzero"`
	MediaReplyGrants   map[string]time.Time           `json:"media_reply_grants,omitempty"`
	HistoryGrants      map[string]time.Time           `json:"history_grants,omitempty"`
	ContactGrant       time.Time                      `json:"contact_grant,omitzero"`
	GroupMuteGrants    map[string]time.Time           `json:"group_mute_grants,omitempty"`
	GroupRules         map[string]wechatapp.GroupRule `json:"group_rules,omitempty"`
	// Persist the import claim with its note so retrying cannot restore a
	// note the owner later edited, cleared, or lost by revoking its scope.
	LegacyGroupNoteImports map[string]string `json:"legacy_group_note_imports,omitempty"`
	// Persist the reply-policy import claim independently. This prevents a
	// replay from restoring an old trigger or persona after an owner edit.
	LegacyGroupReplyImports map[string]string `json:"legacy_group_reply_imports,omitempty"`
	// Imported workers require a local old-host/native-profile attestation
	// before they may start or submit any automated reply.
	RelocationActivation *legacyRelocationActivation `json:"relocation_activation,omitempty"`
	LegacyActivation     *legacyActivation           `json:"legacy_activation,omitempty"`
	// Older imports may only have the archive digest. It is still sufficient to
	// require the same activation gate until the connection is explicitly bound.
	LegacyArchiveDigest string    `json:"legacy_archive_digest,omitempty"`
	RollbackFrozenAt    time.Time `json:"rollback_frozen_at,omitzero"`
}

func (policy stationWechatPolicy) groupPolicy() wechatapp.GroupPolicy {
	return wechatapp.GroupPolicy{Replies: policy.Grants, Attachments: policy.AttachmentGrants, Rules: policy.GroupRules}
}

const stationWechatPolicyLimit = 128 << 10

type stationWechatWorker struct {
	canvasMu         sync.Mutex
	connectionID     string
	host             *stationWechat
	account          string
	roundMu          sync.Mutex
	mu               sync.Mutex
	deliveryMu       sync.Mutex
	surfMu           sync.Mutex
	surfShareMu      sync.Mutex
	surfShareGrantMu sync.Mutex
	surfAppsMu       sync.Mutex
	ownerMu          sync.Mutex
	crossDeliveryMu  sync.Mutex
	forwardMu        sync.Mutex
	surfCancel       context.CancelFunc
	rollbackAuditMu  sync.Mutex
	shareMu          sync.Mutex
	speechMu         sync.Mutex
	share            *stationShareHost
	dir              string
	runtimeDir       string
	policy           stationWechatPolicy
	actions          *wechatapp.ActionLedger
	run              *wechatapp.WorkerRun
	// cutoverBlocked records a failed automatic preflight for older imported
	// archives that predate LegacyArchiveDigest. It is intentionally transient:
	// the archive evidence remains the authority and an explicit successful
	// activation clears this display state.
	cutoverBlocked atomic.Bool
}

func (s *stationWechat) addBoundOwnerReplyGrant(connection, account string, policy stationWechatPolicy) stationWechatPolicy {
	state, err := s.wechatOwnerState(connection, account)
	grants := make(map[string]time.Time, len(policy.Grants)+1)
	for thread, granted := range policy.Grants {
		grants[thread] = granted
	}
	if err == nil && state.Binding != nil && !state.Binding.BoundAt.IsZero() {
		ownerInput := companion.Input{Channel: wechatapp.ID, Account: account, Thread: state.Binding.Sender, Sender: state.Binding.Sender, Directed: true, Mode: "direct"}
		if s.ownerBoundToWechat(connection, account, ownerInput) && (grants[state.Binding.Sender].IsZero() || grants[state.Binding.Sender].Before(state.Binding.BoundAt)) {
			grants[state.Binding.Sender] = state.Binding.BoundAt
		}
	}
	// Older single-account releases have a trusted legacy agent-config.json
	// with owner_ids but no newer owner-binding.json yet. Preserve that
	// explicit owner authorization during cutover so the owner's private chat
	// does not get treated as an ordinary unlisted contact. Other contacts
	// remain subject to explicit reply grants.
	for _, sender := range s.legacyOwnerReplyIDs() {
		if grants[sender].IsZero() {
			grants[sender] = time.Unix(0, 0).UTC()
		}
	}
	policy.Grants = grants
	return policy
}

func (s *stationWechat) legacyOwnerReplyIDs() []string {
	if s == nil || s.dataDir == "" {
		return nil
	}
	path := filepath.Join(s.dataDir, "applications", wechatapp.ID, "legacy-admin", "agent-config.json")
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 8<<20 {
		return nil
	}
	var cfg struct {
		Enabled  bool     `json:"enabled"`
		OwnerIDs []string `json:"owner_ids"`
	}
	if json.Unmarshal(raw, &cfg) != nil || !cfg.Enabled || len(cfg.OwnerIDs) > 30 {
		return nil
	}
	seen := make(map[string]bool, len(cfg.OwnerIDs))
	ids := make([]string, 0, len(cfg.OwnerIDs))
	for _, id := range cfg.OwnerIDs {
		id = strings.TrimSpace(id)
		if id != "" && identity.ValidPathID(id) && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *stationWechat) ownerBoundToWechat(connection, account string, in companion.Input) bool {
	if s == nil || s.ownerReader == nil || in.Account != account || in.Sender == "" {
		return false
	}
	owner, err := s.ownerReader()
	if err != nil || owner.FishID == "" || owner.ID == "" {
		return false
	}
	state, err := s.wechatOwnerState(connection, account)
	return err == nil && state.Binding != nil && state.Binding.Sender == in.Sender && state.Binding.Fish == owner.FishID && state.Binding.Owner == owner.ID
}

func bindStationWechat(registry apphost.Registry, dataDir, workspaceRoot string, model stationservices.Model, artifacts stationservices.Artifacts, shareOptions pluginruntime.Options, localASR stationLocalASR, localVision stationLocalVision, documents stationDocumentTools) (apphost.Registry, *stationWechat, error) {
	entry, ok := registry.Lookup(wechatapp.ID)
	native, supported := entry.Provider.(stationWechatNative)
	if !ok || !supported || entry.Registration != "station-native" {
		return registry, nil, nil
	}
	host := &stationWechat{
		stationWechatNative: native, root: filepath.Join(stationApplicationsDir(dataDir), wechatapp.ID, "workers"),
		dataDir: dataDir, workspaceRoot: workspaceRoot,
		installation: entry.InstallationID, modelPersona: model.Persona, model: model.Client,
		workers:   map[string]*stationWechatWorker{},
		artifacts: artifacts, shareOptions: shareOptions, localASR: localASR, localVision: localVision, documents: documents,
	}
	host.lifecycle = wechatapp.NewWorkerLifecycle(host.closeWorkerResources)
	host.ctx = host.lifecycle.Context()
	var err error
	host.contactCursor, err = newStationContactCursor()
	if err != nil {
		host.lifecycle.BeginShutdown()
		return registry, nil, err
	}
	registrations := make([]apphost.Registration, 0, len(registry.Applications()))
	for _, app := range registry.Applications() {
		item, _ := registry.Lookup(app.ID)
		if app.ID == entry.ID {
			item.Provider = host
		}
		registrations = append(registrations, item)
	}
	bound, err := apphost.New(registrations...)
	if err != nil {
		host.lifecycle.BeginShutdown()
		return registry, nil, err
	}
	return bound, host, nil
}

func (s *stationWechat) closeWorkerResources() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, worker := range s.workers {
		worker.shareMu.Lock()
		if worker.share != nil {
			_ = worker.share.controller.Close()
			_ = worker.share.store.Close()
		}
		worker.shareMu.Unlock()
	}
}

func (s *stationWechat) restore() {
	// In legacy-console mode the old admin.Manager owns the same native
	// account and checkpoint. Starting the station worker as well would make
	// two bots race on one account, so the station keeps the provider/UI only.
	if os.Getenv("AHS_LEGACY_WECHAT") == "1" {
		return
	}
	s.lifecycle.Restore(s.Connections, s.restoreWorker)
}

func (s *stationWechat) restoreWorker(id string) {
	w, err := s.worker(id, false)
	if err != nil || w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.policy.Enabled || w.policy.Paused || w.run != nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	defer cancel()
	native, err := s.WorkerSender(ctx, id)
	if err == nil {
		guard, guardErr := s.legacyActivationGuard(ctx, id, native, w)
		if guardErr != nil {
			w.cutoverBlocked.Store(true)
			return
		}
		w.cutoverBlocked.Store(false)
		_ = s.launch(w, native, guard)
	}
}

// The fence belongs to the connection, including writes admitted before its
// worker exists. Rollback waits for those requests and newly admitted ones.
func (s *stationWechat) platformFence(id string) *sync.RWMutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	fence := s.platformFences[id]
	if fence == nil {
		if s.platformFences == nil {
			s.platformFences = make(map[string]*sync.RWMutex)
		}
		fence = new(sync.RWMutex)
		s.platformFences[id] = fence
	}
	return fence
}

func (s *stationWechat) worker(id string, create bool) (*stationWechatWorker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return nil, apphost.ErrLifecycleConflict
	}
	if w := s.workers[id]; w != nil {
		return w, nil
	}
	dir := filepath.Join(s.root, id)
	root, err := workspacefs.OpenPath(dir)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return nil, nil
		}
		root, err = workspacefs.EnsurePath(dir)
	}
	if err != nil {
		return nil, apphost.ErrUnavailable
	}
	defer root.Close()
	policy, err := readStationWechatPolicy(root)
	if err != nil {
		return nil, apphost.ErrUnavailable
	}
	runtimeDir, err := s.relocationRuntime(root, id)
	if err != nil {
		return nil, err
	}
	w := &stationWechatWorker{connectionID: id, host: s, dir: dir, runtimeDir: runtimeDir, policy: policy}
	s.workers[id] = w
	return w, nil
}

func (w *stationWechatWorker) runtimeStateDir() string {
	if w.runtimeDir != "" {
		return w.runtimeDir
	}
	return w.dir
}

func (w *stationWechatWorker) queueDir() string {
	return filepath.Join(w.runtimeStateDir(), "queue")
}

func readStationWechatPolicy(root *os.Root) (stationWechatPolicy, error) {
	const name = "worker.json"
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return stationWechatPolicy{}, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return stationWechatPolicy{}, apphost.ErrUnavailable
	}
	file, err := root.Open(name)
	if err != nil {
		return stationWechatPolicy{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, stationWechatPolicyLimit+1))
	var policy stationWechatPolicy
	if err != nil || len(data) > stationWechatPolicyLimit || json.Unmarshal(data, &policy) != nil || len(policy.Grants) > 100 || len(policy.GroupMuteGrants) > 100 || len(policy.GroupRules) > 100 {
		return stationWechatPolicy{}, apphost.ErrInvalidConnectionPolicy
	}
	if policy.LegacyActivation != nil && !policy.LegacyActivation.valid() || policy.LegacyArchiveDigest != "" && !legacyMemoryDigestValid(policy.LegacyArchiveDigest) {
		return stationWechatPolicy{}, apphost.ErrInvalidConnectionPolicy
	}
	if a := policy.RelocationActivation; a != nil && (!a.valid() || policy.LegacyActivation == nil) {
		return stationWechatPolicy{}, apphost.ErrInvalidConnectionPolicy
	}
	if !policy.RollbackFrozenAt.IsZero() && (!policy.Paused || policy.LegacyActivation == nil) {
		return stationWechatPolicy{}, apphost.ErrInvalidConnectionPolicy
	}
	for thread, granted := range policy.Grants {
		if !identity.ValidPathID(thread) || granted.IsZero() {
			return stationWechatPolicy{}, apphost.ErrInvalidConnectionPolicy
		}
	}
	for thread, granted := range policy.AttachmentGrants {
		if policy.Grants[thread].IsZero() || granted.IsZero() || granted.Before(policy.Grants[thread]) {
			return stationWechatPolicy{}, apphost.ErrInvalidConnectionPolicy
		}
	}
	for thread, granted := range policy.HistoryGrants {
		if policy.Grants[thread].IsZero() || granted.IsZero() || granted.Before(policy.Grants[thread]) {
			return stationWechatPolicy{}, apphost.ErrInvalidConnectionPolicy
		}
	}
	for thread, granted := range policy.MediaReplyGrants {
		if policy.AttachmentGrants[thread].IsZero() || granted.IsZero() || granted.Before(policy.AttachmentGrants[thread]) {
			return stationWechatPolicy{}, apphost.ErrInvalidConnectionPolicy
		}
	}
	for group, granted := range policy.GroupMuteGrants {
		if !wechatapp.ValidGroup(group) || granted.IsZero() {
			return stationWechatPolicy{}, apphost.ErrInvalidConnectionPolicy
		}
	}
	for thread, rule := range policy.GroupRules {
		if !wechatapp.ValidGroupRule(thread, rule, policy.Grants, policy.AttachmentGrants) {
			return stationWechatPolicy{}, apphost.ErrInvalidConnectionPolicy
		}
	}
	for thread, digest := range policy.LegacyGroupNoteImports {
		if !wechatapp.ValidGroup(thread) || !legacyMemoryDigestValid(digest) {
			return stationWechatPolicy{}, apphost.ErrInvalidConnectionPolicy
		}
	}
	for thread, digest := range policy.LegacyGroupReplyImports {
		if !wechatapp.ValidGroup(thread) || !legacyMemoryDigestValid(digest) {
			return stationWechatPolicy{}, apphost.ErrInvalidConnectionPolicy
		}
	}
	return policy, nil
}

func (w *stationWechatWorker) save(policy stationWechatPolicy) error {
	data, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	if len(data) > stationWechatPolicyLimit {
		return apphost.ErrInvalidConnectionPolicy
	}
	root, err := workspacefs.OpenPath(w.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	// Use the pinned private directory for policy reads and reject redirects.
	if info, err := root.Lstat("worker.json"); err == nil && !info.Mode().IsRegular() || err != nil && !errors.Is(err, os.ErrNotExist) {
		return apphost.ErrUnavailable
	}
	temp := ".worker-" + identity.RandomToken()
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	if err := root.Rename(temp, "worker.json"); err != nil {
		return err
	}
	w.policy = policy
	return nil
}

func (w *stationWechatWorker) state() string {
	if w.run != nil {
		if state := w.run.Status(w.policy.Paused); state != "" {
			return state
		}
	}
	switch {
	case !w.policy.RollbackFrozenAt.IsZero():
		return "rollback_frozen"
	case w.policy.Paused:
		return "paused"
	case w.cutoverBlocked.Load():
		return "cutover_blocked"
	case w.policy.Enabled && (w.policy.LegacyActivation != nil || w.policy.LegacyArchiveDigest != ""):
		// Automatic restore deliberately leaves imported connections dormant when
		// their old-host evidence cannot be verified.
		return "cutover_blocked"
	case w.policy.Enabled:
		return "needs_setup"
	default:
		return "disabled"
	}
}

func (w *stationWechatWorker) stop(ctx context.Context) error {
	if w.run == nil {
		return nil
	}
	if err := w.run.Drain(ctx); err != nil {
		return err
	}
	w.run = nil
	return nil
}

func (w *stationWechatWorker) policyView() *apphost.ConfiguredPolicy {
	threads := make([]string, 0, len(w.policy.Grants))
	for thread := range w.policy.Grants {
		threads = append(threads, thread)
	}
	slices.Sort(threads)
	grants := []string{}
	if len(threads) != 0 {
		grants = append(grants, "send.text")
	}
	attachments := slices.Sorted(maps.Keys(w.policy.AttachmentGrants))
	if len(attachments) > 0 {
		grants = append(grants, "media.read")
	}
	mediaReplies := slices.Sorted(maps.Keys(w.policy.MediaReplyGrants))
	history := slices.Sorted(maps.Keys(w.policy.HistoryGrants))
	if len(history) > 0 {
		grants = append(grants, "messages.read", "members.read")
	}
	if len(mediaReplies) > 0 {
		grants = append(grants, "send.image", "send.voice", "send.video", "send.file")
	}
	contactRead := !w.policy.ContactGrant.IsZero()
	if contactRead {
		grants = append(grants, "contacts.read")
	}
	groupMute := slices.Sorted(maps.Keys(w.policy.GroupMuteGrants))
	if len(groupMute) > 0 {
		grants = append(grants, "account.mute")
	}
	groupRules := make([]apphost.ConnectionGroupRule, 0, len(w.policy.GroupRules))
	for _, thread := range slices.Sorted(maps.Keys(w.policy.GroupRules)) {
		groupRules = append(groupRules, w.policy.GroupRules[thread].View(thread))
	}
	return &apphost.ConfiguredPolicy{WorkerEnabled: w.policy.Enabled, ConversationEnabled: len(threads) != 0, ToolsEnabled: len(attachments) > 0 || len(history) > 0 || contactRead || len(groupMute) > 0, Grants: grants, ReplyThreads: threads, AttachmentThreads: attachments, AutoObserveImages: !w.policy.AutoImagesSince.IsZero(), StationImageVision: w.policy.StationImageVision, StationVideoVision: w.policy.StationVideoVision, StationLocalAudio: w.policy.StationLocalAudio, AutoObserveVideos: !w.policy.AutoVideosSince.IsZero(), MediaReplyThreads: mediaReplies, HistoryThreads: history, ContactRead: contactRead, GroupMuteThreads: groupMute, GroupRules: groupRules}
}

func (s *stationWechat) Inspect(ctx context.Context, id string) (apphost.ConnectionStatus, error) {
	view, err := s.stationWechatNative.Inspect(ctx, id)
	if err != nil {
		return view, err
	}
	w, err := s.worker(id, false)
	if err != nil {
		return view, err
	}
	view.LegacyImport = s.legacyImportState(view.AccountID, w)
	if view.LegacyImport == "applied" {
		if state, _ := s.nativeImportReceipt(id, view.AccountID); state == nativeImportPathChanged {
			view.WorkerBlocker = state
		}
	}
	if w == nil {
		view.Worker = "disabled"
		view.Policy = (&stationWechatWorker{}).policyView()
		return view, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	view.Configuration, view.Worker = "worker-configured", w.state()
	if w.runtimeDir != "" && !w.policy.RollbackFrozenAt.IsZero() && w.policy.RelocationActivation == nil {
		view.WorkerBlocker = "relocation_confirmation_required"
	}
	view.SubmissionsPaused, view.Policy = w.policy.Paused, w.policyView()
	return view, nil
}

func (s *stationWechat) SetConnectionPolicy(ctx context.Context, id string, policy apphost.ConnectionPolicyInput) (apphost.ConnectionStatus, error) {
	threads := policy.ReplyThreads
	if len(threads) > 100 {
		return apphost.ConnectionStatus{}, apphost.ErrInvalidConnectionPolicy
	}
	grants := make(map[string]time.Time, len(threads))
	for _, thread := range threads {
		if _, duplicate := grants[thread]; !identity.ValidPathID(thread) || duplicate {
			return apphost.ConnectionStatus{}, apphost.ErrInvalidConnectionPolicy
		}
		grants[thread] = time.Time{}
	}
	view, err := s.stationWechatNative.Inspect(ctx, id)
	if err != nil {
		return view, err
	}
	w, err := s.worker(id, true)
	if err != nil {
		return view, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if s.ctx.Err() != nil || ctx.Err() != nil {
		return view, apphost.ErrLifecycleConflict
	}
	now := time.Now().UTC()
	for thread := range grants {
		if previous, ok := w.policy.Grants[thread]; ok {
			grants[thread] = previous
		} else {
			grants[thread] = now
		}
	}
	attachments, err := stationScopedGrants(grants, w.policy.AttachmentGrants, policy.AttachmentThreads, now)
	if err != nil {
		return view, err
	}
	mediaReplies, err := stationScopedGrants(attachments, w.policy.MediaReplyGrants, policy.MediaReplyThreads, now)
	if err != nil {
		return view, err
	}
	history, err := stationScopedGrants(grants, w.policy.HistoryGrants, policy.HistoryThreads, now)
	if err != nil {
		return view, err
	}
	groupRules, err := wechatapp.UpdateGroupRules(w.policy.GroupRules, policy.GroupRules, grants, attachments, now)
	if err != nil {
		return view, err
	}
	next := w.policy
	next.GroupRules = groupRules
	next.StationImageVision = stationScopedOption(len(attachments) > 0, policy.StationImageVision, next.StationImageVision)
	next.StationVideoVision = stationScopedOption(len(attachments) > 0, policy.StationVideoVision, next.StationVideoVision)
	next.StationLocalAudio = stationScopedOption(len(attachments) > 0, policy.StationLocalAudio, next.StationLocalAudio)
	next.AutoImagesSince = stationAutoMediaSince(len(attachments) > 0, policy.AutoObserveImages, next.AutoImagesSince, now)
	next.AutoVideosSince = stationAutoMediaSince(len(attachments) > 0, policy.AutoObserveVideos, next.AutoVideosSince, now)
	if policy.GroupMuteThreads != nil {
		requested := *policy.GroupMuteThreads
		if len(requested) > 100 {
			return view, apphost.ErrInvalidConnectionPolicy
		}
		next.GroupMuteGrants = make(map[string]time.Time, len(requested))
		for _, group := range requested {
			if !wechatapp.ValidGroup(group) || !next.GroupMuteGrants[group].IsZero() {
				return view, apphost.ErrInvalidConnectionPolicy
			}
			grant := w.policy.GroupMuteGrants[group]
			if grant.IsZero() {
				grant = now
			}
			next.GroupMuteGrants[group] = grant
		}
	}
	if policy.ContactRead != nil {
		if !*policy.ContactRead {
			next.ContactGrant = time.Time{}
		} else if next.ContactGrant.IsZero() {
			next.ContactGrant = now
		}
	}
	workerScopeChanged := !maps.Equal(grants, w.policy.Grants) || !maps.Equal(attachments, w.policy.AttachmentGrants) || !maps.Equal(mediaReplies, w.policy.MediaReplyGrants) || !maps.Equal(history, w.policy.HistoryGrants) || !maps.Equal(groupRules, w.policy.GroupRules) || next.StationLocalAudio != w.policy.StationLocalAudio || next.StationVideoVision != w.policy.StationVideoVision || next.StationImageVision != w.policy.StationImageVision || next.AutoImagesSince != w.policy.AutoImagesSince || next.AutoVideosSince != w.policy.AutoVideosSince
	if workerScopeChanged {
		next.Grants, next.AttachmentGrants, next.MediaReplyGrants, next.HistoryGrants = grants, attachments, mediaReplies, history
		// Require explicit resume after a scope change on an enabled worker.
		next.Paused = next.Enabled
		if err := w.changePolicy(next); err != nil {
			return view, err
		}
		if err := w.stop(ctx); err != nil {
			return view, err
		}
	} else if next.ContactGrant != w.policy.ContactGrant || !maps.Equal(next.GroupMuteGrants, w.policy.GroupMuteGrants) {
		if err := w.save(next); err != nil {
			return view, err
		}
	}
	view.Configuration, view.Worker = "worker-configured", w.state()
	view.SubmissionsPaused, view.Policy = w.policy.Paused, w.policyView()
	return view, nil
}

func stationScopedOption(hasAttachments bool, requested *bool, previous bool) bool {
	if requested != nil {
		previous = *requested
	}
	return hasAttachments && previous
}

func stationAutoMediaSince(hasAttachments bool, requested *bool, previous, now time.Time) time.Time {
	if !hasAttachments || requested != nil && !*requested {
		return time.Time{}
	}
	if requested != nil && previous.IsZero() {
		return now
	}
	return previous
}

func stationScopedGrants(parent, previous map[string]time.Time, requested *[]string, now time.Time) (map[string]time.Time, error) {
	result := map[string]time.Time{}
	if requested == nil {
		for thread, grant := range previous {
			if !parent[thread].IsZero() {
				result[thread] = grant
			}
		}
		return result, nil
	}
	for _, thread := range *requested {
		if parent[thread].IsZero() || !result[thread].IsZero() {
			return nil, apphost.ErrInvalidConnectionPolicy
		}
		grant := previous[thread]
		if grant.IsZero() {
			grant = now
		}
		result[thread] = grant
	}
	return result, nil
}

func (w *stationWechatWorker) changePolicy(next stationWechatPolicy) error {
	var wasBlocked bool
	if w.run != nil {
		wasBlocked = w.run.SetBlocked(true)
	}
	if err := w.save(next); err != nil {
		if w.run != nil {
			w.run.SetBlocked(wasBlocked)
		}
		return err
	}
	return nil
}

func (s *stationWechat) SetConnectionRunning(ctx context.Context, id string, running bool) (apphost.ConnectionLifecycle, error) {
	native, err := s.WorkerSender(ctx, id)
	if err != nil {
		return apphost.ConnectionLifecycle{}, err
	}
	w, err := s.worker(id, false)
	if err != nil {
		return apphost.ConnectionLifecycle{}, err
	}
	if w == nil {
		if running {
			return apphost.ConnectionLifecycle{}, apphost.ErrLifecycleNotReady
		}
		return apphost.ConnectionLifecycle{ConnectionRef: s.connectionRef(id, native.Account()), Worker: "disabled"}, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if s.ctx.Err() != nil || ctx.Err() != nil {
		return apphost.ConnectionLifecycle{}, apphost.ErrLifecycleConflict
	}
	if running {
		if !w.policy.RollbackFrozenAt.IsZero() {
			return apphost.ConnectionLifecycle{}, apphost.ErrLifecycleCutover
		}
		_, modelErr := s.modelPersona()
		if modelErr != nil || len(w.policy.Grants) == 0 {
			return apphost.ConnectionLifecycle{}, apphost.ErrLifecycleNotReady
		}
		// Check evidence before unpausing, and also for an idempotent start
		// against a worker whose old host might have restarted meanwhile.
		guard, guardErr := s.legacyActivationGuard(ctx, id, native, w)
		if guardErr != nil {
			w.cutoverBlocked.Store(true)
			if w.run != nil {
				w.run.BlockCutover()
			}
			return apphost.ConnectionLifecycle{}, guardErr
		}
		w.cutoverBlocked.Store(false)
		if state := w.state(); state != "running" && state != "waiting_login" {
			if err := w.stop(ctx); err != nil {
				return apphost.ConnectionLifecycle{}, err
			}
			next := w.policy
			next.Enabled, next.Paused = true, false
			if err := w.save(next); err != nil {
				return apphost.ConnectionLifecycle{}, err
			}
			if err := s.launch(w, native, guard); err != nil {
				return apphost.ConnectionLifecycle{}, err
			}
		}
	} else {
		next := w.policy
		next.Paused = true
		if err := w.changePolicy(next); err != nil {
			return apphost.ConnectionLifecycle{}, err
		}
		if err := w.stop(ctx); err != nil {
			return apphost.ConnectionLifecycle{}, err
		}
	}
	return apphost.ConnectionLifecycle{
		ConnectionRef: s.connectionRef(id, native.Account()),
		Worker:        w.state(), WorkerEnabled: w.policy.Enabled, SubmissionsPaused: w.policy.Paused,
	}, nil
}

func (s *stationWechat) launch(w *stationWechatWorker, native *linuxnative.Sender, activationGuard func() bool) error {
	persona, err := s.modelPersona()
	if err != nil || len(w.policy.Grants) == 0 {
		return apphost.ErrLifecycleNotReady
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	policy := w.policy
	run, err := s.lifecycle.Start(func(ctx context.Context, run *wechatapp.WorkerRun) error {
		return s.runWorker(ctx, run, w, native, persona, policy, activationGuard)
	})
	if err != nil {
		return err
	}
	w.run = run
	return nil
}

func (s *stationWechat) runWorker(ctx context.Context, run *wechatapp.WorkerRun, station *stationWechatWorker, native *linuxnative.Sender, persona string, policy stationWechatPolicy, activationGuard func() bool) error {
	activationAllowed := func() error {
		if activationGuard == nil || activationGuard() {
			return nil
		}
		// A local marker/profile can become invalid after an explicit start.
		// Stop only future automated work; accepted receipts stay untouched.
		station.cutoverBlocked.Store(true)
		run.BlockCutover()
		return apphost.ErrLifecycleCutover
	}
	if err := activationAllowed(); err != nil {
		return err
	}
	station.account = native.Account()
	// The bound owner private chat is an implicit reply scope. It is not a
	// group whitelist entry, so add it to this run's immutable policy snapshot
	// after rechecking the persisted owner binding. Other private contacts still
	// require an explicit reply grant.
	policy = s.addBoundOwnerReplyGrant(station.connectionID, native.Account(), policy)
	dir := station.dir
	if err := wechatapp.WaitWorkerLogin(ctx, native); err != nil {
		return err
	}
	if err := activationAllowed(); err != nil {
		return err
	}
	memory, err := companion.NewStore(filepath.Join(dir, "memory"))
	if err != nil {
		return err
	}
	defer memory.Close()
	attachment := &stationAttachmentTools{store: memory, native: native, dir: dir, grants: policy.AttachmentGrants, replies: policy.MediaReplyGrants, reads: policy.HistoryGrants, blocked: run.Blocked, documents: s.documents}
	attachment.crossHost, attachment.crossConnection = s, station.connectionID
	attachment.stationImageVision, attachment.stationVideoVision, attachment.localVision = policy.StationImageVision, policy.StationVideoVision, s.localVision
	attachment.stationLocalAudio, attachment.localASR = policy.StationLocalAudio, s.localASR
	attachment.shareHost = func() (*stationShareHost, error) { return s.shareHost(station, native.Account()) }
	if s.localChat != nil {
		attachment.speech = s.localChat.projectTools
		attachment.speechOwnerAllowed = func(in companion.Input) bool { return s.ownerBoundToWechat(station.connectionID, native.Account(), in) }
		attachment.speechMu = &station.speechMu
	}
	if s.artifacts != nil {
		attachment.work = &stationWorkAccess{artifacts: s.artifacts, dir: dir, grants: policy.MediaReplyGrants}
	}
	// Public workstation tools use the same registered tool catalog as local
	// chat, but they are constructed in the local owner's scope only after the
	// WeChat turn has supplied a verified sender. This keeps project files,
	// generators and local memory out of ordinary group-member turns and avoids
	// writing local checkpoints into the WeChat memory database.
	publicTools := newPublicToolFactoryAccess(func(in companion.Input) companion.ToolAccess {
		if s.localChat == nil || s.ownerReader == nil {
			return nil
		}
		if !s.ownerBoundToWechat(station.connectionID, in.Account, in) {
			return nil
		}
		store, localInput, err := s.localChat.conversation()
		if err != nil {
			return nil
		}
		localInput.TraceID = in.TraceID
		localInput.Text = in.Text
		localInput.OriginalText = in.OriginalText
		localInput.MessageTime = in.MessageTime
		tools := s.localChat.tools(store, localInput)
		tools.projectTools = s.localChat.projectTools
		tools.publicUseOnly = true
		tools.run = companion.NewGuardedToolRun(func(ctx context.Context) error {
			if run.Blocked() || ctx.Err() != nil || policy.Grants[in.Thread].IsZero() || !s.ownerBoundToWechat(station.connectionID, in.Account, in) {
				return apphost.ErrObservationForbidden
			}
			return nil
		})
		return tools
	}, func(in companion.Input) bool {
		if run.Blocked() || in.Channel != "wechat" || !in.Directed || in.Preview || in.Surf || in.ReportOnly {
			return false
		}
		if policy.Grants[in.Thread].IsZero() || s.ownerReader == nil {
			return false
		}
		return s.ownerBoundToWechat(station.connectionID, in.Account, in)
	})
	tools := mergeStationToolAccess(attachment, publicTools, nil)
	var roundErrorLogged atomic.Bool
	recordEvent := func(event string) {
		if event == "" || station.cutoverBlocked.Load() {
			return
		}
		// Keep the old bot's lifecycle events in the same durable execution
		// journal as message/tool records.  The event intentionally carries no
		// hidden model output or credentials; the detailed tool result remains in
		// the existing round/task stores.
		_ = station.recordRound(station.connectionID, native.Account(), time.Now(), bot.Record{Event: event, Category: "decision", Origin: "station"})
	}
	attachment.shareProgress = func(event, status, reason string) {
		if event == "" || station.cutoverBlocked.Load() || policy.Grants[attachment.trace.Thread].IsZero() {
			return
		}
		_ = station.recordRound(station.connectionID, native.Account(), time.Now(), bot.Record{
			Event: event, Category: "tool", ThreadID: attachment.trace.Thread, MessageID: attachment.trace.TraceID,
			Tool: "share-reader", ToolStage: "public-media", Status: status, Reason: reason, Origin: "wechat",
		})
	}
	return run.RunQueue(ctx, native, wechatapp.WorkerQueue{
		ProtectQuote: attachment.protectCrossReply,
		SurfTask: func(now time.Time) (*bot.SurfTask, error) {
			return station.claimSurf(ctx, native.Account(), now, activationAllowed,
				func(ctx context.Context, state stationSurfSchedule) (func(context.Context, string) (companion.ToolAccess, error), error) {
					return s.prepareSurf(ctx, station, state, activationAllowed)
				})
		},
		Record: func(entry bot.Record) {
			if entry.ThreadID != "" && policy.Grants[entry.ThreadID].IsZero() {
				return
			}
			if err := station.recordRound(station.connectionID, native.Account(), time.Now(), entry); err != nil && roundErrorLogged.CompareAndSwap(false, true) {
				log.Print("WeChat execution journal is unavailable")
			}
		},
		Log: recordEvent,
		ObservationFailed: func(in companion.Input, reason string) error {
			if policy.Grants[in.Thread].IsZero() {
				return nil
			}
			return station.recordRound(station.connectionID, native.Account(), time.Now(), bot.Record{Event: "media_observation_failed", Category: "error", ThreadID: in.Thread, MessageID: in.TraceID, Reason: reason, Status: "failed"})
		},
		MediaRepairRequired: func(in companion.Input, jobID, reason string) error {
			if policy.Grants[in.Thread].IsZero() {
				return nil
			}
			return station.recordRound(station.connectionID, native.Account(), time.Now(), bot.Record{Event: "media_repair_required", Category: "error", ThreadID: in.Thread, MessageID: in.TraceID, Reason: reason, Status: "needs_review", SendID: jobID})
		},
		BacklogSettled: func(account, thread string, ids []string, action string) {
			if account != native.Account() || policy.Grants[thread].IsZero() {
				return
			}
			_ = station.recordRound(station.connectionID, native.Account(), time.Now(), bot.Record{Event: "backlog_settled", Category: "decision", ThreadID: thread, Content: action, Reason: strings.Join(ids, ","), Status: "completed"})
		},
		SurfAllowed: func(thread string) bool {
			if run.Blocked() || ctx.Err() != nil {
				return false
			}
			return !policy.Grants[thread].IsZero()
		},
		Draining: func() bool { return run.Blocked() || ctx.Err() != nil },
		AgentTask: func(now time.Time, busy func(string) bool) (*companion.Input, error) {
			return station.claimAgentTask(now, native.Account(), busy)
		},
		AgentTaskActive: station.agentTaskActive,
		AgentTaskDone: func(in companion.Input, state string) {
			station.updateAgentTask(in, state, nil, 0, "")
		},
		AgentTaskRetry: func(in companion.Input, delay time.Duration, reason string) {
			station.updateAgentTask(in, "retrying_model", nil, delay, reason)
		},
		AgentTaskDelivery: func(in companion.Input, delivery bot.TaskDelivery) error {
			station.updateAgentTask(in, "", &delivery, 0, "")
			return nil
		},
		MediaSeeds:       station.mediaSeeds,
		MediaSeedKnown:   station.mediaSeedKnown,
		MediaSeedAllowed: station.mediaSeedAllowed,
		MediaSeedReceipt: station.mediaSeedReceipt,
		MediaSeedSettled: station.settleMediaSeed,
		StateDir:         station.queueDir(), Groups: policy.groupPolicy(), AllowMediaReplies: len(policy.MediaReplyGrants) != 0,
		Behavior: policy.Behavior,
		Memory:   memory, Model: s.model, Persona: persona, Tools: tools,
		MediaObserver:     stationMediaObserver{tools: attachment, imageSince: policy.AutoImagesSince, videoSince: policy.AutoVideosSince},
		ReadGroupShare:    attachment.readGroupShareEvidence,
		DeliveryCancelled: station.deliveryCancelled, WithDeliverySubmit: station.withDeliverySubmit,
		NextMediaResume: station.nextMediaResume, MediaResumeResult: station.mediaResumeResult,
		AuthorizeMedia: attachment.replyAllowed, PrepareMediaSource: attachment.stageReply,
		AuthorizeMediaAsset: attachment.workAllowed, AuthorizeMediaSubmit: attachment.canSubmitMedia,
	}, activationAllowed)
}
