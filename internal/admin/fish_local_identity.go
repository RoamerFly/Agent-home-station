package admin

import "errors"

// localIdentity returns the durable local fish record without creating one.
// This makes an unconfigured installation observable without turning a status
// read into either a filesystem write or a new home-directory authorization.
func (r *fishIdentityRegistry) localIdentity() (fishIdentityRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	release, err := r.acquire()
	if err != nil {
		return fishIdentityRecord{}, err
	}
	defer release()
	return r.readHeld()
}

// initializeLocalIdentity explicitly creates the one local fish and its owner.
// It is deliberately idempotent: retrying initialization must not silently
// re-enable a home that its owner has paused.
func (r *fishIdentityRegistry) initializeLocalIdentity() (fishIdentityRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	release, err := r.acquire()
	if err != nil {
		return fishIdentityRecord{}, err
	}
	defer release()

	record, err := r.readHeld()
	if err != nil {
		return fishIdentityRecord{}, err
	}
	changed := false
	if record.FishID == "" {
		record.FishID = "fish-" + randomToken()[:48]
		changed = true
	}
	if record.LocalOwner == nil {
		record.LocalOwner = &fishLocalOwner{
			ID:         fishLocalOwnerIDPrefix + randomToken()[:fishLocalOwnerTokenChars],
			Revision:   1,
			HomeAccess: true,
		}
		changed = true
	}
	if changed {
		// write normalizes on a value copy; make the returned record reflect the
		// persisted schema version as well.
		record.Version = fishIdentityCurrentVersion
		if err := r.write(record); err != nil {
			return fishIdentityRecord{}, err
		}
	}
	return record, nil
}

// setLocalHomeAccess performs a compare-and-swap on the grant so an older
// settings page cannot reverse a newer pause or resume. Repeating a successful
// request with the current revision is intentionally a no-op.
func (r *fishIdentityRegistry) setLocalHomeAccess(expected uint64, enabled bool) (fishIdentityRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	release, err := r.acquire()
	if err != nil {
		return fishIdentityRecord{}, err
	}
	defer release()

	record, err := r.readHeld()
	if err != nil {
		return fishIdentityRecord{}, err
	}
	if record.LocalOwner == nil {
		return fishIdentityRecord{}, errLocalHomeNotInitialized
	}
	if record.LocalOwner.Revision != expected {
		return fishIdentityRecord{}, errLocalHomeRevisionConflict
	}
	if record.LocalOwner.HomeAccess == enabled {
		return record, nil
	}
	record.LocalOwner.HomeAccess = enabled
	record.LocalOwner.Revision++
	if record.LocalOwner.Revision == 0 {
		return fishIdentityRecord{}, errors.New("本机家目录授权版本已耗尽")
	}
	if err := r.write(record); err != nil {
		return fishIdentityRecord{}, err
	}
	return record, nil
}
