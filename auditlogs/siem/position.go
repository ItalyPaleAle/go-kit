package siem

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// PositionVersion is the version of the encoded cursor
// It versions the encoding itself, so fields can be added later without a schema migration: an older binary reading a newer cursor fails loudly instead of misreading it
const PositionVersion = 1

// ErrPositionInvalid is returned by ParsePosition when the stored cursor cannot be read
// It is worth distinguishing from a store error: this one means the value itself is corrupt, not that the store is unreachable
var ErrPositionInvalid = errors.New("audit stream cursor is not valid")

// Separators in the encoded form
const (
	positionFieldSep = ';'
	positionPairSep  = '='
)

// positionFields are the cursor's field names, in the exact order they are encoded
// Encode and ParsePosition are both driven from this, so the two can only agree
var positionFields = [...]string{"v", "seq", "xactId", "eventId", "eventCreatedAt"}

// Indexes into positionFields
const (
	positionFieldV = iota
	positionFieldSeq
	positionFieldXactID
	positionFieldEventID
	positionFieldEventCreatedAt
)

// Position is the shipper's cursor into the audit event table.
//
// A Store uses whichever fields suit its backend, and ignores the rest: a monotonic counter fills Seq, while a transaction-id watermark fills (XactID, EventID).
// EventCreatedAt is for the caller, whose retention prune reads it to avoid deleting events that have not shipped yet.
type Position struct {
	V              int
	Seq            int64
	XactID         string
	EventID        string
	EventCreatedAt int64
}

// Encode returns the canonical serialization of the cursor.
//
// The form is a fixed sequence of name=value pairs, always all of them and always in the same order:
//
//	v=1;seq=12345;xactId=987654;eventId=019974c1-1f3a-7c4e-9b2d-6f1e8a4c0d55;eventCreatedAt=1789456123
//
// Byte-stability is required: a Store advances the cursor with a compare-and-swap that matches on the full previous value, so the same Position has to produce the same bytes every time, in this process and in the next one
func (p Position) Encode() string {
	version := p.V
	if version == 0 {
		version = PositionVersion
	}

	// Positionally aligned with positionFields
	values := [len(positionFields)]string{
		strconv.Itoa(version),
		strconv.FormatInt(p.Seq, 10),
		escapePositionValue(p.XactID),
		escapePositionValue(p.EventID),
		strconv.FormatInt(p.EventCreatedAt, 10),
	}

	sb := &strings.Builder{}
	sb.Grow(96)

	for i, name := range positionFields {
		if i > 0 {
			sb.WriteByte(positionFieldSep)
		}

		sb.WriteString(name)
		sb.WriteByte(positionPairSep)
		sb.WriteString(values[i])
	}

	return sb.String()
}

// ParsePosition reads a cursor produced by Position.Encode
// Anything else is rejected rather than partially understood: a cursor that is silently misread would ship the wrong events or skip them entirely
func ParsePosition(s string) (Position, error) {
	parts := strings.Split(s, string(positionFieldSep))
	if len(parts) != len(positionFields) {
		return Position{}, fmt.Errorf("%w: expected %d fields, got %d", ErrPositionInvalid, len(positionFields), len(parts))
	}

	values := [len(positionFields)]string{}
	for i, part := range parts {
		name, value, found := strings.Cut(part, string(positionPairSep))
		if !found {
			return Position{}, fmt.Errorf("%w: field %d is not a name=value pair", ErrPositionInvalid, i)
		}
		if name != positionFields[i] {
			return Position{}, fmt.Errorf("%w: expected field %q at index %d, got %q", ErrPositionInvalid, positionFields[i], i, name)
		}

		values[i] = value
	}

	version, err := strconv.Atoi(values[positionFieldV])
	if err != nil {
		return Position{}, fmt.Errorf("%w: version is not a number: %w", ErrPositionInvalid, err)
	}
	if version < 1 {
		return Position{}, fmt.Errorf("%w: version %d is not valid", ErrPositionInvalid, version)
	}
	if version > PositionVersion {
		return Position{}, fmt.Errorf("%w: version %d is newer than the supported version %d", ErrPositionInvalid, version, PositionVersion)
	}

	p := Position{
		V: version,
	}

	p.Seq, err = strconv.ParseInt(values[positionFieldSeq], 10, 64)
	if err != nil {
		return Position{}, fmt.Errorf("%w: seq is not a number: %w", ErrPositionInvalid, err)
	}

	p.EventCreatedAt, err = strconv.ParseInt(values[positionFieldEventCreatedAt], 10, 64)
	if err != nil {
		return Position{}, fmt.Errorf("%w: eventCreatedAt is not a number: %w", ErrPositionInvalid, err)
	}

	p.XactID, err = unescapePositionValue(values[positionFieldXactID])
	if err != nil {
		return Position{}, fmt.Errorf("%w: xactId: %w", ErrPositionInvalid, err)
	}

	p.EventID, err = unescapePositionValue(values[positionFieldEventID])
	if err != nil {
		return Position{}, fmt.Errorf("%w: eventId: %w", ErrPositionInvalid, err)
	}

	return p, nil
}

// positionHexDigits is uppercase so that escaping is a single, fixed mapping with no case to disagree on
const positionHexDigits = "0123456789ABCDEF"

// positionUnreserved reports whether a byte may appear in an encoded value as-is
// The set is the RFC 3986 unreserved characters, which excludes both separators, so no value can ever be mistaken for structure
func positionUnreserved(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case c == '-', c == '.', c == '_', c == '~':
		return true
	default:
		return false
	}
}

// escapePositionValue percent-encodes every byte outside the unreserved set.
func escapePositionValue(s string) string {
	needsEscaping := false
	for i := range len(s) {
		if !positionUnreserved(s[i]) {
			needsEscaping = true
			break
		}
	}

	if !needsEscaping {
		return s
	}

	buf := make([]byte, 0, len(s)+8)
	for i := range len(s) {
		c := s[i]
		if positionUnreserved(c) {
			buf = append(buf, c)
			continue
		}

		buf = append(buf, '%', positionHexDigits[c>>4], positionHexDigits[c&0x0f])
	}

	return string(buf)
}

// unescapePositionValue reverses escapePositionValue, rejecting anything that is not a well-formed escape
func unescapePositionValue(s string) (string, error) {
	if !strings.ContainsRune(s, '%') {
		return s, nil
	}

	buf := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c != '%' {
			buf = append(buf, c)
			i++
			continue
		}

		if i+2 >= len(s) {
			return "", errors.New("truncated escape sequence")
		}

		hi, ok := positionUnhex(s[i+1])
		if !ok {
			return "", fmt.Errorf("invalid escape sequence %q", s[i:i+3])
		}

		lo, ok := positionUnhex(s[i+2])
		if !ok {
			return "", fmt.Errorf("invalid escape sequence %q", s[i:i+3])
		}

		buf = append(buf, hi<<4|lo)
		i += 3
	}

	return string(buf), nil
}

// positionUnhex returns the value of a hex digit, and whether the byte was one
func positionUnhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}
