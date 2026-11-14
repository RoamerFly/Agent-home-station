package admin

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

// mobileObservationAppGrantsKey is host-owned state.  It intentionally lives
// beside the mobile workflow state rather than inside a package configuration:
// an application may be allowed by the device profile without being granted to
// this fish for autonomous, read-only observation.
const mobileObservationAppGrantsKey = "observation-app-grants"

// mobileObservationAppGrant binds a single app permission to one exact device
// binding generation.  BindingID is an opaque, revocable generation token;
// never substitute a later binding for it while reading this record.
type mobileObservationAppGrant struct {
	GrantID   string    `json:"grant_id"`
	Account   string    `json:"account"`
	FishID    string    `json:"fish_id"`
	DeviceID  string    `json:"device_id"`
	BindingID string    `json:"binding_id"`
	Package   string    `json:"package"`
	CreatedAt time.Time `json:"created_at"`
}

type mobileObservationAppGrantState struct {
	Grants []mobileObservationAppGrant `json:"grants"`
}

// mobileObservationAppGrantView intentionally exposes no host paths, device
// serials, or credentials.  Available describes the current profile only; a
// stored grant may remain visible but unavailable so its owner can revoke it
// after a profile change or a broken runtime.
type mobileObservationAppGrantView struct {
	GrantID   string `json:"grant_id"`
	FishID    string `json:"fish_id"`
	DeviceID  string `json:"device_id"`
	BindingID string `json:"binding_id"`
	Package   string `json:"package"`
	Available bool   `json:"available"`
}

var mobileObservationPackageName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z][A-Za-z0-9_]*)+$`)

func decodeMobileObservationAppGrants(raw json.RawMessage) (mobileObservationAppGrantState, error) {
	state := mobileObservationAppGrantState{Grants: []mobileObservationAppGrant{}}
	if len(raw) == 0 {
		return state, nil
	}
	if json.Unmarshal(raw, &state) != nil || state.Grants == nil {
		return mobileObservationAppGrantState{}, errors.New("device_repository_invalid")
	}
	seenID, seenScope := map[string]bool{}, map[string]bool{}
	for _, grant := range state.Grants {
		scope := grant.Account + "\x00" + grant.FishID + "\x00" + grant.DeviceID + "\x00" + grant.BindingID + "\x00" + grant.Package
		if grant.GrantID == "" || grant.Account == "" || grant.FishID == "" || grant.DeviceID == "" || grant.BindingID == "" || !mobileObservationPackageName.MatchString(grant.Package) || len(grant.Package) > 255 || grant.CreatedAt.IsZero() || seenID[grant.GrantID] || seenScope[scope] {
			return mobileObservationAppGrantState{}, errors.New("device_repository_invalid")
		}
		seenID[grant.GrantID], seenScope[scope] = true, true
	}
	return state, nil
}

func (s *mobileWorkflowService) observationAppGrants(snapshot mobileWorkflowSnapshot) (mobileObservationAppGrantState, error) {
	state, err := decodeMobileObservationAppGrants(snapshot.host.Records[mobileObservationAppGrantsKey])
	if err != nil {
		return mobileObservationAppGrantState{}, err
	}
	account := s.manager.native.Account()
	for _, grant := range state.Grants {
		if grant.Account != account {
			return mobileObservationAppGrantState{}, errors.New("device_repository_invalid")
		}
	}
	return state, nil
}

func currentObservationAppGrants(state mobileObservationAppGrantState, deviceID, fishID, bindingID string) []mobileObservationAppGrant {
	if deviceID == "" || fishID == "" || bindingID == "" {
		return []mobileObservationAppGrant{}
	}
	grants := make([]mobileObservationAppGrant, 0)
	for _, grant := range state.Grants {
		if grant.DeviceID == deviceID && grant.FishID == fishID && grant.BindingID == bindingID {
			grants = append(grants, grant)
		}
	}
	sort.Slice(grants, func(i, j int) bool {
		if grants[i].Package == grants[j].Package {
			return grants[i].GrantID < grants[j].GrantID
		}
		return grants[i].Package < grants[j].Package
	})
	return grants
}

func mobileObservationAppAllowed(grant mobileDeviceOverview, packageName string) bool {
	return contains(grant.AuthorizedApps, packageName) && contains(grant.AuthorizedActions, "observe")
}

// ObservationAppGrant is the read-only, current-generation authorization
// lookup reserved for the following autonomous-observation phase.  It does
// not wake a device, modify a task, or make an application authorization.
func (s *mobileWorkflowService) ObservationAppGrant(deviceID, packageName string) (mobileObservationAppGrant, bool, error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	snapshot, _, err := s.load(false)
	if err != nil {
		return mobileObservationAppGrant{}, false, err
	}
	return s.observationAppGrant(snapshot, deviceID, packageName)
}

func (s *mobileWorkflowService) observationAppGrant(snapshot mobileWorkflowSnapshot, deviceID, packageName string) (mobileObservationAppGrant, bool, error) {
	state, err := s.observationAppGrants(snapshot)
	if err != nil {
		return mobileObservationAppGrant{}, false, err
	}
	fishID, bindingID, fishBound, err := s.manager.mobileAccountDeviceBinding(deviceID)
	if err != nil {
		return mobileObservationAppGrant{}, false, err
	}
	if !snapshot.settings.Enabled || !contains(snapshot.settings.Devices, deviceID) || !fishBound || bindingID == "" {
		return mobileObservationAppGrant{}, false, nil
	}
	profile, ok := s.manager.mobileDeviceGrantSummary(snapshot.settings)[deviceID]
	if !ok || !contains(profile.Packages, packageName) || !contains(profile.Actions, "observe") {
		return mobileObservationAppGrant{}, false, nil
	}
	for _, grant := range currentObservationAppGrants(state, deviceID, fishID, bindingID) {
		if grant.Package == packageName {
			return grant, true, nil
		}
	}
	return mobileObservationAppGrant{}, false, nil
}

// SetObservationAppGrant changes only the fish's app-level read-only grant.
// It neither resumes a paused device nor opens the device broker.  The caller
// must present the current binding ID, so an old dashboard cannot authorize a
// newly rebound device generation.
func (s *mobileWorkflowService) SetObservationAppGrant(deviceID, bindingID, packageName string, enabled bool) (mobileOverview, error) {
	deviceID, bindingID, packageName = strings.TrimSpace(deviceID), strings.TrimSpace(bindingID), strings.TrimSpace(packageName)
	if deviceID == "" || bindingID == "" || !mobileObservationPackageName.MatchString(packageName) || len(packageName) > 255 {
		return mobileOverview{}, errors.New("device_observation_app_grant_invalid")
	}
	s.stateMu.Lock()
	snapshot, store, err := s.load(true)
	if err == nil && !contains(snapshot.settings.Devices, deviceID) {
		err = errors.New("device_not_authorized")
	}
	if err != nil {
		s.stateMu.Unlock()
		return mobileOverview{}, err
	}
	fishID, currentBindingID, fishBound, bindingErr := s.manager.mobileAccountDeviceBinding(deviceID)
	if bindingErr != nil {
		s.stateMu.Unlock()
		return mobileOverview{}, bindingErr
	}
	if !fishBound || currentBindingID == "" || bindingID != currentBindingID {
		s.stateMu.Unlock()
		return mobileOverview{}, errors.New("device_fish_binding_changed")
	}
	state, err := s.observationAppGrants(snapshot)
	if err != nil {
		s.stateMu.Unlock()
		return mobileOverview{}, err
	}
	// Revocation must remain possible while a package is missing, a profile is
	// broken, or the app was removed from that profile.  Only a new grant is
	// subject to the present technical profile ACL.
	if enabled {
		grants := s.manager.mobileDeviceGrantSummary(snapshot.settings)
		profile, ok := grants[deviceID]
		if !ok || !contains(profile.Packages, packageName) || !contains(profile.Actions, "observe") {
			s.stateMu.Unlock()
			return mobileOverview{}, errors.New("device_observation_app_not_allowed")
		}
	}
	changed := false
	filtered := make([]mobileObservationAppGrant, 0, len(state.Grants)+1)
	found := false
	for _, grant := range state.Grants {
		match := grant.DeviceID == deviceID && grant.FishID == fishID && grant.BindingID == currentBindingID && grant.Package == packageName
		if match {
			found = true
			if !enabled {
				changed = true
				continue
			}
		}
		filtered = append(filtered, grant)
	}
	if enabled && !found {
		filtered = append(filtered, mobileObservationAppGrant{
			GrantID: "observe-app-" + randomToken(), Account: s.manager.native.Account(), FishID: fishID, DeviceID: deviceID, BindingID: currentBindingID, Package: packageName,
			CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		})
		changed = true
	}
	if changed {
		state.Grants = filtered
		raw, _ := json.Marshal(state)
		err = s.commit(snapshot, store, map[string]json.RawMessage{mobileObservationAppGrantsKey: raw})
	}
	s.stateMu.Unlock()
	if err != nil {
		return mobileOverview{}, err
	}
	return s.Overview(), nil
}
