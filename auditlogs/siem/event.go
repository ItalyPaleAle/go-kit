package siem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// emptyMetadata is the value used when an event carries no metadata
var emptyMetadata = json.RawMessage(`{}`)

// Event is a single audit event in the SIEM wire format.
type Event struct {
	SchemaVersion int       `json:"schemaVersion"`
	Source        string    `json:"source"`
	InstanceID    string    `json:"instanceId"`
	ID            string    `json:"id"`
	Time          time.Time `json:"time"`
	EventType     string    `json:"eventType"`
	Outcome       string    `json:"outcome"`
	AuthMethod    string    `json:"authMethod"`
	ActorUserID   string    `json:"actorUserId,omitempty"`
	TargetUserID  string    `json:"targetUserId,omitempty"`
	HTTPRequestID string    `json:"httpRequestId,omitempty"`
	ClientIP      string    `json:"clientIp,omitempty"`
	UserAgent     string    `json:"userAgent,omitempty"`

	// Attributes carries the application's own correlation identifiers
	// Entries with an empty value are dropped
	Attributes map[string]string `json:"attributes,omitempty"`

	// Metadata is the event's own free-form payload, passed through verbatim as a JSON object
	// This is always present, and "{}" when the event carries none
	Metadata json.RawMessage `json:"metadata"`

	// Position is where this event sits in the shipping order
	// This is internal and not part of the encoded data
	Position Position `json:"-"`
}

// envelope is the body sent when the format is FormatJSON
type envelope struct {
	SchemaVersion int       `json:"schemaVersion"`
	Source        string    `json:"source"`
	InstanceID    string    `json:"instanceId"`
	SentAt        time.Time `json:"sentAt"`
	Count         int       `json:"count"`
	Events        []Event   `json:"events"`
}

// stamp fills in the fields the Store does not populate and normalizes the ones that must always be present
func (e *Event) stamp(source string, instanceID string) {
	e.SchemaVersion = SchemaVersion
	e.Source = source
	e.InstanceID = instanceID

	// created_at is stored as Unix seconds, so the timestamp has second precision
	e.Time = e.Time.UTC()

	// An attribute with no value is the application saying the column was NULL, so drop it rather than emitting an empty string
	for k, v := range e.Attributes {
		if v == "" {
			delete(e.Attributes, k)
		}
	}

	// Metadata is passed through verbatim as a JSON object
	// We validate to ensure that it's valid JSON before submitting
	if len(e.Metadata) == 0 || !json.Valid(e.Metadata) {
		e.Metadata = emptyMetadata
	}
}

// encodeBatch renders the events in the given format and returns the body along with its content type
func encodeBatch(events []Event, format Format, source string, instanceID string, now time.Time) (body []byte, contentType string, err error) {
	// All events are stamped in place
	for i := range events {
		events[i].stamp(source, instanceID)
	}

	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)

	switch format {
	case FormatJSON:
		err = enc.Encode(envelope{
			SchemaVersion: SchemaVersion,
			Source:        source,
			InstanceID:    instanceID,
			SentAt:        now.UTC(),
			Count:         len(events),
			Events:        events,
		})
		if err != nil {
			return nil, "", fmt.Errorf("failed to encode batch envelope: %w", err)
		}
	default:
		// Encode terminates every line with a newline, including the last one
		for i := range events {
			err = enc.Encode(events[i])
			if err != nil {
				return nil, "", fmt.Errorf("failed to encode event %q: %w", events[i].ID, err)
			}
		}
	}

	return buf.Bytes(), format.contentType(), nil
}
