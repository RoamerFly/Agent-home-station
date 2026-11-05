package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/plugins"
)

type externalJobAuthority struct {
	task   agentTask
	phase  string
	poll   bool
	cached bool
}
type externalJobWireResult struct {
	Observation     externalHTTPObservation `json:"observation"`
	Ticket          plugins.JobTicket       `json:"ticket"`
	CancelAck       bool                    `json:"cancel_ack,omitempty"`
	KnownRemoteOnly bool                    `json:"known_remote_only,omitempty"`
}

var externalJobID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$`)

// Restore the original authenticated request, never the speaker that happened
// to trigger a later status/cancel call. Audit-only fields excluded by Input's
// JSON contract are retained by the existing task envelope.
func (m *Manager) externalJobTrace(task agentTask) (companion.Input, error) {
	trace := companion.Input{Channel: "wechat", Account: task.Account, Thread: task.Thread, Sender: task.Sender, Mode: "direct", RequestedText: task.RequestedText}
	if task.PluginJob != nil && task.PluginJob.Origin != nil {
		var err error
		trace, err = cloneProjectPluginTrace(*task.PluginJob.Origin)
		if err != nil {
			return trace, err
		}
	}
	trace.Account, trace.Thread, trace.Sender = task.Account, task.Thread, task.Sender
	trace.TraceID, trace.QuoteServerID, trace.TaskID = task.OriginTrace, task.QuoteServerID, task.ID
	if task.Constraints != nil {
		if trace.Source == nil {
			trace.Source = task.Constraints.Source
		}
		if trace.ReplyContext == nil {
			trace.ReplyContext = task.Constraints.ReplyContext
		}
	}
	return cloneProjectPluginTrace(trace)
}
func (g *externalJobAuthority) authorization(ctx context.Context, p *externalHTTPPolicy) (string, error) {
	phases := []string{"start", "poll", "cancel"}
	if g.poll {
		phases = []string{"start", "query"}
		if g.cached {
			phases = []string{"read"}
		}
	}
	if g.task.PluginJob == nil || !slices.Contains(phases, g.phase) {
		return "", errors.New("external_job_phase_denied")
	}
	if g.task.PluginJob.LegacyConfigurationSHA256 == "" || g.task.PluginJob.Origin == nil {
		return "", errors.New("external_job_original_proof_missing")
	}
	current, ok := p.p.manager.agent.lookupTask(g.task.ID, g.task.Account, g.task.Thread)
	if !ok || current.PluginJob == nil || !current.Updated.Equal(g.task.Updated) || current.Account != p.p.trace.Account || current.Thread != p.p.trace.Thread || current.Sender != p.p.trace.Sender || current.State != g.task.State || current.PluginJob.Polls != g.task.PluginJob.Polls || current.PluginJob.CancelState != g.task.PluginJob.CancelState || current.PluginJob.LegacyConfigurationSHA256 != g.task.PluginJob.LegacyConfigurationSHA256 || !reflect.DeepEqual(current.PluginJob.Origin, g.task.PluginJob.Origin) || !reflect.DeepEqual(current.PluginJob.Ticket, g.task.PluginJob.Ticket) || !reflect.DeepEqual(current.Constraints, g.task.Constraints) || !reflect.DeepEqual(current.PluginJob.PollState, g.task.PluginJob.PollState) {
		return "", errors.New("external_job_task_changed")
	}
	if err := p.p.manager.validateTaskSource(current); err != nil {
		return "", err
	}
	if current.OwnerRequested && !p.p.privileged() {
		return "", errors.New("external_job_owner_revoked")
	}
	if g.phase == "cancel" {
		if current.State != "cancelled" || current.PluginJob.CancelState != "claimed" || !validExternalRemoteID(current.PluginJob.Ticket.RemoteID) {
			return "", errors.New("external_job_cancel_claim_required")
		}
	} else {
		if g.cached {
			if current.State != "running" || current.PluginJob.Ticket.State != "completed" {
				return "", errors.New("external_poll_result_not_claimed")
			}
		} else if current.State != "waiting" && current.State != "pending" {
			return "", errors.New("external_job_task_not_waiting")
		}
		if g.phase == "start" && (current.PluginJob.Ticket.State != "submitting" || current.PluginJob.Ticket.RemoteID != "") {
			return "", errors.New("external_job_start_not_replayable")
		}
		if g.phase == "query" && (current.PluginJob.PollState == nil || current.PluginJob.PollState.Initial == nil || current.PluginJob.Ticket.State != "waiting" || current.PluginJob.Ticket.RemoteID != "") {
			return "", errors.New("external_poll_initial_response_required")
		}
		if g.phase == "poll" && (current.PluginJob.Ticket.State != "waiting" || !validExternalRemoteID(current.PluginJob.Ticket.RemoteID)) {
			return "", errors.New("external_job_original_remote_id_required")
		}
	}
	auth := p.p.externalHTTPAuthorizationFor(p.tool, g.phase == "cancel")
	if auth == "" {
		return "", errors.New("external_job_authorization_denied")
	}
	proof, _ := json.Marshal([]any{current.Account, current.Thread, current.Sender, current.OwnerRequested, current.OriginTrace, current.Constraints, current.PluginJob.Origin})
	return agentID(auth, namedSpeechSHA(proof)), nil
}
func validExternalRemoteID(id string) bool {
	return id != "." && id != ".." && externalJobID.MatchString(id)
}

func (m *Manager) callExternalJobPhase(ctx context.Context, task agentTask, phase string, args json.RawMessage) (plugins.JobTicket, error) {
	if task.PluginJob == nil {
		return plugins.JobTicket{}, errors.New("external_job_task_missing")
	}
	if task.PluginJob.LegacyConfigurationSHA256 == "" {
		return task.PluginJob.Ticket, errors.New("legacy_configuration_proof_missing")
	}
	if task.PluginJob.Origin == nil {
		return task.PluginJob.Ticket, errors.New("legacy_source_proof_missing")
	}
	trace, err := m.externalJobTrace(task)
	if err != nil {
		return task.PluginJob.Ticket, err
	}
	p := &permittedTools{manager: m, trace: trace}
	grant := &externalJobAuthority{task: copyTask(task), phase: phase}
	policy, err := p.newExternalPolicy(ctx, task.PluginJob.Ticket.Tool, args, grant)
	if err != nil {
		return task.PluginJob.Ticket, err
	}
	data, verified, checkpoint, err := policy.invokePhase(ctx)
	if err != nil {
		return task.PluginJob.Ticket, err
	}
	var result externalJobWireResult
	if musicStrictJSON(verified, &result) != nil {
		return task.PluginJob.Ticket, errors.New("external_job_checkpoint_invalid")
	}
	if err = policy.jobResultProof(result, data, checkpoint.Body, checkpoint.ContentType); err != nil {
		return task.PluginJob.Ticket, err
	}
	return result.Ticket, nil
}

func (p *externalHTTPPolicy) validateJob(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	var result externalJobWireResult
	if musicStrictJSON(raw, &result) != nil || !p.requested || len(p.observed) == 0 {
		return pluginruntime.VerifiedResult{}, errors.New("external_job_result_unverified")
	}
	observation, _ := json.Marshal(result.Observation)
	if !sameJSON(observation, p.observed) {
		return pluginruntime.VerifiedResult{}, errors.New("external_job_observation_changed")
	}
	data, err := p.readPrivateFile(j, "reply.bin", p.body, 25<<20)
	if err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	if err = p.jobResultProof(result, data, p.body, p.contentType); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	return p.savePhaseResult(j, raw)
}

// The package owns lifecycle parsing. This guard proves every changed ticket
// fact against the original ticket and the actual host response; it never
// accepts a package-selected remote ID, completion, cancellation or output.
func (p *externalHTTPPolicy) jobResultProof(result externalJobWireResult, data []byte, body externalHTTPBlobRef, contentType string) error {
	denied := errors.New("external_job_result_changed")
	old := p.job.task.PluginJob.Ticket
	if result.Ticket.Tool != old.Tool || result.Ticket.Fingerprint != old.Fingerprint || result.Observation.ContentType != contentType {
		return denied
	}
	if p.job.phase == "cancel" {
		if result.KnownRemoteOnly {
			return denied
		}
		observation, _ := json.Marshal(result.Observation)
		if result.CancelAck && sameJSON(observation, p.observed) && reflect.DeepEqual(result.Ticket, old) {
			return nil
		}
		// On checkpoint recovery p.observed is absent; prove the observation
		// through the authenticated body and immutable private metadata instead.
		if !result.CancelAck || !reflect.DeepEqual(result.Ticket, old) {
			return denied
		}
		if result.Observation.BinaryRef != nil {
			if *result.Observation.BinaryRef != body || result.Observation.BodyBase64 != "" {
				return denied
			}
			return nil
		}
		actual, err := base64.StdEncoding.Strict().DecodeString(result.Observation.BodyBase64)
		if err != nil || !bytes.Equal(actual, data) {
			return denied
		}
		return nil
	}
	if result.CancelAck || result.Observation.BinaryRef != nil {
		return denied
	}
	actual, err := base64.StdEncoding.Strict().DecodeString(result.Observation.BodyBase64)
	if err != nil || !bytes.Equal(actual, data) {
		return denied
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return denied
	}
	remote := old.RemoteID
	if p.job.phase == "start" {
		remote = ""
		switch id := externalHTTPValue(value, p.tool.Job.IDPath).(type) {
		case string:
			if validExternalRemoteID(id) {
				remote = id
			}
		case json.Number:
			if strings.Trim(id.String(), "0123456789") == "" && validExternalRemoteID(id.String()) {
				remote = id.String()
			}
		}
	}
	if result.Ticket.RemoteID != remote {
		return denied
	}
	status := externalHTTPText(externalHTTPValue(value, p.tool.Poll.StatusPath))[0]
	state := "waiting"
	if slices.Contains(p.tool.Poll.FailedValues, status) {
		state = "failed"
	} else if slices.Contains(p.tool.Poll.DoneValues, status) {
		state = "completed"
	}
	if result.KnownRemoteOnly {
		if p.job.phase != "start" || state != "completed" && status != "" || result.Ticket.State != "waiting" || !validExternalRemoteID(remote) || result.Ticket.Output.Text != "" || len(result.Ticket.Output.Media) != 0 || status != "" && !externalJobOutputInvalid(value, p.tool) {
			return denied
		}
		return nil
	}
	if result.Ticket.State != state || p.job.phase == "poll" && status == "" || state == "waiting" && !validExternalRemoteID(remote) {
		return denied
	}
	if state != "completed" {
		if result.Ticket.Output.Text != "" || len(result.Ticket.Output.Media) != 0 {
			return denied
		}
		return nil
	}
	selected := p.tool
	if selected.Poll.TextPath != "" {
		selected.TextPath = selected.Poll.TextPath
	}
	if selected.Poll.MediaURLPath != "" {
		selected.MediaURLPath = selected.Poll.MediaURLPath
	}
	proof := &externalHTTPPolicy{tool: selected}
	if !proof.outputProof(plugins.ExternalResponse{ContentType: contentType, BodyBase64: result.Observation.BodyBase64}, result.Ticket.Output) {
		return denied
	}
	return nil
}

// This is a failure witness for retaining an already observed remote ID,
// never a substitute completed output or another creation attempt.
func externalJobOutputInvalid(value any, tool plugins.ExternalProtocolTool) bool {
	if tool.Poll.TextPath != "" {
		tool.TextPath = tool.Poll.TextPath
	}
	if tool.Poll.MediaURLPath != "" {
		tool.MediaURLPath = tool.Poll.MediaURLPath
	}
	text := externalHTTPText(externalHTTPValue(value, tool.TextPath))[0]
	url, ok := externalHTTPValue(value, tool.MediaURLPath).(string)
	if tool.MediaURLPath != "" && (!ok || url == "") {
		return true
	}
	output := plugins.Output{Text: text}
	if url != "" {
		output.Media = []plugins.MediaResult{{Kind: tool.MediaKind, URL: url}}
	}
	encoded, _ := json.Marshal(output)
	return text == "" && url == "" || len(text) > 4096 || len(url) > 2048 || len(encoded) > 16<<10
}

func (p *permittedTools) installedExternalJobTools(ctx context.Context) []mcptools.Tool {
	if _, _, _, err := p.manager.externalPackageCapability(ctx, externalJobCapability); err != nil {
		return nil
	}
	bridge, _, err := p.manager.externalHTTPBridge()
	if err != nil {
		return nil
	}
	out := []mcptools.Tool{}
	if p.runtime == nil {
		return out
	}
	for _, tool := range p.runtime.Tools() {
		if !p.runtime.Background(tool.Name) {
			continue
		}
		protocol, err := bridge.JobProtocol(tool.Name)
		if err == nil {
			out = append(out, mcptools.Tool{Name: protocol.Tool.Name, Description: tool.Description, InputSchema: protocol.Tool.InputSchema})
		}
	}
	return out
}
