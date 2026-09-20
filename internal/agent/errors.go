package agent

import (
	"errors"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// isContextTooLarge detects Anthropic's "prompt is too long" error and
// OpenAI's "context_length_exceeded" error. Sorus surfaces the underlying SDK
// error verbatim, so we sniff across provider shapes here.
//
// Heuristic; tighten if edge cases surface.
func isContextTooLarge(err error) bool {
	if err == nil {
		return false
	}

	// Anthropic SDK: anthropic.Error with StatusCode 400 and the message
	// contains "prompt is too long". Type is "invalid_request_error".
	var aerr *anthropic.Error
	if errors.As(err, &aerr) {
		if aerr.StatusCode == 400 && strings.Contains(strings.ToLower(aerr.Error()), "prompt is too long") {
			return true
		}
	}

	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "context_length_exceeded") {
		return true
	}
	if strings.Contains(msg, "prompt is too long") {
		return true
	}
	if strings.Contains(msg, "input is too long") {
		return true
	}
	return false
}
