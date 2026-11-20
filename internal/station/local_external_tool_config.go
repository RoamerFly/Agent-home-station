package station

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/workspacefs"
	catalog "wechat-bot/plugins"
)

const externalToolConfigFile = "config.json"
const externalToolConfigLimit = 128 << 10

type externalToolConfig struct {
	Values  map[string]json.RawMessage `json:"values"`
	Secrets map[string]string          `json:"secrets,omitempty"`
}

func externalToolDataID(id string) bool {
	if !discoveredToolID.MatchString(id) {
		return false
	}
	switch id {
	case "catalog", "registrations", "validation", "mobile-control":
		return false
	}
	_, err := catalog.ContractFor(id)
	return err != nil
}

// Called under s.update before enable/start. The directory is owned by the
// workbench. The runtime still receives a versioned copy through its host.
func (s *localProjectTools) loadExternalToolConfig(ctx context.Context, host *localProjectToolHost, tool pluginhost.InstalledPackage) error {
	id := tool.Package.ID
	schema, err := host.store.ConfigSchema(ctx, id)
	if err != nil {
		return err
	}
	current, err := host.store.Configuration(ctx, id)
	if err != nil {
		return err
	}
	root, err := workspacefs.EnsurePath(filepath.Join(stationToolDataDir(s.home.dataDir), id))
	if err != nil {
		return err
	}
	defer root.Close()
	config, err := readExternalToolConfig(root)
	if errors.Is(err, os.ErrNotExist) {
		// Seed existing non-secret settings on upgrades from the catalog-only layout.
		// Existing credentials stay in the host's credential storage.
		initial := externalToolConfig{Values: current.Values}
		for name, field := range schema.Properties {
			if _, present := initial.Values[name]; !present && !field.Secret && len(field.Default) > 0 {
				initial.Values[name] = field.Default
			}
		}
		if err = createExternalToolConfig(root, initial); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		config, err = readExternalToolConfig(root)
	}
	if err != nil {
		return err
	}
	for name := range config.Values {
		if field, ok := schema.Properties[name]; !ok || field.Secret {
			return errors.New("invalid public tool configuration field")
		}
	}
	for name, value := range config.Secrets {
		if !schema.Properties[name].Secret || value == "" {
			return errors.New("invalid private tool configuration field")
		}
	}
	for name, field := range schema.Properties {
		if _, present := config.Values[name]; !present && !field.Secret && len(field.Default) > 0 {
			config.Values[name] = field.Default
		}
	}
	if tool.Enabled {
		active, activeErr := host.store.ConfigurationForExecution(ctx, id)
		if activeErr == nil && externalConfigMatches(schema, config, active.Values) {
			return nil
		}
	} else if len(config.Secrets) == 0 && sameExternalValues(config.Values, current.Values) && host.store.CheckConfiguration(ctx, id) == nil {
		return nil
	}
	update := pluginhost.ConfigUpdate{Revision: current.Revision, Values: config.Values, Secrets: config.Secrets}
	if tool.Enabled {
		_, err = host.controller.ConfigureIdle(ctx, id, update)
	} else {
		_, err = host.store.SaveConfiguration(ctx, id, update)
	}
	return err
}

func externalConfigMatches(schema pluginhost.ConfigSchema, config externalToolConfig, active map[string]json.RawMessage) bool {
	values := map[string]json.RawMessage{}
	for name, raw := range active {
		if !schema.Properties[name].Secret {
			values[name] = raw
		}
	}
	if !sameExternalValues(values, config.Values) {
		return false
	}
	for name, value := range config.Secrets {
		var previous string
		if json.Unmarshal(active[name], &previous) != nil || previous != value {
			return false
		}
	}
	return true
}

func sameExternalValues(a, b map[string]json.RawMessage) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

func readExternalToolConfig(root *os.Root) (externalToolConfig, error) {
	var config externalToolConfig
	info, err := root.Lstat(externalToolConfigFile)
	if err != nil {
		return config, err
	}
	if !info.Mode().IsRegular() {
		return config, errors.New("tool configuration must be a regular file")
	}
	file, err := workspacefs.OpenRegular(root, externalToolConfigFile)
	if err != nil {
		return config, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, externalToolConfigLimit+1))
	if err != nil {
		return config, err
	}
	if len(raw) > externalToolConfigLimit {
		return config, errors.New("tool configuration exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&config); err != nil {
		return config, err
	}
	if decoder.Decode(new(any)) != io.EOF || config.Values == nil {
		return config, errors.New("tool configuration must contain a values object")
	}
	return config, nil
}

func createExternalToolConfig(root *os.Root, config externalToolConfig) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	file, err := root.OpenFile(externalToolConfigFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}
