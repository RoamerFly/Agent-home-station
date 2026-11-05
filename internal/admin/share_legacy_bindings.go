package admin

import (
	"errors"

	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/sharespec"
)

// legacyShareBindingURL reads a pre-plugin, administrator-created share URL
// only for the exact received source that it was bound to. It never creates a
// binding from a model argument, searches another conversation, or treats an
// object ID as global across accounts or threads.
//
// The caller may use the returned URL before asking the native client for a
// new short link. A missing legacy binding is not an error; a malformed or
// cross-scope matching record is an error so a corrupted store cannot steer a
// plugin task to a different share.
func legacyShareBindingURL(dir string, source linuxnative.ChannelsSource) (string, bool, error) {
	if source.AccountID == "" || source.ThreadID == "" || source.MessageID == "" {
		return "", false, errors.New("share_legacy_binding_scope_invalid")
	}
	bindings, err := loadChannelsBindings(dir)
	if err != nil {
		return "", false, errors.New("share_legacy_binding_store_invalid")
	}
	keys := []string{channelBindingID(source.AccountID, source.ThreadID, source.MessageID)}
	if source.ObjectID != "" {
		keys = append(keys, channelBindingID(source.AccountID, source.ThreadID, "object:"+source.ObjectID))
	}
	for index, key := range keys {
		binding, found := bindings[key]
		if !found {
			continue
		}
		messageMatches := binding.MessageID == source.MessageID
		objectMatches := source.ObjectID != "" && binding.ObjectID != "" && binding.ObjectID == source.ObjectID
		// A message key must bind that precise message. The object form is an
		// explicit legacy alias and may only bind the same scoped object.
		identityMatches := messageMatches
		if index == 1 {
			identityMatches = objectMatches
		}
		if binding.Account != source.AccountID || binding.Thread != source.ThreadID || !identityMatches {
			return "", false, errors.New("share_legacy_binding_scope_mismatch")
		}
		url, err := sharespec.ShareURL(binding.URL)
		if err != nil {
			return "", false, errors.New("share_legacy_binding_invalid")
		}
		return url, true, nil
	}
	return "", false, nil
}
