package admin

import (
	"context"
	"errors"
	"slices"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

// Readiness describes admission for this account; it is not a live service probe.
type pluginPackageReadiness struct {
	State                 string                        `json:"state"`
	MissingRequiredFields []pluginReadinessMissingField `json:"missing_required_fields"`
	Reasons               []pluginReadinessReason       `json:"reasons"`
}

type pluginReadinessMissingField struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type pluginReadinessReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func readinessReason(state, code, message string) pluginPackageReadiness {
	return pluginPackageReadiness{State: state, Reasons: []pluginReadinessReason{{Code: code, Message: message}}}
}

// Only open the field metadata when the real startup validator found a
// configuration problem on this package. Never serialize configuration values.
func missingRequiredFields(ctx context.Context, store *pluginhost.Store, id string) []pluginReadinessMissingField {
	schema, err := store.ConfigSchema(ctx, id)
	if err != nil || len(schema.Required) == 0 {
		return nil
	}
	config, err := store.Configuration(ctx, id)
	if err != nil {
		return nil
	}
	var missing []pluginReadinessMissingField
	for _, name := range schema.Required {
		field := schema.Properties[name]
		_, present := config.Values[name]
		if present || len(field.Default) > 0 || slices.Contains(config.ConfiguredSecrets, name) {
			continue
		}
		label := field.Title
		if label == "" {
			label = name
		}
		missing = append(missing, pluginReadinessMissingField{ID: name, Label: label})
	}
	return missing
}

func pluginReadiness(ctx context.Context, store *pluginhost.Store, controller *pluginruntime.Controller, installed pluginhost.InstalledPackage, connected bool) pluginPackageReadiness {
	if controller == nil {
		return readinessReason("runtime_unavailable", "runtime_controller_unavailable", "工具执行环境状态不可用")
	}
	err := controller.CheckEnableOnDemand(ctx, installed.Package.ID, installed.Revision)
	if err != nil {
		var dependency *pluginruntime.DependencyError
		if !errors.As(err, &dependency) {
			return readinessReason("unknown", "preflight_incomplete", "启用条件未能核对，请刷新工具目录")
		}
		result := readinessReason("not_enableable", dependency.Code, pluginDependencyMessage(dependency))
		if dependency.Code == "dependency_configuration" {
			result.State = "needs_configuration"
			if dependency.Dependency == installed.Package.ID {
				result.MissingRequiredFields = missingRequiredFields(ctx, store, installed.Package.ID)
			}
		}
		return result
	}
	if !connected {
		return readinessReason("runtime_unavailable", "runtime_environment_unavailable", "工具执行环境未就绪")
	}
	return pluginPackageReadiness{State: "ready"}
}
