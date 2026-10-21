// SPDX-License-Identifier: Apache-2.0

// Package update checks GitHub for newer OpenTidemark releases (SPEC §17.7): a daily background
// check, an on-demand check for admins and the status shown in the UI. It never sends install
// data; a development build makes no request at all.
package update

import "time"

// Distribution stamps set at build time with -X main.distribution (SPEC §17.6).
const (
	DistributionArchive   = "archive"
	DistributionContainer = "container"
)

// Build describes the running binary.
type Build struct {
	Version        string // -X main.version
	Distribution   string // -X main.distribution: "archive", "container" or empty
	WindowsService bool   // running under the Windows Service Control Manager
}

// Development reports whether b is a development build: not stamped by `make dist`,
// `.\build.ps1 dist` or the Dockerfile, or its version is not a release version.
func (b Build) Development() bool {
	if b.Distribution != DistributionArchive && b.Distribution != DistributionContainer {
		return true
	}
	_, err := ParseVersion(b.Version)
	return err != nil
}

// InstallKind is how the running instance was installed.
type InstallKind string

// Install kinds (SPEC §17.7).
const (
	KindSystemd        InstallKind = "systemd"
	KindWindowsService InstallKind = "windows-service"
	KindContainer      InstallKind = "container"
	KindManual         InstallKind = "manual"
	KindDev            InstallKind = "dev"
)

// Reasons why one-click update is not available (Apply.Reason, and details.reason of a 409).
const (
	ReasonDisabled            = "disabled"
	ReasonDev                 = "dev"
	ReasonContainer           = "container"
	ReasonHA                  = "ha"
	ReasonNotAService         = "not-a-service"
	ReasonHelperMissing       = "helper-missing"
	ReasonUnsupportedPlatform = "unsupported-platform"
)

// Error codes of a failed check (Status.Error).
const (
	CodeUnreachable     = "unreachable"
	CodeRateLimited     = "rate_limited"
	CodeInvalidResponse = "invalid_response"
	CodeNotFound        = "not_found"
)

// StateIdle is the only state while no update is being installed.
const StateIdle = "idle"

// releasePlatforms are the platforms release archives exist for (SPEC §17.6).
var releasePlatforms = map[string]bool{"linux/amd64": true, "linux/arm64": true, "windows/amd64": true}

// Apply says whether the update can be installed from the UI.
type Apply struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
}

// Release is a release newer than the running version.
type Release struct {
	Version     string    `json:"version"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"publishedAt"`
	NotesURL    string    `json:"notesUrl"`
}

// Status is the update status of this node (GET /api/v1/system/update).
type Status struct {
	Enabled     bool        `json:"enabled"`
	Current     string      `json:"current"`
	NodeID      string      `json:"nodeId"`
	InstallKind InstallKind `json:"installKind"`
	Apply       Apply       `json:"apply"`
	Latest      *Release    `json:"latest,omitempty"`
	Available   bool        `json:"available"`
	CheckedAt   *time.Time  `json:"checkedAt,omitempty"`
	Error       string      `json:"error,omitempty"`
	State       string      `json:"state"`
	Progress    *Progress   `json:"progress,omitempty"`
	LastResult  *Result     `json:"lastResult,omitempty"`
}

// Install describes the installation for the apply decision.
type Install struct {
	Enabled  bool        // TIDEMARK_UPDATE_CHECK
	Kind     InstallKind // DetectInstallKind
	HA       bool        // shared PostgreSQL metadata (SPEC §17.5)
	Platform string      // GOOS/GOARCH
	Helper   bool        // HelperInstalled
}

// Apply decides whether one-click update is possible: the first reason that applies, else
// supported.
func (in Install) Apply() Apply {
	var reason string
	switch {
	case !in.Enabled:
		reason = ReasonDisabled
	case in.Kind == KindDev:
		reason = ReasonDev
	case in.Kind == KindContainer:
		reason = ReasonContainer
	case in.HA:
		reason = ReasonHA
	case !releasePlatforms[in.Platform]:
		reason = ReasonUnsupportedPlatform
	case in.Kind == KindManual:
		reason = ReasonNotAService
	case !in.Helper:
		reason = ReasonHelperMissing
	default:
		return Apply{Supported: true}
	}
	return Apply{Supported: false, Reason: reason}
}
