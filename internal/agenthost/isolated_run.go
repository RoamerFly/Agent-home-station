package agenthost

import (
	"context"
	"errors"
	"strings"
)

// RunIsolated executes a trusted broker's fixed argv in the same mandatory
// system sandbox as Agent programs. It never exposes host credentials or falls
// back to a host shell. The caller controls deadline/cancellation and mounts.
func RunIsolated(ctx context.Context, config Config, cwd string, argv []string) ([]byte, error) {
	if config.Isolation == nil || len(argv) == 0 {
		return nil, errors.New("sandbox_required")
	}
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		if strings.ContainsRune(arg, 0) {
			return nil, errors.New("invalid_isolated_argv")
		}
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
	}
	cmd, e := isolatedCommand(ctx, config, cwd, strings.Join(quoted, " "))
	if e != nil {
		return nil, e
	}
	out := &limitedOutput{limit: config.OutputBytes}
	cmd.Stdout = out
	// Raw runtime diagnostics can contain local file paths; do not merge them
	// into the structured result given to the model.
	if e = cmd.Run(); e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return out.data, errors.New("isolated_program_failed")
	}
	if out.truncated {
		return nil, errors.New("isolated_output_incomplete")
	}
	return out.data, nil
}
