// Package siem ships audit log events to an external collector (a SIEM) over HTTP
//
// The package does not own the audit events: it reads them from a Store that the application implements on top of its own audit table, and it persists its cursor through the same interface
// Delivery is at-least-once: the cursor advances only after the collector returns a 2xx, so a batch that is delivered but whose response is lost is sent again
// Collectors are expected to deduplicate on Event.ID
package siem

import (
	"time"
)

// Format is the wire format used for a batch
type Format string

const (
	// FormatNDJSON sends one compact JSON object per line, with content type application/x-ndjson
	// This is the format Splunk HEC /raw, Elastic, Vector, Loki and Fluent Bit all ingest natively
	FormatNDJSON Format = "ndjson"
	// FormatJSON sends a single JSON envelope containing the batch, with content type application/json
	FormatJSON Format = "json"
)

// SchemaVersion is the version of the event payload contract
// It is emitted on every event and in the batch envelope, and is bumped only on a breaking change to the payload
const SchemaVersion = 1

// Content types emitted for each format
const (
	ndjsonContentType = "application/x-ndjson"
	jsonContentType   = "application/json"
)

// Defaults applied by NewShipper
const (
	DefaultBatchSize     = 100
	DefaultFlushInterval = 10 * time.Second
	DefaultHeaderPrefix  = "X-Auditlog"
	DefaultAuthHeader    = "Authorization"
)

const (
	MinBatchSize     = 1
	MaxBatchSize     = 1000
	MinFlushInterval = time.Second
	MaxFlushInterval = 5 * time.Minute
)

// Backoff schedule for failed deliveries
const (
	backoffBase = time.Second
	backoffCap  = 5 * time.Minute
)

// Outcome values reported to a Metrics implementation
const (
	OutcomeSent     = "sent"
	OutcomeFiltered = "filtered"
	OutcomeRetried  = "retried"
	OutcomeFailed   = "failed"
)

// IsValid returns true if the format is valid
func (f Format) IsValid() bool {
	switch f {
	case FormatNDJSON, FormatJSON:
		return true
	default:
		return false
	}
}

// contentType returns the value of the Content-Type header for the format
func (f Format) contentType() string {
	if f == FormatJSON {
		return jsonContentType
	}
	return ndjsonContentType
}
