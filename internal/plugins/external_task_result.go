package plugins

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
)

func ValidExternalRemoteID(id string) bool {
	return id != "." && id != ".." && jobIDPattern.MatchString(id)
}

type ExternalJobWireResult struct {
	Observation     ExternalHTTPObservation `json:"observation"`
	Ticket          JobTicket               `json:"ticket"`
	CancelAck       bool                    `json:"cancel_ack,omitempty"`
	KnownRemoteOnly bool                    `json:"known_remote_only,omitempty"`
}

type ExternalPollWireResult struct {
	Observation ExternalHTTPObservation `json:"observation"`
	Ticket      JobTicket               `json:"ticket"`
	Output      *ExternalHTTPWireOutput `json:"output,omitempty"`
}

// The package owns lifecycle parsing. This guard proves every changed ticket
// fact against the original ticket and the actual host response; it never
// accepts a package-selected remote ID, completion, cancellation or output.
func VerifyExternalJobResult(tool ExternalProtocolTool, phase string, old JobTicket, result ExternalJobWireResult, data []byte, body ExternalHTTPBlobRef, contentType string) error {
	denied := errors.New("external_job_result_changed")
	if tool.Job == nil || tool.Poll == nil || (phase != "start" && phase != "poll" && phase != "cancel") ||
		result.Ticket.Tool != old.Tool || result.Ticket.Fingerprint != old.Fingerprint || result.Observation.ContentType != contentType {
		return denied
	}
	if phase == "cancel" {
		if result.KnownRemoteOnly {
			return denied
		}
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
	if !json.Valid(data) || decoder.Decode(&value) != nil {
		return denied
	}
	remote := old.RemoteID
	if phase == "start" {
		remote = ""
		switch id := ExternalHTTPValue(value, tool.Job.IDPath).(type) {
		case string:
			if ValidExternalRemoteID(id) {
				remote = id
			}
		case json.Number:
			if strings.Trim(id.String(), "0123456789") == "" && ValidExternalRemoteID(id.String()) {
				remote = id.String()
			}
		}
	}
	if result.Ticket.RemoteID != remote {
		return denied
	}
	status := ExternalHTTPText(ExternalHTTPValue(value, tool.Poll.StatusPath))[0]
	state := "waiting"
	if slices.Contains(tool.Poll.FailedValues, status) {
		state = "failed"
	} else if slices.Contains(tool.Poll.DoneValues, status) {
		state = "completed"
	}
	if result.KnownRemoteOnly {
		if phase != "start" || state != "completed" && status != "" || result.Ticket.State != "waiting" || !ValidExternalRemoteID(remote) || result.Ticket.Output.Text != "" || len(result.Ticket.Output.Media) != 0 || status != "" && !externalJobOutputInvalid(value, tool) {
			return denied
		}
		return nil
	}
	if result.Ticket.State != state || phase == "poll" && status == "" || state == "waiting" && !ValidExternalRemoteID(remote) {
		return denied
	}
	if state != "completed" {
		if result.Ticket.Output.Text != "" || len(result.Ticket.Output.Media) != 0 {
			return denied
		}
		return nil
	}
	selected := tool
	if selected.Poll.TextPath != "" {
		selected.TextPath = selected.Poll.TextPath
	}
	if selected.Poll.MediaURLPath != "" {
		selected.MediaURLPath = selected.Poll.MediaURLPath
	}
	if !VerifyExternalHTTPOutput(selected, ExternalResponse{ContentType: contentType, BodyBase64: result.Observation.BodyBase64}, result.Ticket.Output) {
		return denied
	}
	return nil
}

// This is a failure witness for retaining an already observed remote ID,
// never a substitute completed output or another creation attempt.
func externalJobOutputInvalid(value any, tool ExternalProtocolTool) bool {
	if tool.Poll.TextPath != "" {
		tool.TextPath = tool.Poll.TextPath
	}
	if tool.Poll.MediaURLPath != "" {
		tool.MediaURLPath = tool.Poll.MediaURLPath
	}
	text := ExternalHTTPText(ExternalHTTPValue(value, tool.TextPath))[0]
	url, ok := ExternalHTTPValue(value, tool.MediaURLPath).(string)
	if tool.MediaURLPath != "" && (!ok || url == "") {
		return true
	}
	output := Output{Text: text}
	if url != "" {
		output.Media = []MediaResult{{Kind: tool.MediaKind, URL: url}}
	}
	encoded, _ := json.Marshal(output)
	return text == "" && url == "" || len(text) > 4096 || len(url) > 2048 || len(encoded) > 16<<10
}

func VerifyExternalPollResult(tool ExternalProtocolTool, phase string, old JobTicket, result ExternalPollWireResult, data []byte, body ExternalHTTPBlobRef, contentType string) (Output, error) {
	denied := errors.New("external_poll_result_changed")
	if tool.Job != nil || tool.Poll == nil || (phase != "start" && phase != "query") ||
		result.Ticket.Tool != old.Tool || result.Ticket.Fingerprint != old.Fingerprint || result.Ticket.RemoteID != "" || result.Observation.ContentType != contentType || result.Ticket.Output.Text != "" || len(result.Ticket.Output.Media) != 0 {
		return Output{}, denied
	}
	if !ExternalHTTPIsJSON(data, contentType) {
		if phase != "start" || result.Ticket.State != "completed" || result.Output == nil {
			return Output{}, denied
		}
		return MaterializeExternalHTTPOutput(tool, ExternalHTTPWireResult{Observation: result.Observation, Output: *result.Output}, data, body, contentType)
	}
	if result.Observation.BinaryRef != nil {
		return Output{}, denied
	}
	actual, err := base64.StdEncoding.Strict().DecodeString(result.Observation.BodyBase64)
	if err != nil || !bytes.Equal(actual, data) {
		return Output{}, denied
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if !json.Valid(data) || decoder.Decode(&value) != nil {
		return Output{}, denied
	}
	status := ExternalHTTPText(ExternalHTTPValue(value, tool.Poll.StatusPath))[0]
	state := "waiting"
	if slices.Contains(tool.Poll.FailedValues, status) {
		state = "failed"
	} else if slices.Contains(tool.Poll.DoneValues, status) {
		state = "completed"
	}
	if result.Ticket.State != state {
		return Output{}, denied
	}
	if state != "completed" {
		if result.Output != nil {
			return Output{}, denied
		}
		return Output{}, nil
	}
	if result.Output == nil {
		return Output{}, denied
	}
	selected := tool
	if selected.Poll.TextPath != "" {
		selected.TextPath = selected.Poll.TextPath
	}
	if selected.Poll.MediaURLPath != "" {
		selected.MediaURLPath = selected.Poll.MediaURLPath
	}
	return MaterializeExternalHTTPOutput(selected, ExternalHTTPWireResult{Observation: result.Observation, Output: *result.Output}, data, body, contentType)
}
