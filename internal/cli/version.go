package cli

import "fmt"

var (
	appVersion = "dev"
	appCommit  = "none"
	appDate    = "unknown"
)

// SetVersionInfo sets the version info from build-time ldflags.
func SetVersionInfo(version, commit, date string) {
	appVersion = version
	appCommit = commit
	appDate = date
}

// VersionString returns a formatted version string.
func VersionString() string {
	short := appCommit
	if len(short) > 7 {
		short = short[:7]
	}
	return fmt.Sprintf("openmarmut v%s (%s, %s)", appVersion, short, appDate)
}
