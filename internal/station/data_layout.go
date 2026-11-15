package station

import (
	"os"
	"path/filepath"
)

// The release data root is deliberately divided into three readable private
// areas.  The fallback paths keep older homes readable during migration; a
// new directory wins as soon as it exists, so a migration never needs a
// duplicate compatibility copy.
func stationApplicationsDir(dataDir string) string {
	if structuredDataRoot(dataDir) {
		return filepath.Join(dataDir, "applications")
	}
	return preferDataDirectory(dataDir, "applications", "apps")
}

func stationApplicationsRelative(dataDir string) string {
	rel, err := filepath.Rel(dataDir, stationApplicationsDir(dataDir))
	if err != nil || rel == "." {
		return "apps"
	}
	return rel
}

func stationPublicToolsDir(dataDir string) string {
	if structuredDataRoot(dataDir) {
		return filepath.Join(dataDir, "publictools")
	}
	return preferDataDirectory(dataDir, "publictools", "project-tools")
}

func stationMainDir(dataDir string) string {
	if !structuredDataRoot(dataDir) {
		return dataDir
	}
	return filepath.Join(dataDir, "main_setting")
}

func stationAuthorityDir(dataDir string) string {
	main := stationMainDir(dataDir)
	if main != dataDir || directoryExists(filepath.Join(main, "authority")) {
		return filepath.Join(main, "authority")
	}
	return dataDir
}

func stationMemoryDir(dataDir string) string {
	main := stationMainDir(dataDir)
	if main != dataDir || directoryExists(filepath.Join(main, "memory")) {
		return filepath.Join(main, "memory")
	}
	return filepath.Join(dataDir, "local-companion")
}

func stationWorkspaceDir(dataDir string) string {
	if structuredDataRoot(dataDir) {
		return filepath.Join(dataDir, "workspace")
	}
	return filepath.Join(dataDir, "managed-workspaces")
}

func stationLogsDir(dataDir string) string {
	main := stationMainDir(dataDir)
	if main != dataDir || directoryExists(filepath.Join(main, "logs")) {
		return filepath.Join(main, "logs")
	}
	return filepath.Join(dataDir, "logs")
}

func stationObservationsDir(dataDir string) string {
	main := stationMainDir(dataDir)
	if main != dataDir || directoryExists(filepath.Join(main, "observations")) {
		return filepath.Join(main, "observations")
	}
	return filepath.Join(dataDir, "app-observations")
}

// stationNativeLifecycleRelative keeps native locks and stop proofs inside
// main_setting for structured homes. Legacy homes continue using their
// historical root-level filenames during migration.
func stationNativeLifecycleRelative(dataDir, name string) string {
	if structuredDataRoot(dataDir) && directoryExists(filepath.Join(dataDir, "main_setting")) {
		return filepath.Join("main_setting", name)
	}
	return name
}

// DefaultWorkspaceRoot is exported for the command entry point and legacy
// migration commands. It keeps old tests and homes on their former path while
// new release data uses data/main/workspaces.
func DefaultWorkspaceRoot(dataDir string) string { return stationWorkspaceDir(dataDir) }

// ApplicationsRoot and PublicToolsRoot expose the same layout to the command
// entry point without duplicating migration-aware path rules.
func ApplicationsRoot(dataDir string) string { return stationApplicationsDir(dataDir) }
func PublicToolsRoot(dataDir string) string  { return stationPublicToolsDir(dataDir) }
func AuthorityRoot(dataDir string) string    { return stationAuthorityDir(dataDir) }

func stationToolPrivateDir(dataDir, tool string) string {
	if structuredDataRoot(dataDir) {
		return filepath.Join(stationPublicToolsDir(dataDir), tool, "private")
	}
	return filepath.Join(dataDir, "station-"+tool)
}

func stationToolRoot(dataDir, tool string) string {
	if structuredDataRoot(dataDir) {
		return filepath.Join(stationPublicToolsDir(dataDir), tool)
	}
	return dataDir
}

func stationToolLogsDir(dataDir, tool string) string {
	if structuredDataRoot(dataDir) {
		return filepath.Join(stationPublicToolsDir(dataDir), tool, "logs")
	}
	return filepath.Join(dataDir, "station-"+tool+"-logs")
}

func preferDataDirectory(root, preferred, legacy string) string {
	if preferred != "" && (directoryExists(filepath.Join(root, preferred)) || legacy == "") {
		return filepath.Join(root, preferred)
	}
	if legacy != "" {
		return filepath.Join(root, legacy)
	}
	return root
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func structuredDataRoot(root string) bool {
	return directoryExists(filepath.Join(root, "applications")) ||
		directoryExists(filepath.Join(root, "main")) ||
		directoryExists(filepath.Join(root, "publictools")) ||
		regularFileExists(filepath.Join(root, "data-layout.json"))
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
