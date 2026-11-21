package companion

// Model replies may propose an audience and source IDs, but only the host may
// mint a verified reply proof. A model can echo proof fields from a previous
// tool result or conversation; do not treat that echo as an existing proof.
// Normalization resolves the proposed IDs and signs fresh evidence afterward.
func clearModelReplyProof(d *Decision) {
	d.ReviewFailure = nil
	for i := range d.ReplyContexts {
		r := &d.ReplyContexts[i]
		r.EvidenceEpoch = ""
		r.EvidenceAuthorization = ""
		r.SourceVersions = nil
		r.AnchorEventID = ""
		r.ContextVersion = ""
		r.State = ""
	}
}
