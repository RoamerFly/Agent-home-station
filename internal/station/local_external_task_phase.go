package station

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/plugins"
	"wechat-bot/internal/strictjson"
	"wechat-bot/internal/workspacefs"
)

type localExternalTaskPhase struct {
	Phase        string                      `json:"phase"`
	Tool         string                      `json:"tool"`
	Arguments    json.RawMessage             `json:"arguments"`
	ArgsDigest   string                      `json:"arguments_sha256"`
	Ticket       plugins.JobTicket           `json:"ticket"`
	TicketDigest string                      `json:"ticket_sha256"`
	Protocol     plugins.ExternalJobProtocol `json:"protocol"`
}

type localExternalInitial struct {
	Body        plugins.ExternalHTTPBlobRef `json:"body"`
	ContentType string                      `json:"content_type"`
}

type localExternalTaskResult struct {
	Ticket    plugins.JobTicket     `json:"ticket"`
	Initial   *localExternalInitial `json:"initial"`
	CancelAck bool                  `json:"cancel_ack"`
}

type localExternalTaskPolicy struct {
	localExternalBinding
	task    *localExternalTask
	request localExternalTaskPhase
	initial *localExternalInitial

	mu       sync.Mutex
	started  bool
	body     []byte
	observed plugins.ExternalHTTPObservation
}

func (t *localExternalTask) phaseKey(phase string, ordinal int) string {
	if phase == "start" {
		return localCheckpointKey("station-external-task", t.origin.EventID, t.origin.Revision, t.def.Protocol.Tool.Name, t.args)
	}
	return localCheckpointKey("station-external-task-phase", t.root.ID, phase, ordinal)
}

func (t *localExternalTask) phase(phase string, ordinal int, ticket plugins.JobTicket, initial *localExternalInitial) *localExternalTaskPolicy {
	binding := t.env.binding
	binding.source = t.origin
	binding.capability = localExternalJobCap
	if t.def.Protocol.Tool.Job == nil {
		binding.capability = localExternalPollCap
	}
	ticketData, _ := json.Marshal(ticket)
	request := localExternalTaskPhase{Phase: phase, Tool: ticket.Tool, Arguments: t.args, ArgsDigest: externalDigest(t.args),
		Ticket: ticket, TicketDigest: externalDigest(ticketData), Protocol: t.def.Protocol}
	binding.arguments, _ = json.Marshal(request)
	binding.invocation = pluginhost.Invocation{Account: t.env.in.Account, Thread: t.env.in.Thread, Requester: t.env.in.Sender,
		Purpose: localExternalTaskPurpose, RequestKey: t.phaseKey(phase, ordinal),
		Authorization:  localCheckpointKey(binding.grant.projectJobAuthorization(), binding.registration, binding.installed.Revision),
		SourceVersions: map[string]int{t.origin.EventID: t.origin.Revision}}
	binding.extraCheck = func(ctx context.Context) error {
		current, err := binding.tools.store.LocalRequestSource(ctx, t.env.in, t.trace)
		if err != nil || *current != *t.origin {
			return errLocalExternalTaskAuthority
		}
		return nil
	}
	return &localExternalTaskPolicy{localExternalBinding: binding, task: t, request: request, initial: initial}
}

func (t *localExternalTask) invokePhase(ctx context.Context, phase string, ordinal int, ticket plugins.JobTicket, initial *localExternalInitial) (pluginhost.Job, localExternalTaskResult, error) {
	policy := t.phase(phase, ordinal, ticket, initial)
	// Only the owner's active turn and the admitted continuation worker can
	// reach this method. Both reserve the same durable ordinal before dispatch.
	result, err := policy.invokeJob(ctx, policy, localExternalTaskCall, ticket.Tool, true, false)
	if result.Job.ID == "" {
		return result.Job, localExternalTaskResult{}, err
	}
	if err != nil {
		job, readErr := t.lookup(ctx, phase, ordinal)
		if readErr != nil {
			return job, localExternalTaskResult{}, err
		}
		result.Job = job
	}
	saved, _, err := policy.saved(ctx, result.Job)
	return result.Job, saved, err
}

func (p *localExternalTaskPolicy) checkStored(ctx context.Context, job pluginhost.Job) error {
	if job.ID == "" || job.Version != p.installed.Package.Version ||
		p.Check(ctx, job, pluginhost.Capability{ID: p.capability}) != nil {
		return errLocalExternalTaskAuthority
	}
	return nil
}

func (p *localExternalTaskPolicy) saved(ctx context.Context, job pluginhost.Job) (localExternalTaskResult, json.RawMessage, error) {
	var state localExternalTaskResult
	if err := p.checkStored(ctx, job); err != nil || job.State != "completed" {
		return state, nil, err
	}
	data, err := p.host.store.PrivateJobResult(ctx, job, p.checkStored)
	if err != nil {
		return state, nil, err
	}
	if json.Unmarshal(data, &state) != nil || state.Ticket.Tool != p.request.Tool || state.Ticket.Fingerprint != p.request.Ticket.Fingerprint {
		return state, nil, errors.New("外部任务的已核验结果不可读")
	}
	return state, data, nil
}

func (p *localExternalTaskPolicy) Service(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	phase := p.request.Phase
	if p.started || method != "wechat/external_request" || !strictjson.Matches(raw, map[string]string{
		"phase": phase, "tool": p.request.Tool, "arguments_sha256": p.request.ArgsDigest, "ticket_sha256": p.request.TicketDigest,
	}) || p.checkStored(ctx, job) != nil || cap.ID != p.capability {
		return nil, errors.New("外部任务阶段未获授权或已提交")
	}
	p.started = true
	bound, cancel := p.watchedContext(ctx, job, cap)
	defer cancel()
	bridge := p.task.env.bridge
	var reply plugins.ExternalResponse
	var err error
	if p.capability == localExternalJobCap {
		reply, err = bridge.Execute(bound, plugins.ExternalRequest{Phase: phase, Tool: p.request.Tool,
			Arguments: p.request.Arguments, Ticket: p.request.Ticket})
	} else {
		request := plugins.ExternalPollRequest{Phase: phase, Tool: p.request.Tool, Arguments: p.request.Arguments, Fingerprint: p.request.Ticket.Fingerprint}
		if phase == "query" {
			request.Initial, err = p.readInitial(p.task.root.ID)
			if err != nil {
				return nil, err
			}
		}
		reply, err = bridge.ExecutePoll(bound, request)
	}
	if err != nil {
		return nil, errors.New("外部任务阶段结果未确认，请核对原服务")
	}
	if err := p.Check(bound, job, cap); err != nil {
		return nil, err
	}
	body, err := base64.StdEncoding.Strict().DecodeString(reply.BodyBase64)
	if err != nil {
		return nil, err
	}
	p.body, p.observed.ContentType = body, reply.ContentType
	if plugins.ExternalHTTPIsJSON(body, reply.ContentType) {
		p.observed.BodyBase64 = reply.BodyBase64
	} else {
		p.observed.BinaryRef = &plugins.ExternalHTTPBlobRef{SHA256: externalDigest(body), Size: int64(len(body))}
	}
	return json.Marshal(p.observed)
}

func (p *localExternalTaskPolicy) Validate(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var invalid pluginruntime.VerifiedResult
	if !p.started || p.Check(ctx, job, cap) != nil {
		return invalid, errLocalExternalTaskAuthority
	}
	var ref plugins.ExternalHTTPBlobRef
	if p.observed.BinaryRef != nil {
		ref = *p.observed.BinaryRef
	}
	tool, phase, old := p.request.Protocol.Tool, p.request.Phase, p.request.Ticket
	var output plugins.Output
	metadata := map[string]any{}
	var ticket plugins.JobTicket
	if p.capability == localExternalJobCap {
		var result plugins.ExternalJobWireResult
		if strictjson.DecodeKnownFields(raw, &result) != nil || !reflect.DeepEqual(result.Observation, p.observed) {
			return invalid, errors.New("外部 Job 缺少真实回执")
		}
		if err := plugins.VerifyExternalJobResult(tool, phase, old, result, p.body, ref, p.observed.ContentType); err != nil {
			return invalid, err
		}
		ticket, output = result.Ticket, result.Ticket.Output
		if result.CancelAck {
			metadata["cancel_ack"] = true
		}
	} else {
		var result plugins.ExternalPollWireResult
		if strictjson.DecodeKnownFields(raw, &result) != nil || !reflect.DeepEqual(result.Observation, p.observed) {
			return invalid, errors.New("外部 Poll 缺少真实回执")
		}
		var err error
		output, err = plugins.VerifyExternalPollResult(tool, phase, old, result, p.body, ref, p.observed.ContentType)
		if err != nil {
			return invalid, err
		}
		ticket = result.Ticket
		if phase == "start" && ticket.State == "waiting" {
			if err := p.saveInitial(job.ID); err != nil {
				return invalid, err
			}
			metadata["initial"] = localExternalInitial{
				Body: plugins.ExternalHTTPBlobRef{SHA256: externalDigest(p.body), Size: int64(len(p.body))}, ContentType: p.observed.ContentType,
			}
		}
	}
	ticket.Output = plugins.Output{}
	metadata["ticket"] = ticket
	return p.saveOutput(ctx, job, cap, tool.Name, output, externalConnectionSecrets(p.task.env.connections, tool.Name), metadata)
}

const maxLocalExternalInitial = 2 << 20

func (p *localExternalTaskPolicy) initialRoot() (*os.Root, error) {
	return workspacefs.EnsurePath(filepath.Join(stationAuthorityDir(p.tools.projectTools.home.dataDir), "external-poll-initial"))
}

func (p *localExternalTaskPolicy) saveInitial(id string) error {
	if !validLocalRVCHex(id, 64) || len(p.body) > maxLocalExternalInitial || !json.Valid(p.body) {
		return errors.New("外部 Poll 首次回执无效")
	}
	root, err := p.initialRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.OpenFile(id+".json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(p.body)
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}

func (p *localExternalTaskPolicy) readInitial(id string) (plugins.ExternalResponse, error) {
	var empty plugins.ExternalResponse
	if !validLocalRVCHex(id, 64) || p.initial == nil || p.initial.Body.Size <= 0 || p.initial.Body.Size > maxLocalExternalInitial {
		return empty, errors.New("外部 Poll 首次回执不可核验")
	}
	root, err := p.initialRoot()
	if err != nil {
		return empty, err
	}
	defer root.Close()
	file, err := workspacefs.OpenRegular(root, id+".json")
	if err != nil {
		return empty, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, p.initial.Body.Size+1))
	if err != nil || int64(len(body)) != p.initial.Body.Size || externalDigest(body) != p.initial.Body.SHA256 {
		return empty, errors.New("外部 Poll 首次回执已变化")
	}
	return plugins.ExternalResponse{ContentType: p.initial.ContentType, BodyBase64: base64.StdEncoding.EncodeToString(body)}, nil
}
