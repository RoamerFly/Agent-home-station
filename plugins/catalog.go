// Package catalog embeds official package declarations, never plugin runtime
// code. The deployment-selected catalog directory remains the only source used
// to prepare an installable package snapshot.
package catalog

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"

	"wechat-bot/internal/pluginhost"
)

// Contract is the declarative, reviewable part of an official plugin. It is
// deliberately limited to the manifest and the three schemas the host already
// exposes after installation. Runtime code, test fixtures, documentation and
// model assets are not embedded in the light core.
//
// ManifestSHA256 is the SHA-256 of the exact embedded plugin.json bytes. It
// is therefore the same value Store.Prepare returns for an unchanged official
// catalog directory, rather than a hash of a re-serialized Go struct.
type Contract struct {
	Package        pluginhost.Package
	ManifestSHA256 string
	Configuration  pluginhost.ConfigSchema
	Capabilities   []pluginhost.Capability
	UI             json.RawMessage
}

//go:embed */plugin.json */config.schema.json */capabilities.json */ui.schema.json
var declarations embed.FS

// ContractFor reads and parses one official declarative contract. The caller
// receives no executable plugin files through this package.
func ContractFor(id string) (Contract, error) {
	if id == "" || path.Base(id) != id {
		return Contract{}, errors.New("invalid catalog package id")
	}
	root := id + "/"
	manifest, err := declarations.ReadFile(root + "plugin.json")
	if err != nil {
		return Contract{}, fmt.Errorf("catalog package unavailable: %w", err)
	}
	var out Contract
	if err := json.Unmarshal(manifest, &out.Package); err != nil {
		return Contract{}, errors.New("invalid catalog manifest")
	}
	sum := sha256.Sum256(manifest)
	out.ManifestSHA256 = hex.EncodeToString(sum[:])

	configuration, err := declarations.ReadFile(root + "config.schema.json")
	if err != nil {
		return Contract{}, fmt.Errorf("catalog configuration unavailable: %w", err)
	}
	if err := decodeStrict(configuration, &out.Configuration); err != nil {
		return Contract{}, errors.New("invalid catalog configuration schema")
	}
	if out.Configuration.Type != "object" || out.Configuration.AdditionalProperties {
		return Contract{}, errors.New("catalog configuration schema is not closed")
	}

	capabilities, err := declarations.ReadFile(root + "capabilities.json")
	if err != nil {
		return Contract{}, fmt.Errorf("catalog capabilities unavailable: %w", err)
	}
	var capabilityDocument struct {
		Version      int                     `json:"version"`
		Capabilities []pluginhost.Capability `json:"capabilities"`
	}
	if err := decodeStrict(capabilities, &capabilityDocument); err != nil || capabilityDocument.Version != 1 {
		return Contract{}, errors.New("invalid catalog capability declaration")
	}
	out.Capabilities = capabilityDocument.Capabilities

	ui, err := declarations.ReadFile(root + "ui.schema.json")
	if err != nil || !json.Valid(ui) {
		return Contract{}, errors.New("invalid catalog UI declaration")
	}
	out.UI = append(json.RawMessage(nil), ui...)
	return out, nil
}

func decodeStrict(raw []byte, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("trailing declaration data")
	}
	return nil
}

// List preserves the prior catalog API for consumers that only need package
// summaries. New code that needs schemas or the install digest should call
// ContractFor instead.
func List() ([]pluginhost.Package, error) {
	names, err := fs.Glob(declarations, "*/plugin.json")
	if err != nil {
		return nil, err
	}
	result := make([]pluginhost.Package, 0, len(names))
	for _, name := range names {
		contract, err := ContractFor(path.Dir(name))
		if err != nil {
			return nil, err
		}
		result = append(result, contract.Package)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
