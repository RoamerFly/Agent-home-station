package admin

// Legacy polling without a provider job ID is a durable host operation. Query
// templates always use the original creation response, never a later speaker
// or a replacement response returned by a polling endpoint.
import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/plugins"
)

const externalPollCapability = "external.poll_request"

type externalPollInitial struct {
	JobID       string              `json:"job_id"`
	Body        externalHTTPBlobRef `json:"body"`
	ContentType string              `json:"content_type"`
	Binding     string              `json:"binding"`
}
type externalPollState struct {
	Arguments json.RawMessage      `json:"arguments"`
	Initial   *externalPollInitial `json:"initial,omitempty"`
}
type externalPollWireResult struct {
	Observation externalHTTPObservation `json:"observation"`
	Ticket      plugins.JobTicket       `json:"ticket"`
	Output      *externalHTTPWireOutput `json:"output,omitempty"`
}

func externalTaskCapability(task agentTask) string {
	if task.PluginJob != nil && task.PluginJob.PollState != nil {
		return externalPollCapability
	}
	return externalJobCapability
}
func (p *permittedTools) externalPollOnly(name string) bool {
	for _, entry := range p.manager.plugins.installed() {
		if !entry.Enabled {
			continue
		}
		for _, tool := range entry.Manifest.Tools {
			if entry.Manifest.ID+"__"+tool.Name == name {
				return tool.Job == nil && tool.Poll != nil
			}
		}
	}
	return false
}
func (p *permittedTools) installedExternalPollTools(ctx context.Context) []mcptools.Tool {
	if _, _, _, err := p.manager.externalPackageCapability(ctx, externalPollCapability); err != nil {
		return nil
	}
	bridge, _, err := p.manager.externalHTTPBridge()
	if err != nil || p.runtime == nil {
		return nil
	}
	tools := []mcptools.Tool{}
	for _, tool := range p.runtime.Tools() {
		if !p.externalPollOnly(tool.Name) {
			continue
		}
		protocol, err := bridge.PollProtocol(tool.Name)
		if err != nil {
			continue
		}
		tools = append(tools, mcptools.Tool{Name: tool.Name, Description: tool.Description + " Creates one durable local operation and returns task_id immediately. Subsequent status queries reuse its original creation response; cancellation stops local follow-up only.", InputSchema: protocol.Tool.InputSchema})
	}
	return tools
}
func (p *externalHTTPPolicy) executePoll(ctx context.Context, in plugins.ExternalRequest) (plugins.ExternalResponse, error) {
	request := plugins.ExternalPollRequest{Phase: in.Phase, Tool: in.Tool, Arguments: in.Arguments, Fingerprint: p.tool.Fingerprint}
	if in.Phase == "query" {
		initial, err := p.pollInitialResponse(ctx)
		if err != nil {
			return plugins.ExternalResponse{}, err
		}
		request.Initial = initial
	}
	return p.bridge.ExecutePoll(ctx, request)
}
func (p *externalHTTPPolicy) pollInitialResponse(ctx context.Context) (plugins.ExternalResponse, error) {
	if p.job == nil || !p.job.poll || p.job.task.PluginJob.PollState == nil || p.job.task.PluginJob.PollState.Initial == nil {
		return plugins.ExternalResponse{}, errors.New("external_poll_initial_missing")
	}
	ref := p.job.task.PluginJob.PollState.Initial
	j, err := p.store.Job(ctx, ref.JobID)
	if err != nil {
		return plugins.ExternalResponse{}, err
	}
	private, err := p.store.PrivateJobResult(ctx, j, func(ctx context.Context, old pluginhost.Job) error {
		if old.Plugin != externalHTTPPluginID || old.Tool != externalPollCapability || old.State != "completed" || old.Context.Account != p.invocation.Account || old.Context.Thread != p.invocation.Thread || old.Context.Requester != p.invocation.Requester || old.Context.Authorization != p.invocation.Authorization || old.Context.EvidenceEpoch != p.invocation.EvidenceEpoch || !reflect.DeepEqual(old.Context.SourceVersions, p.invocation.SourceVersions) || old.PackageDigest != p.installed.Digest || old.Version != p.installed.Package.Version || old.ConfigRevision != strconv.Itoa(p.configRevision) {
			return errors.New("external_poll_initial_scope_changed")
		}
		var args externalHTTPArguments
		if json.Unmarshal(old.Arguments, &args) != nil || args.Phase != "start" || args.Tool != p.request.Tool || args.ArgumentsSHA256 != p.request.ArgumentsSHA256 || !bytes.Equal(args.Arguments, p.request.Arguments) || old.Context.RequestKey != "external-job-"+agentID(p.job.task.Account, p.job.task.Thread, p.job.task.ID, "start", "0") {
			return errors.New("external_poll_initial_request_changed")
		}
		return nil
	})
	if err != nil {
		return plugins.ExternalResponse{}, err
	}
	var checkpoint externalHTTPCheckpoint
	if musicStrictJSON(private, &checkpoint) != nil || checkpoint.JobID != ref.JobID || checkpoint.Binding != ref.Binding || checkpoint.Body != ref.Body || checkpoint.ContentType != ref.ContentType {
		return plugins.ExternalResponse{}, errors.New("external_poll_initial_checkpoint_changed")
	}
	data, err := p.readPrivateFile(j, "reply.bin", ref.Body, 25<<20)
	if err != nil {
		return plugins.ExternalResponse{}, err
	}
	return plugins.ExternalResponse{ContentType: ref.ContentType, BodyBase64: base64.StdEncoding.EncodeToString(data)}, nil
}
func (p *externalHTTPPolicy) pollResultProof(result externalPollWireResult, data []byte, body externalHTTPBlobRef, contentType string) (plugins.Output, error) {
	denied := errors.New("external_poll_result_changed")
	old := p.job.task.PluginJob.Ticket
	if result.Ticket.Tool != old.Tool || result.Ticket.Fingerprint != old.Fingerprint || result.Ticket.RemoteID != "" || result.Observation.ContentType != contentType || result.Ticket.Output.Text != "" || len(result.Ticket.Output.Media) != 0 {
		return plugins.Output{}, denied
	}
	if !externalHTTPIsJSON(data, contentType) {
		if p.job.phase != "start" || result.Ticket.State != "completed" || result.Output == nil {
			return plugins.Output{}, denied
		}
		return p.materialize(externalHTTPWireResult{Observation: result.Observation, Output: *result.Output}, data, body, contentType)
	}
	if result.Observation.BinaryRef != nil {
		return plugins.Output{}, denied
	}
	actual, err := base64.StdEncoding.Strict().DecodeString(result.Observation.BodyBase64)
	if err != nil || !bytes.Equal(actual, data) {
		return plugins.Output{}, denied
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return plugins.Output{}, denied
	}
	status := externalHTTPText(externalHTTPValue(value, p.tool.Poll.StatusPath))[0]
	state := "waiting"
	if slices.Contains(p.tool.Poll.FailedValues, status) {
		state = "failed"
	} else if slices.Contains(p.tool.Poll.DoneValues, status) {
		state = "completed"
	}
	if result.Ticket.State != state {
		return plugins.Output{}, denied
	}
	if state != "completed" {
		if result.Output != nil {
			return plugins.Output{}, denied
		}
		return plugins.Output{}, nil
	}
	if result.Output == nil {
		return plugins.Output{}, denied
	}
	selected := p.tool
	if selected.Poll.TextPath != "" {
		selected.TextPath = selected.Poll.TextPath
	}
	if selected.Poll.MediaURLPath != "" {
		selected.MediaURLPath = selected.Poll.MediaURLPath
	}
	proof := &externalHTTPPolicy{tool: selected}
	return proof.materialize(externalHTTPWireResult{Observation: result.Observation, Output: *result.Output}, data, body, contentType)
}
func (p *externalHTTPPolicy) validatePoll(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	var result externalPollWireResult
	if musicStrictJSON(raw, &result) != nil || !p.requested || len(p.observed) == 0 {
		return pluginruntime.VerifiedResult{}, errors.New("external_poll_result_unverified")
	}
	observed, _ := json.Marshal(result.Observation)
	if !sameJSON(observed, p.observed) {
		return pluginruntime.VerifiedResult{}, errors.New("external_poll_observation_changed")
	}
	data, err := p.readPrivateFile(j, "reply.bin", p.body, 25<<20)
	if err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	if _, err = p.pollResultProof(result, data, p.body, p.contentType); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	return p.savePhaseResult(j, raw)
}
func (m *Manager) callExternalPollPhase(ctx context.Context, task agentTask, phase string) (plugins.JobTicket, *externalPollInitial, error) {
	old := task.PluginJob.Ticket
	if task.PluginJob.PollState == nil || task.PluginJob.Origin == nil || task.PluginJob.LegacyConfigurationSHA256 == "" {
		return old, nil, errors.New("external_poll_original_proof_missing")
	}
	trace, err := m.externalJobTrace(task)
	if err != nil {
		return old, nil, err
	}
	p := &permittedTools{manager: m, trace: trace}
	policy, err := p.newExternalPolicy(ctx, old.Tool, task.PluginJob.PollState.Arguments, &externalJobAuthority{task: copyTask(task), phase: phase, poll: true})
	if err != nil {
		return old, nil, err
	}
	data, verified, checkpoint, err := policy.invokePhase(ctx)
	if err != nil {
		return old, nil, err
	}
	var result externalPollWireResult
	if musicStrictJSON(verified, &result) != nil {
		return old, nil, errors.New("external_poll_checkpoint_invalid")
	}
	output, proofErr := policy.pollResultProof(result, data, checkpoint.Body, checkpoint.ContentType)
	if proofErr != nil {
		return old, nil, proofErr
	}
	result.Ticket.Output = output
	var initial *externalPollInitial
	if phase == "start" {
		initial = &externalPollInitial{JobID: checkpoint.JobID, Body: checkpoint.Body, ContentType: checkpoint.ContentType, Binding: checkpoint.Binding}
	}
	return result.Ticket, initial, nil
}
func (m *Manager) externalPollCachedOutput(ctx context.Context, task agentTask) (plugins.Output, *externalHTTPPolicy, error) {
	trace, err := m.externalJobTrace(task)
	if err != nil {
		return plugins.Output{}, nil, err
	}
	p := &permittedTools{manager: m, trace: trace}
	policy, err := p.newExternalPolicy(ctx, task.PluginJob.Ticket.Tool, task.PluginJob.PollState.Arguments, &externalJobAuthority{task: copyTask(task), phase: "read", poll: true, cached: true})
	if err != nil {
		return plugins.Output{}, nil, err
	}
	if err = policy.Check(ctx, pluginhost.Job{Plugin: externalHTTPPluginID, Tool: externalPollCapability, Context: policy.invocation, Arguments: policy.arguments}, pluginhost.Capability{}); err != nil {
		return plugins.Output{}, nil, err
	}
	output := task.PluginJob.Ticket.Output
	binary := false
	for _, media := range output.Media {
		if media.URL == "" && media.Base64Data == "" && len(media.Data) == 0 {
			binary = true
		}
	}
	initial, err := policy.pollInitialResponse(ctx)
	if err != nil {
		return plugins.Output{}, nil, err
	}
	if !binary {
		return output, policy, nil
	}
	ref := task.PluginJob.PollState.Initial
	j, err := policy.store.Job(ctx, ref.JobID)
	if err != nil {
		return plugins.Output{}, nil, err
	}
	private, err := policy.store.PrivateJobResult(ctx, j, func(check context.Context, _ pluginhost.Job) error {
		return policy.Check(check, pluginhost.Job{Plugin: externalHTTPPluginID, Tool: externalPollCapability, Context: policy.invocation, Arguments: policy.arguments}, pluginhost.Capability{})
	})
	if err != nil {
		return plugins.Output{}, nil, err
	}
	var checkpoint externalHTTPCheckpoint
	if musicStrictJSON(private, &checkpoint) != nil || checkpoint.Body != ref.Body || checkpoint.Binding != ref.Binding || checkpoint.JobID != ref.JobID || checkpoint.ContentType != ref.ContentType {
		return plugins.Output{}, nil, errors.New("external_poll_initial_checkpoint_invalid")
	}
	verified, err := policy.readPrivateFile(j, "verified.json", checkpoint.Verified, 8<<20)
	if err != nil {
		return plugins.Output{}, nil, err
	}
	var result externalPollWireResult
	if musicStrictJSON(verified, &result) != nil {
		return plugins.Output{}, nil, errors.New("external_poll_initial_result_invalid")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(initial.BodyBase64)
	if err != nil {
		return plugins.Output{}, nil, err
	}
	proof := &externalHTTPPolicy{tool: policy.tool, job: &externalJobAuthority{task: task, phase: "start", poll: true}}
	output, err = proof.pollResultProof(result, data, ref.Body, ref.ContentType)
	return output, policy, err
}
