package bot

// Only explicit identifiers bind a named voice. Titles and friendly labels
// cannot certify a model, and unbound legacy files remain unbound.
func (d *mediaDelivery) voiceMismatch() string {
	if d.Kind != "voice" && d.Kind != "file" && d.Kind != "video" {
		return ""
	}
	if d.Input.ExpectedVoiceID != "" && d.VoiceID != d.Input.ExpectedVoiceID {
		return "media_expected_voice_mismatch"
	}
	if d.Input.ExpectedVoiceProfile != "" && d.VoiceProfile != d.Input.ExpectedVoiceProfile {
		return "media_expected_voice_profile_mismatch"
	}
	return ""
}
