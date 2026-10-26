package admin

import (
	"errors"
	"strings"
)

// Only locally generated file/workspace validation errors use this wrapper.
// Arbitrary provider responses and subprocess output must not enter it.
type agentLocalError struct{ message string }

func (e *agentLocalError) Error() string { return e.message }
func (e *agentLocalError) SafeToolFailure() (string, string) {
	return "agent_local_operation_failed", e.message
}
func localAgentError(err error) error {
	if err == nil {
		return nil
	}
	var safe interface{ SafeToolFailure() (string, string) }
	if errors.As(err, &safe) {
		return err
	}
	text := strings.ReplaceAll(strings.ReplaceAll(err.Error(), "\r", " "), "\n", " ")
	if len(text) > 512 {
		text = text[:512]
	}
	return &agentLocalError{text}
}

func toolInputError(message string) error { return &agentLocalError{message} }
