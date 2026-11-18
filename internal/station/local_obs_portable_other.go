//go:build !windows

package station

import (
	"context"
	"errors"
)

func (s *localProjectTools) configurePortableOBS(context.Context, *localProjectToolHost) error {
	return nil
}

func (s *localProjectTools) preparePortableOBSPayload(r localOBSDeployment, _ int, _ string) (localOBSDeployment, error) {
	return r, errors.New("便携 OBS 预览需要 Windows")
}
