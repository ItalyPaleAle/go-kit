package siem

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testEncodedPosition = `v=1;seq=12345;xactId=987654;eventId=019974c1-1f3a-7c4e-9b2d-6f1e8a4c0d55;eventCreatedAt=1789456123`

func testPosition() Position {
	return Position{
		V:              1,
		Seq:            12345,
		XactID:         "987654",
		EventID:        "019974c1-1f3a-7c4e-9b2d-6f1e8a4c0d55",
		EventCreatedAt: 1789456123,
	}
}

func TestPositionEncodeIsStable(t *testing.T) {
	pos := testPosition()

	// The compare-and-swap matches on the full previous value, so the same cursor has to produce the same bytes every time
	for range 20 {
		assert.Equal(t, testEncodedPosition, pos.Encode())
	}
}

func TestPositionEncodeDefaultsTheVersion(t *testing.T) {
	encoded := Position{Seq: 7}.Encode()

	assert.Equal(t, `v=1;seq=0;xactId=;eventId=;eventCreatedAt=0`, Position{}.Encode())
	assert.Contains(t, encoded, "v=1;")
}

func TestPositionEncodeAlwaysWritesEveryField(t *testing.T) {
	// A field is never dropped for being empty, so the shape does not depend on which backend filled it in
	encoded := Position{V: 1, Seq: 42}.Encode()

	assert.Equal(t, `v=1;seq=42;xactId=;eventId=;eventCreatedAt=0`, encoded)
}

func TestPositionRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		pos  Position
	}{
		{name: "fully populated", pos: testPosition()},
		{name: "zero", pos: Position{V: 1}},
		{name: "sqlite shape", pos: Position{V: 1, Seq: 99, EventCreatedAt: 1789456123}},
		{name: "postgres shape", pos: Position{V: 1, XactID: "987654", EventID: "019974c1-1f3a-7c4e-9b2d-6f1e8a4c0d55", EventCreatedAt: 1789456123}},
		{name: "negative values", pos: Position{V: 1, Seq: -1, EventCreatedAt: -1}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded := tc.pos.Encode()

			got, err := ParsePosition(encoded)
			require.NoError(t, err)
			assert.Equal(t, tc.pos, got)

			// Re-encoding the parsed value has to produce the same bytes, or the compare-and-swap would silently stop matching
			assert.Equal(t, encoded, got.Encode())
		})
	}
}

func TestPositionEscapesSeparators(t *testing.T) {
	// The identifiers a Store puts here are numbers and UUIDs in practice, but a value carrying a separator must not be able to forge structure
	pos := Position{V: 1, XactID: "a;b=c", EventID: "d%e f"}

	encoded := pos.Encode()
	assert.Equal(t, `v=1;seq=0;xactId=a%3Bb%3Dc;eventId=d%25e%20f;eventCreatedAt=0`, encoded)

	got, err := ParsePosition(encoded)
	require.NoError(t, err)
	assert.Equal(t, pos, got)
}

func TestPositionLeavesOrdinaryIdentifiersUnescaped(t *testing.T) {
	// Readability in a database cell matters: an operator looking at the row should recognize the cursor
	assert.Contains(t, testPosition().Encode(), "eventId=019974c1-1f3a-7c4e-9b2d-6f1e8a4c0d55")
}

func TestParsePositionRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "too few fields", input: `v=1;seq=1`},
		{name: "too many fields", input: testEncodedPosition + ";extra=1"},
		{name: "missing pair separator", input: `v=1;seq;xactId=;eventId=;eventCreatedAt=0`},
		{name: "unknown field name", input: `v=1;sequence=1;xactId=;eventId=;eventCreatedAt=0`},
		{name: "fields out of order", input: `seq=1;v=1;xactId=;eventId=;eventCreatedAt=0`},
		{name: "version not a number", input: `v=x;seq=1;xactId=;eventId=;eventCreatedAt=0`},
		{name: "version zero", input: `v=0;seq=1;xactId=;eventId=;eventCreatedAt=0`},
		{name: "seq not a number", input: `v=1;seq=x;xactId=;eventId=;eventCreatedAt=0`},
		{name: "seq overflows", input: `v=1;seq=99999999999999999999;xactId=;eventId=;eventCreatedAt=0`},
		{name: "eventCreatedAt not a number", input: `v=1;seq=1;xactId=;eventId=;eventCreatedAt=x`},
		{name: "truncated escape", input: `v=1;seq=1;xactId=a%2;eventId=;eventCreatedAt=0`},
		{name: "invalid escape", input: `v=1;seq=1;xactId=a%zz;eventId=;eventCreatedAt=0`},
		{name: "invalid escape in eventId", input: `v=1;seq=1;xactId=;eventId=%GG;eventCreatedAt=0`},
		{name: "json from an older build", input: `{"v":1,"seq":12345}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePosition(tc.input)

			require.Error(t, err)
			assert.ErrorIs(t, err, ErrPositionInvalid)
		})
	}
}

func TestParsePositionRejectsANewerVersion(t *testing.T) {
	// An older binary must fail loudly rather than misread a cursor a newer one wrote
	_, err := ParsePosition(`v=99;seq=1;xactId=;eventId=;eventCreatedAt=0`)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrPositionInvalid)
	assert.ErrorContains(t, err, "newer than the supported version")
}

func TestEscapePositionValueRoundTrip(t *testing.T) {
	// Every byte value has to survive the round trip, including the ones that never occur in practice
	raw := make([]byte, 0, 256)
	for i := range 256 {
		raw = append(raw, byte(i))
	}

	escaped := escapePositionValue(string(raw))
	for _, c := range []byte(escaped) {
		require.True(t, positionUnreserved(c) || c == '%', "escaped output contains a raw %q", c)
	}

	got, err := unescapePositionValue(escaped)
	require.NoError(t, err)
	assert.Equal(t, string(raw), got)
}

func TestEscapePositionValueUsesUppercaseHex(t *testing.T) {
	// One fixed mapping, so two builds cannot disagree on the bytes
	assert.Equal(t, "%FF", escapePositionValue("\xff"))
}
