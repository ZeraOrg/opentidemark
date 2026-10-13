// SPDX-License-Identifier: Apache-2.0

// Package model holds the normalized telemetry model shared by every protocol (SPEC §8.1).
// It has no dependencies on other Tidemark packages.
package model

import (
	"strconv"
	"strings"
)

// Level is the ordinal severity of an event; it is used for range comparisons.
type Level uint8

// Levels in ascending order of severity.
const (
	Verbose Level = iota
	Debug
	Information
	Warning
	Error
	Fatal
)

// levelNames are the canonical (Serilog/Seq) names, indexed by Level.
var levelNames = [...]string{"Verbose", "Debug", "Information", "Warning", "Error", "Fatal"}

// AllLevels lists every level in ascending order.
func AllLevels() []Level { return []Level{Verbose, Debug, Information, Warning, Error, Fatal} }

// String returns the canonical name of the level ("Information", …).
func (l Level) String() string {
	if int(l) < len(levelNames) {
		return levelNames[l]
	}
	return levelNames[Information]
}

// Valid reports whether l is one of the six defined levels.
func (l Level) Valid() bool { return l <= Fatal }

// levelAliases maps lower-case textual inputs to levels (SPEC §8.1 table).
var levelAliases = map[string]Level{
	"verbose": Verbose, "trace": Verbose, "trc": Verbose, "vrb": Verbose, "finest": Verbose, "finer": Verbose,
	"debug": Debug, "dbg": Debug, "fine": Debug,
	"information": Information, "info": Information, "inf": Information, "notice": Information,
	"warning": Warning, "warn": Warning, "wrn": Warning,
	"error": Error, "err": Error, "eror": Error,
	"fatal": Fatal, "critical": Fatal, "crit": Fatal, "ftl": Fatal, "emergency": Fatal,
	"emerg": Fatal, "alert": Fatal, "panic": Fatal,
}

// ParseLevel parses a textual level (case-insensitive, trimmed). Numeric strings 0–5 are
// interpreted as the Tidemark/Serilog ordinal (0 = Verbose … 5 = Fatal). Syslog numbers are
// handled by SyslogLevel. The boolean is false when the input is not recognised.
func ParseLevel(s string) (Level, bool) {
	t := strings.ToLower(strings.TrimSpace(s))
	if l, ok := levelAliases[t]; ok {
		return l, true
	}
	if n, err := strconv.Atoi(t); err == nil && n >= int(Verbose) && n <= int(Fatal) {
		return Level(n), true //nolint:gosec // range-checked against Verbose..Fatal above
	}
	return Information, false
}

// ParseLevelOrDefault parses s and falls back to Information when it is unknown.
func ParseLevelOrDefault(s string) Level {
	l, _ := ParseLevel(s)
	return l
}

// Syslog severity numbers (RFC 5424).
const (
	syslogCrit    = 2
	syslogErr     = 3
	syslogWarning = 4
	syslogInfo    = 6
	syslogDebug   = 7
)

// SyslogLevel maps a syslog severity (0 emerg … 7 debug) to a level.
func SyslogLevel(severity int) (Level, bool) {
	switch {
	case severity < 0 || severity > syslogDebug:
		return Information, false
	case severity <= syslogCrit:
		return Fatal, true
	case severity == syslogErr:
		return Error, true
	case severity == syslogWarning:
		return Warning, true
	case severity <= syslogInfo:
		return Information, true
	default:
		return Debug, true
	}
}

// OTLP severity number ranges (OpenTelemetry log data model).
const (
	otlpTraceMax = 4
	otlpDebugMax = 8
	otlpInfoMax  = 12
	otlpWarnMax  = 16
	otlpErrorMax = 20
	otlpFatalMax = 24
)

// OTLPLevel maps an OTLP severityNumber (1–24) to a level; 0 or out-of-range values fall back
// to parsing severityText (and finally Information).
func OTLPLevel(number int32, text string) Level {
	switch {
	case number <= 0 || number > otlpFatalMax:
		return ParseLevelOrDefault(text)
	case number <= otlpTraceMax:
		return Verbose
	case number <= otlpDebugMax:
		return Debug
	case number <= otlpInfoMax:
		return Information
	case number <= otlpWarnMax:
		return Warning
	case number <= otlpErrorMax:
		return Error
	default:
		return Fatal
	}
}
