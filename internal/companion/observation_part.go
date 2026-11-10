package companion

import (
	"slices"
	"strings"
)

// MediaObservationEvidence carries firsthand excerpts and the range observed.
// Publisher metadata may accompany the text returned to a model but does not
// qualify as firsthand evidence.
type MediaObservationEvidence struct {
	ObservedRange string
	Excerpt       string
	Parts         []ObservationPart
}

// ObservationPart attributes an excerpt to the channel that supplied it.
// The enclosing event or tool job supplies identity, version and access scope.
// Excerpts, including text visible inside an image, are external content.
type ObservationPart struct {
	Kind    string `json:"kind"`
	Range   string `json:"range,omitempty"`
	Excerpt string `json:"external_text_excerpt"`
}

const (
	ObservationPublisherDescription = "publisher_description"
	ObservationPageBody             = "page_body"
	ObservationVisualFrames         = "visual_frames"
	ObservationAudioTranscript      = "audio_transcript"
)

// Firsthand identifies content actually returned by a reader or observer.
// Authorization and range checks belong to the host that created the part.
func (p ObservationPart) Firsthand() bool {
	switch p.Kind {
	case ObservationPageBody, ObservationVisualFrames, ObservationAudioTranscript:
		return strings.TrimSpace(p.Excerpt) != ""
	}
	return false
}

// HasFirsthandObservation reports whether a reader returned any actual content.
func HasFirsthandObservation(parts []ObservationPart) bool {
	return slices.ContainsFunc(parts, ObservationPart.Firsthand)
}

// CardDescriptionOnly recognizes prior host-labelled cards without assigning
// modalities to old content whose provenance is unknown.
func (m IncomingMediaRef) CardDescriptionOnly() bool {
	if len(m.ObservationParts) != 0 {
		return !HasFirsthandObservation(m.ObservationParts)
	}
	return strings.HasPrefix(m.Observation, "[收到分享卡片，仅依据卡片标题与简介") || strings.HasPrefix(m.Observation, "[分享/视频号卡片，仅标题与简介")
}
