package cli

import (
	"fmt"
	"strings"

	"github.com/marmutapp/openmarmut/internal/ui"
)

// styledError formats an error with FormatError and appends a hint if applicable.
func styledError(err error) string {
	msg := ui.FormatError(err.Error())
	if hint := errorHint(err); hint != "" {
		msg += "\n" + ui.FormatHint(hint)
	}
	return msg
}

// errorHint returns a hint string for common error patterns, or "" if none.
func errorHint(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()

	switch {
	case strings.Contains(s, "no providers configured"):
		return "Add a providers section to .openmarmut.yaml"
	case strings.Contains(s, "not found") && strings.Contains(s, "provider"):
		return "Run 'openmarmut providers' to list configured providers"
	case strings.Contains(s, "no active provider"):
		return "Set active_provider in .openmarmut.yaml or use --provider flag"
	case strings.Contains(s, "docker") && (strings.Contains(s, "Cannot connect") || strings.Contains(s, "connection refused")):
		return "Start Docker with 'sudo service docker start' or 'docker desktop'"
	case strings.Contains(s, "ErrAuthFailed") || (strings.Contains(s, "environment variable") && strings.Contains(s, "not set")):
		return fmt.Sprintf("Set the required environment variable or add api_key to your provider config")
	case strings.Contains(s, "path escapes target"):
		return "Paths must be relative and within the target directory"
	}

	return ""
}
