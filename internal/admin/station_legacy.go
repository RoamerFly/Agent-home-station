package admin

// This file bridges the station's single-account storage into the legacy
// WeChat console.  The bridge is intentionally one-way and only runs when the
// legacy console is explicitly enabled by the desktop host.

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

type stationLegacyModel struct {
	Provider        string `json:"provider"`
	BaseURL         string `json:"base_url"`
	Model           string `json:"model"`
	MaxTokens       int    `json:"max_tokens"`
	ThinkingEnabled bool   `json:"thinking_enabled"`
	ReasoningEffort string `json:"reasoning_effort"`
	Persona         string `json:"persona"`
	APIKey          string `json:"api_key"`
	KeyConfigured   bool   `json:"key_configured"`
}

type stationLegacyGroupRule struct {
	Mode                    string `json:"mode"`
	Prefix                  string `json:"prefix"`
	Persona                 string `json:"persona"`
	ForceReplyWhenMentioned bool   `json:"force_reply_when_mentioned"`
}

type stationLegacyWorkerPolicy struct {
	Enabled            bool                              `json:"enabled"`
	Paused             bool                              `json:"paused"`
	ReplyGrants        map[string]string                 `json:"reply_grants"`
	StationImageVision bool                              `json:"station_image_vision"`
	StationVideoVision bool                              `json:"station_video_vision"`
	GroupRules         map[string]stationLegacyGroupRule `json:"group_rules"`
}

// PrepareStationLegacyData creates the old manager's config from the station
// model and worker policy, without printing or returning secrets. Existing
// legacy files are authoritative and are never overwritten.
func PrepareStationLegacyData(legacyDir, modelFile, workerDir string) error {
	if legacyDir == "" || modelFile == "" || workerDir == "" {
		return errors.New("legacy station paths are required")
	}
	if err := os.MkdirAll(legacyDir, 0700); err != nil {
		return err
	}
	configPath := filepath.Join(legacyDir, "config.json")
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		model, modelErr := readStationLegacyModel(modelFile)
		if modelErr != nil && !errors.Is(modelErr, os.ErrNotExist) {
			return modelErr
		}
		policy, policyErr := readStationLegacyPolicy(filepath.Join(workerDir, "worker.json"))
		if policyErr != nil && !errors.Is(policyErr, os.ErrNotExist) {
			return policyErr
		}
		// A fresh install has no account policy or model yet. Let the console
		// use its own disabled defaults until the owner connects an account.
		if modelErr == nil && policyErr == nil {
			config := stationLegacyConfig(model, policy)
			data, err := json.MarshalIndent(config, "", "  ")
			if err != nil {
				return err
			}
			if err := writePrivate(configPath, data); err != nil {
				return err
			}
		}
	} else if err != nil {
		return err
	}
	// The station's memory is already merged and source-scoped. Copy it once
	// into the directory expected by the old manager; never overwrite a legacy
	// database that an operator has subsequently edited.
	if err := copyTreeIfMissing(filepath.Join(workerDir, "memory"), filepath.Join(legacyDir, "companion")); err != nil {
		return err
	}
	// A real old admin journal may have been restored beside the station
	// workers. It is copied as-is so the old console can project those genuine
	// rounds; memory events and synthetic fixtures are never converted here.
	activitySource := filepath.Join(filepath.Dir(filepath.Dir(workerDir)), "legacy-activity.json")
	activityTarget := filepath.Join(legacyDir, "activity.json")
	if _, err := os.Stat(activityTarget); errors.Is(err, os.ErrNotExist) {
		if _, sourceErr := os.Stat(activitySource); sourceErr == nil {
			return copyFileIfMissing(activitySource, activityTarget)
		}
	}
	return nil
}

func readStationLegacyModel(path string) (stationLegacyModel, error) {
	var model stationLegacyModel
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return model, os.ErrNotExist
	}
	if err != nil || json.Unmarshal(data, &model) != nil || model.BaseURL == "" || model.Model == "" || model.APIKey == "" && model.KeyConfigured {
		return model, errors.New("station legacy model configuration unavailable")
	}
	return model, nil
}

func readStationLegacyPolicy(path string) (stationLegacyWorkerPolicy, error) {
	var policy stationLegacyWorkerPolicy
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return policy, os.ErrNotExist
	}
	if err != nil || json.Unmarshal(data, &policy) != nil || policy.ReplyGrants == nil {
		return policy, errors.New("station legacy worker policy unavailable")
	}
	return policy, nil
}

func stationLegacyConfig(model stationLegacyModel, policy stationLegacyWorkerPolicy) Config {
	c := defaults()
	c.Provider, c.BaseURL, c.Model, c.APIKey = model.Provider, model.BaseURL, model.Model, model.APIKey
	c.MaxTokens, c.ThinkingEnabled, c.ReasoningEffort = model.MaxTokens, model.ThinkingEnabled, model.ReasoningEffort
	c.Enabled = policy.Enabled && !policy.Paused
	c.FilehelperEnabled, c.PrivateEnabled, c.PrivateAllFriends = false, false, false
	c.Companion = companion.Defaults()
	c.Companion.Enabled = true
	c.Companion.Persona = model.Persona
	c.Companion.Permissions.ReplyText = true
	c.Companion.Permissions.Remember = true
	c.Companion.Permissions.ObserveGroups = false
	c.Companion.Permissions.AutonomousGroups = false
	c.Companion.Permissions.Proactive = false
	c.Behavior = bot.DefaultOptions()
	c.Behavior.ReadImages, c.Behavior.ReadVideos = policy.StationImageVision, policy.StationVideoVision
	c.GroupRules = makeStationLegacyGroupRules(policy)
	return c
}

func makeStationLegacyGroupRules(policy stationLegacyWorkerPolicy) []GroupRule {
	result := make([]GroupRule, 0, len(policy.ReplyGrants))
	for id := range policy.ReplyGrants {
		raw := policy.GroupRules[id]
		mode := raw.Mode
		if mode == "" {
			mode = "mention"
		}
		result = append(result, GroupRule{ID: id, Mode: mode, Prefix: raw.Prefix, Persona: raw.Persona, ForceReplyWhenMentioned: raw.ForceReplyWhenMentioned})
	}
	return result
}

func copyTreeIfMissing(source, destination string) error {
	if _, err := os.Stat(filepath.Join(destination, "memory.sqlite")); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		return errors.Join(copyErr, closeErr)
	}); err != nil {
		return err
	}
	return nil
}

func copyFileIfMissing(source, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	return errors.Join(copyErr, closeErr)
}
