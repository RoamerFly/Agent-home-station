package admin

// External connection declarations and credentials belong to the account's
// private host state. They are never plugin configuration or runtime arguments.
import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/plugins"
)

const externalConfigurationNamespace = "configuration-v1"
const externalConfigurationChunkBytes = 512 << 10

var errExternalConfigurationChanged = errors.New("external_configuration_source_changed")
var errExternalConfigurationCorrupt = errors.New("external_configuration_storage_invalid")

type externalConfigurationDocument struct {
	Version     int                    `json:"version"`
	Settings    mcpSettings            `json:"settings"`
	Connections map[string]pluginEntry `json:"connections"`
}

type externalConfigurationManifest struct {
	Version    int    `json:"version"`
	Chunks     int    `json:"chunks"`
	Bytes      int    `json:"bytes"`
	SHA256     string `json:"sha256"`
	SourceHash string `json:"source_hash"`
}

type externalConfigurationRepository struct {
	mu        sync.Mutex
	dir       string
	account   string
	current   pluginhost.HostState
	document  externalConfigurationDocument
	source    string
	openStore func() (*pluginhost.Store, error)
	owned     bool
}

func newExternalConfigurationRepository(dir, account string) (*externalConfigurationRepository, error) {
	r := &externalConfigurationRepository{dir: dir, account: account, owned: true}
	state, owner, err := readExistingExternalConfiguration(context.Background(), dir, account)
	if err != nil {
		return nil, err
	}
	if r.account == "" {
		r.account = owner
	}
	if state.Revision > 0 {
		r.document, err = decodeExternalConfiguration(state)
		r.current, r.source = state, state.SourceHash
	} else {
		r.document, r.source, err = readLegacyExternalConfiguration(dir)
		r.current = pluginhost.HostState{Records: map[string]json.RawMessage{}}
	}
	if err != nil {
		return nil, err
	}
	r.openStore = func() (*pluginhost.Store, error) {
		return pluginhost.OpenStore(filepath.Join(dir, "plugin-host"), r.account)
	}
	return r, nil
}

// A read-only probe precedes legacy parsing. A fresh chat-only installation
// neither creates a Store nor touches an optional database. Active data wins
// even if the retained legacy files later become corrupt or disappear.
func readExistingExternalConfiguration(ctx context.Context, dir, account string) (pluginhost.HostState, string, error) {
	empty := pluginhost.HostState{Records: map[string]json.RawMessage{}}
	path := filepath.Join(dir, "plugin-host", "plugins.sqlite")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, "", nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return empty, "", errExternalConfigurationCorrupt
	}
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro"}
	if !strings.HasPrefix(uri.Path, "/") {
		uri.Path = "/" + uri.Path
	}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return empty, "", errExternalConfigurationCorrupt
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, "", errExternalConfigurationCorrupt
	}
	defer tx.Rollback()
	var owner string
	if err = tx.QueryRowContext(ctx, "SELECT account FROM owner WHERE singleton=1").Scan(&owner); err != nil || owner == "" || account != "" && account != owner {
		return empty, "", errExternalConfigurationCorrupt
	}
	var tables int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='host_states'").Scan(&tables); err != nil {
		return empty, "", errExternalConfigurationCorrupt
	}
	if tables == 0 {
		return empty, owner, nil
	}
	state := empty
	err = tx.QueryRowContext(ctx, "SELECT revision,source_hash FROM host_states WHERE plugin=? AND namespace=?", externalHTTPPluginID, externalConfigurationNamespace).Scan(&state.Revision, &state.SourceHash)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, owner, nil
	}
	if err != nil || state.Revision < 1 || len(state.SourceHash) != 64 {
		return empty, "", errExternalConfigurationCorrupt
	}
	if _, err = hex.DecodeString(state.SourceHash); err != nil {
		return empty, "", errExternalConfigurationCorrupt
	}
	var count, totalBytes, largest, smallest int
	if err = tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(SUM(length(payload)),0),COALESCE(MAX(length(payload)),0),COALESCE(MIN(length(payload)),0) FROM host_state_records WHERE plugin=? AND namespace=?", externalHTTPPluginID, externalConfigurationNamespace).Scan(&count, &totalBytes, &largest, &smallest); err != nil || count < 1 || count > 17 || totalBytes > 8<<20 || largest > 1<<20 || smallest < 1 {
		return empty, "", errExternalConfigurationCorrupt
	}
	rows, err := tx.QueryContext(ctx, "SELECT key,payload FROM host_state_records WHERE plugin=? AND namespace=?", externalHTTPPluginID, externalConfigurationNamespace)
	if err != nil {
		return empty, "", errExternalConfigurationCorrupt
	}
	total := 0
	for rows.Next() {
		var key string
		var raw []byte
		if rows.Scan(&key, &raw) != nil || len(raw) > 1<<20 || len(raw) == 0 {
			rows.Close()
			return empty, "", errExternalConfigurationCorrupt
		}
		total += len(raw)
		if total > 8<<20 || len(state.Records) > 32 {
			rows.Close()
			return empty, "", errExternalConfigurationCorrupt
		}
		state.Records[key] = append(json.RawMessage(nil), raw...)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return empty, "", errExternalConfigurationCorrupt
	}
	if err = tx.Commit(); err != nil {
		return empty, "", errExternalConfigurationCorrupt
	}
	return state, owner, nil
}

func readLegacyExternalConfiguration(dir string) (externalConfigurationDocument, string, error) {
	doc := externalConfigurationDocument{Version: 1, Settings: mcpDefaults(), Connections: map[string]pluginEntry{}}
	hashes := map[string]string{}
	for _, name := range []string{"mcp-config.json", "plugins.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			hashes[name] = "absent"
			continue
		}
		if err != nil {
			return doc, "", errExternalConfigurationCorrupt
		}
		hashes[name] = namedSpeechSHA(raw)
		if name == "mcp-config.json" {
			if len(raw) > 64<<10 || json.Unmarshal(raw, &doc.Settings) != nil {
				return doc, "", errors.New("MCP 配置不可读，未自动覆盖")
			}
		} else if len(raw) > 2<<20 || json.Unmarshal(raw, &doc.Connections) != nil || doc.Connections == nil {
			return doc, "", errors.New("工具配置不可读，未自动覆盖")
		}
	}
	if err := validateExternalConfiguration(doc); err != nil {
		return doc, "", err
	}
	raw, _ := json.Marshal(hashes)
	return doc, namedSpeechSHA(raw), nil
}

func validateExternalConfiguration(doc externalConfigurationDocument) error {
	if doc.Version != 1 || doc.Connections == nil || len(doc.Connections) > 30 {
		return errExternalConfigurationCorrupt
	}
	if err := validateMCP(doc.Settings); err != nil {
		return err
	}
	items := []plugins.Installed{}
	for _, item := range doc.Connections {
		if !item.Removed {
			items = append(items, item.Installed)
		}
	}
	if _, err := plugins.New(items); err != nil {
		return errors.New("工具声明无效")
	}
	settings, _ := json.Marshal(doc.Settings)
	connections, _ := json.Marshal(doc.Connections)
	if len(settings) > 64<<10 || len(connections) > 2<<20 {
		return errors.New("外部连接配置过大，未保存")
	}
	return nil
}

func externalConfigurationRecords(doc externalConfigurationDocument, source string) (map[string]json.RawMessage, error) {
	if err := validateExternalConfiguration(doc); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	count := (len(raw) + externalConfigurationChunkBytes - 1) / externalConfigurationChunkBytes
	records := map[string]json.RawMessage{}
	for index := 0; index < count; index++ {
		end := (index + 1) * externalConfigurationChunkBytes
		if end > len(raw) {
			end = len(raw)
		}
		records[fmt.Sprintf("document:%06d", index)], _ = json.Marshal(base64.StdEncoding.EncodeToString(raw[index*externalConfigurationChunkBytes : end]))
	}
	records["manifest"], _ = json.Marshal(externalConfigurationManifest{Version: 1, Chunks: count, Bytes: len(raw), SHA256: namedSpeechSHA(raw), SourceHash: source})
	return records, nil
}

func decodeExternalConfiguration(state pluginhost.HostState) (externalConfigurationDocument, error) {
	var manifest externalConfigurationManifest
	var doc externalConfigurationDocument
	if musicStrictJSON(state.Records["manifest"], &manifest) != nil || manifest.Version != 1 || manifest.SourceHash != state.SourceHash || manifest.Chunks < 1 || manifest.Chunks > 16 || len(state.Records) != manifest.Chunks+1 || manifest.Bytes < 1 || manifest.Bytes > 3<<20 {
		return doc, errExternalConfigurationCorrupt
	}
	raw := []byte{}
	for index := 0; index < manifest.Chunks; index++ {
		var chunk string
		if json.Unmarshal(state.Records[fmt.Sprintf("document:%06d", index)], &chunk) != nil {
			return doc, errExternalConfigurationCorrupt
		}
		data, err := base64.StdEncoding.DecodeString(chunk)
		if err != nil || len(data) > externalConfigurationChunkBytes {
			return doc, errExternalConfigurationCorrupt
		}
		raw = append(raw, data...)
	}
	if len(raw) != manifest.Bytes || namedSpeechSHA(raw) != manifest.SHA256 || musicStrictJSON(raw, &doc) != nil {
		return doc, errExternalConfigurationCorrupt
	}
	return doc, validateExternalConfiguration(doc)
}

func cloneExternalConfiguration(doc externalConfigurationDocument) externalConfigurationDocument {
	raw, _ := json.Marshal(doc)
	var out externalConfigurationDocument
	_ = json.Unmarshal(raw, &out)
	return out
}

func (r *externalConfigurationRepository) snapshot() externalConfigurationDocument {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneExternalConfiguration(r.document)
}

// Refresh is a read operation, not activation. It lets an administrator reload
// after a CAS conflict without restarting the account or reimporting JSON.
func (r *externalConfigurationRepository) Refresh(ctx context.Context) (externalConfigurationDocument, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, owner, err := readExistingExternalConfiguration(ctx, r.dir, r.account)
	if err != nil {
		return externalConfigurationDocument{}, err
	}
	var doc externalConfigurationDocument
	source := state.SourceHash
	if state.Revision > 0 {
		doc, err = decodeExternalConfiguration(state)
	} else if r.current.Revision > 0 {
		return externalConfigurationDocument{}, errExternalConfigurationCorrupt
	} else {
		doc, source, err = readLegacyExternalConfiguration(r.dir)
	}
	if err != nil {
		return externalConfigurationDocument{}, err
	}
	if r.account == "" && owner != "" {
		r.account = owner
	}
	r.current, r.document, r.source = state, doc, source
	return cloneExternalConfiguration(doc), nil
}

func (r *externalConfigurationRepository) commitLocked(ctx context.Context, doc externalConfigurationDocument) error {
	records, err := externalConfigurationRecords(doc, r.source)
	if err != nil {
		return err
	}
	if r.current.Revision == 0 {
		_, source, readErr := readLegacyExternalConfiguration(r.dir)
		if readErr != nil {
			return readErr
		}
		if source != r.source {
			return errExternalConfigurationChanged
		}
	}
	store, err := r.openStore()
	if err != nil {
		return err
	}
	if r.owned {
		defer store.Close()
	}
	state := r.current
	if state.Revision == 0 {
		state, err = store.InitializeHostState(ctx, externalHTTPPluginID, externalConfigurationNamespace, r.source, records)
	} else {
		deletes := []string{}
		for key := range state.Records {
			if _, ok := records[key]; !ok {
				deletes = append(deletes, key)
			}
		}
		sort.Strings(deletes)
		state.Revision, err = store.UpdateHostState(ctx, externalHTTPPluginID, externalConfigurationNamespace, state.Revision, records, deletes)
		state.Records = records
	}
	if err != nil {
		return err
	}
	r.current, r.document = state, cloneExternalConfiguration(doc)
	return nil
}

func (r *externalConfigurationRepository) Activate(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current.Revision > 0 {
		return nil
	}
	return r.commitLocked(ctx, r.document)
}

func (r *externalConfigurationRepository) SaveSettings(ctx context.Context, settings mcpSettings) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	doc := cloneExternalConfiguration(r.document)
	doc.Settings = cloneMCP(settings)
	return r.commitLocked(ctx, doc)
}

func (r *externalConfigurationRepository) SaveConnections(ctx context.Context, items map[string]pluginEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	doc := cloneExternalConfiguration(r.document)
	doc.Connections = items
	return r.commitLocked(ctx, doc)
}

func (m *Manager) migrateExternalConfiguration(ctx context.Context) error {
	return m.plugins.repository.Activate(ctx)
}

// Callers serialize this explicit configuration reload with other admin saves.
func (m *Manager) refreshExternalConfiguration(ctx context.Context) error {
	doc, err := m.plugins.repository.Refresh(ctx)
	if err != nil {
		return err
	}
	m.plugins.mu.Lock()
	m.plugins.items = doc.Connections
	m.plugins.mu.Unlock()
	m.mu.Lock()
	m.mcp = doc.Settings
	m.mu.Unlock()
	return nil
}

// Migration preview may substitute these host-only bytes for retained legacy
// paths. Callers must never put them in a plugin configuration or API response.
func (m *Manager) externalConfigurationSources() (map[string][]byte, bool) {
	r := m.plugins.repository
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current.Revision == 0 {
		return nil, false
	}
	settings, _ := json.Marshal(r.document.Settings)
	connections, _ := json.Marshal(r.document.Connections)
	return map[string][]byte{"mcp-config.json": settings, "plugins.json": connections}, true
}
