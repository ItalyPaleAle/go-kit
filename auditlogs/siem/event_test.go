package siem

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testEvent() Event {
	return Event{
		ID:            "019974c1-1f3a-7c4e-9b2d-6f1e8a4c0d55",
		Time:          time.Date(2026, 9, 15, 20, 11, 3, 0, time.UTC),
		EventType:     "order.confirm",
		Outcome:       "success",
		AuthMethod:    "session",
		ActorUserID:   "u_9f2c",
		TargetUserID:  "u_9f2c",
		HTTPRequestID: "01JC",
		ClientIP:      "203.0.113.41",
		UserAgent:     "Mozilla/5.0 <test>",
		Attributes:    map[string]string{"orderId": "3f7a", "tenantId": "t-42"},
		Metadata:      json.RawMessage(`{"operation":"decrypt","algorithm":"ES384"}`),
		Position:      Position{V: 1, Seq: 42, XactID: "987654", EventID: "019974c1-1f3a-7c4e-9b2d-6f1e8a4c0d55"},
	}
}

func TestEncodeBatchNDJSON(t *testing.T) {
	const expected = `{"schemaVersion":1,"source":"testapp","instanceId":"inst-1","id":"019974c1-1f3a-7c4e-9b2d-6f1e8a4c0d55","time":"2026-09-15T20:11:03Z","eventType":"order.confirm","outcome":"success","authMethod":"session","actorUserId":"u_9f2c","targetUserId":"u_9f2c","httpRequestId":"01JC","clientIp":"203.0.113.41","userAgent":"Mozilla/5.0 <test>","attributes":{"orderId":"3f7a","tenantId":"t-42"},"metadata":{"operation":"decrypt","algorithm":"ES384"}}` + "\n"

	events := []Event{testEvent()}
	body, contentType, err := encodeBatch(events, FormatNDJSON, "testapp", "inst-1", time.Now())

	require.NoError(t, err)
	assert.Equal(t, ndjsonContentType, contentType) //nolint:testifylint // comparing a content type, not JSON
	// This is a golden test: the exact bytes matter, including the trailing newline and the field order
	assert.Equal(t, expected, string(body)) //nolint:testifylint // asserting exact bytes, not JSON equivalence
}

func TestEncodeBatchNDJSONMultipleEvents(t *testing.T) {
	first := testEvent()
	second := testEvent()
	second.ID = "019974c1-1f3a-7c4e-9b2d-6f1e8a4c0d56"
	second.EventType = "order.create"

	body, _, err := encodeBatch([]Event{first, second}, FormatNDJSON, "testapp", "inst-1", time.Now())
	require.NoError(t, err)

	// Every line is terminated, including the last one
	assert.True(t, strings.HasSuffix(string(body), "\n"))

	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	require.Len(t, lines, 2)

	// Each line stays fully attributable on its own after a collector splits the batch apart
	for _, line := range lines {
		var decoded map[string]any
		err = json.Unmarshal([]byte(line), &decoded)
		require.NoError(t, err)
		assert.InDelta(t, float64(SchemaVersion), decoded["schemaVersion"], 0)
		assert.Equal(t, "testapp", decoded["source"])
		assert.Equal(t, "inst-1", decoded["instanceId"])
	}
}

func TestEncodeBatchJSON(t *testing.T) {
	sentAt := time.Date(2026, 9, 15, 20, 11, 8, 0, time.UTC)

	body, contentType, err := encodeBatch([]Event{testEvent()}, FormatJSON, "testapp", "inst-1", sentAt)
	require.NoError(t, err)
	assert.Equal(t, jsonContentType, contentType) //nolint:testifylint // comparing a content type, not JSON

	var decoded envelope
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)

	assert.Equal(t, SchemaVersion, decoded.SchemaVersion)
	assert.Equal(t, "testapp", decoded.Source)
	assert.Equal(t, "inst-1", decoded.InstanceID)
	assert.True(t, decoded.SentAt.Equal(sentAt))
	assert.Equal(t, 1, decoded.Count)
	require.Len(t, decoded.Events, 1)
	assert.Equal(t, "019974c1-1f3a-7c4e-9b2d-6f1e8a4c0d55", decoded.Events[0].ID)
}

func TestEncodeBatchOmitsEmptyFields(t *testing.T) {
	e := Event{
		ID:         "019974c1-1f3a-7c4e-9b2d-6f1e8a4c0d55",
		Time:       time.Date(2026, 9, 15, 20, 11, 3, 0, time.UTC),
		EventType:  "order.expire",
		Outcome:    "success",
		AuthMethod: "system",
	}

	body, _, err := encodeBatch([]Event{e}, FormatNDJSON, "testapp", "inst-1", time.Now())
	require.NoError(t, err)

	var decoded map[string]json.RawMessage
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)

	// Nullable columns are omitted, never emitted as null
	for _, key := range []string{"actorUserId", "targetUserId", "httpRequestId", "clientIp", "userAgent"} {
		assert.NotContains(t, decoded, key)
	}

	// An event with no application attributes carries no attributes object at all
	assert.NotContains(t, decoded, "attributes")

	// Metadata is always present, as an object, even when the event has none
	assert.JSONEq(t, `{}`, string(decoded["metadata"]))
}

func TestEncodeBatchNeverEmitsTheShippingKey(t *testing.T) {
	body, _, err := encodeBatch([]Event{testEvent()}, FormatNDJSON, "testapp", "inst-1", time.Now())
	require.NoError(t, err)

	// seq and xact_id are backend-specific plumbing and must not leak into the public contract
	assert.NotContains(t, string(body), "seq")
	assert.NotContains(t, string(body), "xactId")
	assert.NotContains(t, string(body), "987654")
}

func TestEncodeBatchNormalizesMetadata(t *testing.T) {
	t.Run("replaces malformed metadata rather than stalling the feed", func(t *testing.T) {
		e := testEvent()
		e.Metadata = json.RawMessage(`{"broken"`)

		body, _, err := encodeBatch([]Event{e}, FormatNDJSON, "testapp", "inst-1", time.Now())

		require.NoError(t, err)
		assert.Contains(t, string(body), `"metadata":{}`)
	})

	t.Run("passes a metadata object through verbatim", func(t *testing.T) {
		e := testEvent()
		e.Metadata = json.RawMessage(`{"keyLabel":"prod-backup"}`)

		body, _, err := encodeBatch([]Event{e}, FormatNDJSON, "testapp", "inst-1", time.Now())

		require.NoError(t, err)
		assert.Contains(t, string(body), `"metadata":{"keyLabel":"prod-backup"}`)
	})
}

func TestEncodeBatchNormalizesTheTimestamp(t *testing.T) {
	loc := time.FixedZone("CEST", 2*60*60)
	e := testEvent()
	e.Time = time.Date(2026, 9, 15, 22, 11, 3, 0, loc)

	body, _, err := encodeBatch([]Event{e}, FormatNDJSON, "testapp", "inst-1", time.Now())

	require.NoError(t, err)
	assert.Contains(t, string(body), `"time":"2026-09-15T20:11:03Z"`)
}

func TestEncodeBatchDoesNotEscapeHTML(t *testing.T) {
	e := testEvent()
	e.UserAgent = "curl/8.0 <script>&"

	body, _, err := encodeBatch([]Event{e}, FormatNDJSON, "testapp", "inst-1", time.Now())

	require.NoError(t, err)
	assert.Contains(t, string(body), `"userAgent":"curl/8.0 <script>&"`)
	assert.NotContains(t, string(body), "\\u003c")
	assert.NotContains(t, string(body), "\\u0026")
}

func TestEncodeBatchAttributes(t *testing.T) {
	t.Run("drops attributes with no value", func(t *testing.T) {
		// An empty value is the application saying the column was NULL
		e := testEvent()
		e.Attributes = map[string]string{"orderId": "3f7a", "tenantId": ""}

		body, _, err := encodeBatch([]Event{e}, FormatNDJSON, "testapp", "inst-1", time.Now())

		require.NoError(t, err)
		assert.Contains(t, string(body), `"attributes":{"orderId":"3f7a"}`)
	})

	t.Run("omits the object when every attribute is empty", func(t *testing.T) {
		e := testEvent()
		e.Attributes = map[string]string{"orderId": "", "tenantId": ""}

		body, _, err := encodeBatch([]Event{e}, FormatNDJSON, "testapp", "inst-1", time.Now())

		require.NoError(t, err)
		assert.NotContains(t, string(body), "attributes")
	})

	t.Run("is not merged into metadata", func(t *testing.T) {
		// The application's own payload reaches the collector byte for byte, so it can be compared against the source table
		e := testEvent()
		e.Metadata = json.RawMessage(`{"keyLabel":"prod-backup"}`)

		body, _, err := encodeBatch([]Event{e}, FormatNDJSON, "testapp", "inst-1", time.Now())

		require.NoError(t, err)
		assert.Contains(t, string(body), `"metadata":{"keyLabel":"prod-backup"}`)
	})

	t.Run("serializes the keys in a stable order", func(t *testing.T) {
		e := testEvent()
		e.Attributes = map[string]string{"zeta": "3", "alpha": "1", "mid": "2"}

		body, _, err := encodeBatch([]Event{e}, FormatNDJSON, "testapp", "inst-1", time.Now())

		require.NoError(t, err)
		assert.Contains(t, string(body), `"attributes":{"alpha":"1","mid":"2","zeta":"3"}`)
	})
}
