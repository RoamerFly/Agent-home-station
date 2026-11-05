package admin

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

// documentWorkshopOCR is the one small bridge from a task-materialized image
// to the fixed initial-process OCR broker. It never accepts paths, language,
// executable names or command flags from the model or plugin.
func (p *permittedTools) documentWorkshopOCR(ctx context.Context, policy *documentWorkshopPolicy) (*pluginruntime.DocumentOCRObservation, error) {
	if runtime.GOOS != "windows" || policy == nil || policy.capability != "document.inspect" || len(policy.materialized) != 1 {
		return nil, nil
	}
	input := policy.materialized[0]
	if !strings.HasPrefix(strings.ToLower(input.MIME), "image/") {
		return nil, nil
	}
	options, err := runtimeDeploymentOptions()
	if err != nil || options.DocumentRuntimeRoot == "" {
		return nil, errors.New("document_ocr_broker_unavailable")
	}
	installed, err := policy.store.Installed(ctx, documentWorkshopPluginID)
	if err != nil || !installed.Enabled || installed.Removed {
		return nil, errors.New("document_ocr_broker_unavailable")
	}
	executor := pluginruntime.InstalledExecutor(policy.store, options)
	spec, err := executor.Resolve(ctx, installed.Package, pluginhost.Grant{Account: policy.invocation.Account, Plugin: documentWorkshopPluginID, Version: installed.Package.Version, Permissions: installed.Permissions})
	if err != nil {
		return nil, errors.New("document_ocr_broker_unavailable")
	}
	// Match the normal document task mount exactly; this broker is not a
	// privileged side channel. The input gets an explicit RX file grant and its
	// parent exists only for path traversal.
	spec.DataDir = input.OutputDir
	spec.ReadOnlyDirs = append(spec.ReadOnlyDirs, filepath.Dir(input.InputPath))
	spec.ReadOnlyFiles = append(spec.ReadOnlyFiles, input.InputPath)
	observation, err := pluginruntime.RunDocumentOCR(ctx, spec, pluginruntime.DocumentOCRRequest{RuntimeRoot: options.DocumentRuntimeRoot, InputPath: input.InputPath, InputSHA256: input.SHA256})
	if err != nil {
		return nil, err
	}
	return &observation, nil
}
