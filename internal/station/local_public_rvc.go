package station

import (
	"context"
	"errors"
)

// publicRVCVoices reads only the workstation's installed capability and
// registered voice metadata. No application source or reply target enters it.
func (s *localProjectTools) publicRVCVoices(ctx context.Context, fish string) ([]localRVCVoice, error) {
	if s == nil || s.rvc == nil || !s.rvcRegistered(fish) {
		return nil, errors.New("工作站 RVC 未登记")
	}
	host, err := s.forFish(fish)
	if err != nil {
		return nil, err
	}
	if _, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localRVCPackage, capability: localRVCCapability}); err != nil {
		return nil, err
	}
	info, err := s.rvc.Info(ctx, fish)
	if err != nil || len(info.Profiles) == 0 {
		return nil, errors.New("工作站 RVC 声线当前不可用")
	}
	return info.Profiles, nil
}
