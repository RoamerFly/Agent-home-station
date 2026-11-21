package station

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"wechat-bot/internal/plugins"
)

func (s *localProjectTools) publicExternalTaskDefinition(ctx context.Context, fish, name string) (plugins.ExternalTaskDefinition, error) {
	var empty plugins.ExternalTaskDefinition
	host, err := s.forFish(fish)
	if err != nil {
		return empty, err
	}
	installed, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localExternalPackage})
	if err != nil || !slices.Contains(installed.Permissions, "network") {
		return empty, errors.New("工作站外部任务未启用或缺少联网授权")
	}
	raw, err := s.externalRegistration()
	if err != nil {
		return empty, errors.New("工作站外部任务登记不可读")
	}
	_, bridge, err := externalBridgeRegistration(raw)
	if err != nil {
		return empty, err
	}
	return bridge.Task(name)
}

type publicExternalTaskPhaseResult struct {
	Ticket       plugins.JobTicket
	Initial      plugins.ExternalResponse
	Text         string
	Binary       []byte
	MIME         string
	DeclaredKind string
	MediaPending bool
	CancelAck    bool
}

// A phase is one request using the workstation's current declaration. The
// caller owns the durable phase reservation, source authorization and cadence.
func (s *localProjectTools) publicExternalTaskPhase(ctx context.Context, fish, registration, name, phase string,
	arguments json.RawMessage, ticket plugins.JobTicket, initial plugins.ExternalResponse) (publicExternalTaskPhaseResult, error) {
	var result publicExternalTaskPhaseResult
	host, err := s.forFish(fish)
	if err != nil {
		return result, err
	}
	installed, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localExternalPackage})
	if err != nil || !slices.Contains(installed.Permissions, "network") {
		return result, errors.New("工作站外部任务未启用或缺少联网授权")
	}
	configuration, err := host.store.Configuration(ctx, localExternalPackage)
	if err != nil {
		return result, err
	}
	raw, err := s.externalRegistration()
	if err != nil || externalDigest(raw) != registration {
		return result, errors.New("工作站外部任务登记已变化")
	}
	connections, bridge, err := externalBridgeRegistration(raw)
	if err != nil {
		return result, err
	}
	def, err := bridge.Task(name)
	if err != nil || ticket.Tool != name || ticket.Fingerprint != def.Protocol.Tool.Fingerprint ||
		phase != "start" && phase != "poll" && phase != "query" && phase != "cancel" ||
		def.Protocol.Tool.Job == nil && (phase == "poll" || phase == "cancel") ||
		def.Protocol.Tool.Job != nil && phase == "query" || phase == "cancel" && !def.CanCancel {
		return result, errors.New("外部任务阶段与当前声明不符")
	}
	var reply plugins.ExternalResponse
	if def.Protocol.Tool.Job != nil {
		reply, err = bridge.Execute(ctx, plugins.ExternalRequest{Phase: phase, Tool: name, Arguments: arguments, Ticket: ticket})
	} else {
		reply, err = bridge.ExecutePoll(ctx, plugins.ExternalPollRequest{Phase: phase, Tool: name, Arguments: arguments,
			Fingerprint: ticket.Fingerprint, Initial: initial})
	}
	if err != nil {
		return result, errors.New("外部任务阶段结果未确认，请核对原服务")
	}
	current, currentErr := installedLocalProject(ctx, host, localProjectOperation{pkg: localExternalPackage})
	configNow, configErr := host.store.Configuration(ctx, localExternalPackage)
	rawNow, rawErr := s.externalRegistration()
	if currentErr != nil || configErr != nil || rawErr != nil || current.Revision != installed.Revision ||
		configNow.Revision != configuration.Revision || externalDigest(rawNow) != registration || ctx.Err() != nil {
		return result, errors.New("外部任务期间授权或登记已变化，结果需要人工核对")
	}
	body, err := base64.StdEncoding.Strict().DecodeString(reply.BodyBase64)
	if err != nil {
		return result, errors.New("外部任务回执编码无效")
	}
	observation := plugins.ExternalHTTPObservation{ContentType: reply.ContentType, BodyBase64: reply.BodyBase64}
	var binaryRef plugins.ExternalHTTPBlobRef
	if !plugins.ExternalHTTPIsJSON(body, reply.ContentType) {
		binaryRef = plugins.ExternalHTTPBlobRef{SHA256: externalDigest(body), Size: int64(len(body))}
		observation.BodyBase64 = ""
		observation.BinaryRef = &binaryRef
	}
	tool := def.Protocol.Tool
	if tool.Job != nil && phase == "cancel" {
		wire := plugins.ExternalJobWireResult{Observation: observation, Ticket: ticket, CancelAck: true}
		if err := plugins.VerifyExternalJobResult(tool, phase, ticket, wire, body, binaryRef, reply.ContentType); err != nil {
			return result, errors.New("外部取消回执未通过核验")
		}
		result.Ticket, result.CancelAck = ticket, true
		return result, nil
	}
	if !plugins.ExternalHTTPIsJSON(body, reply.ContentType) {
		if tool.Job != nil || phase != "start" || tool.MediaKind == "" || len(body) == 0 {
			return result, errors.New("外部任务回执缺少受支持的 JSON 状态")
		}
		wire := plugins.ExternalPollWireResult{Observation: observation, Ticket: ticket,
			Output: &plugins.ExternalHTTPWireOutput{Media: []plugins.ExternalHTTPWireMedia{{MediaResult: plugins.MediaResult{Kind: tool.MediaKind}, HostBinaryRef: binaryRef.SHA256}}}}
		wire.Ticket.State = "completed"
		output, err := plugins.VerifyExternalPollResult(tool, phase, ticket, wire, body, binaryRef, reply.ContentType)
		if err != nil || len(output.Media) != 1 {
			return result, errors.New("外部任务文件未通过核验")
		}
		result.Ticket, result.Binary, result.MIME, result.DeclaredKind = wire.Ticket, output.Media[0].Data, output.Media[0].MIMEType, output.Media[0].Kind
		return result, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if !json.Valid(body) || decoder.Decode(&value) != nil {
		return result, errors.New("外部任务 JSON 回执无效")
	}
	status := plugins.ExternalHTTPText(plugins.ExternalHTTPValue(value, tool.Poll.StatusPath))[0]
	state := "waiting"
	if slices.Contains(tool.Poll.FailedValues, status) {
		state = "failed"
	} else if slices.Contains(tool.Poll.DoneValues, status) {
		state = "completed"
	}
	next := ticket
	next.State, next.Output = state, plugins.Output{}
	if tool.Job != nil && phase == "start" {
		switch id := plugins.ExternalHTTPValue(value, tool.Job.IDPath).(type) {
		case string:
			if plugins.ValidExternalRemoteID(id) {
				next.RemoteID = id
			}
		case json.Number:
			if strings.Trim(id.String(), "0123456789") == "" && plugins.ValidExternalRemoteID(id.String()) {
				next.RemoteID = id.String()
			}
		}
	}
	if state == "completed" {
		selected := tool
		if tool.Poll.TextPath != "" {
			selected.TextPath = tool.Poll.TextPath
		}
		if tool.Poll.MediaURLPath != "" {
			selected.MediaURLPath = tool.Poll.MediaURLPath
		}
		if selected.TextPath != "" {
			next.Output.Text = plugins.ExternalHTTPText(plugins.ExternalHTTPValue(value, selected.TextPath))[0]
		}
		if selected.MediaURLPath != "" {
			url, _ := plugins.ExternalHTTPValue(value, selected.MediaURLPath).(string)
			next.Output.Media = []plugins.MediaResult{{Kind: selected.MediaKind, URL: url}}
		}
	}
	if tool.Job != nil {
		wire := plugins.ExternalJobWireResult{Observation: observation, Ticket: next}
		if phase == "start" && status == "" && plugins.ValidExternalRemoteID(next.RemoteID) {
			wire.KnownRemoteOnly, wire.Ticket.State, wire.Ticket.Output = true, "waiting", plugins.Output{}
		}
		if err := plugins.VerifyExternalJobResult(tool, phase, ticket, wire, body, binaryRef, reply.ContentType); err != nil {
			return result, errors.New("外部 Job 回执未通过核验")
		}
		next = wire.Ticket
	} else {
		wire := plugins.ExternalPollWireResult{Observation: observation, Ticket: next}
		wire.Ticket.Output = plugins.Output{}
		if state == "completed" {
			wire.Output = &plugins.ExternalHTTPWireOutput{Text: next.Output.Text}
			for _, media := range next.Output.Media {
				wire.Output.Media = append(wire.Output.Media, plugins.ExternalHTTPWireMedia{MediaResult: media})
			}
		}
		if _, err := plugins.VerifyExternalPollResult(tool, phase, ticket, wire, body, binaryRef, reply.ContentType); err != nil {
			return result, errors.New("外部 Poll 回执未通过核验")
		}
	}
	result.Ticket = next
	result.MediaPending = len(next.Output.Media) != 0
	result.Text = next.Output.Text
	for _, secret := range externalConnectionSecrets(connections, name) {
		result.Text = strings.ReplaceAll(result.Text, secret, "[已隐藏]")
	}
	result.Ticket.Output = plugins.Output{}
	if tool.Job == nil && phase == "start" && state == "waiting" {
		result.Initial = reply
	}
	return result, nil
}
