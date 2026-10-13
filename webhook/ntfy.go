package webhook

import (
	"errors"
	"fmt"
	"mime"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// NtfyPriority is the priority of an ntfy notification
type NtfyPriority int

const (
	// NtfyPriorityDefault leaves the priority unset, so the server uses its default (3)
	NtfyPriorityDefault NtfyPriority = 0
	NtfyPriorityMin     NtfyPriority = 1
	NtfyPriorityLow     NtfyPriority = 2
	NtfyPriorityNormal  NtfyPriority = 3
	NtfyPriorityHigh    NtfyPriority = 4
	NtfyPriorityMax     NtfyPriority = 5
)

// NtfyMessage is the type for a message in the ntfy format
// It only includes the fields that ntfy accepts as headers and that are useful for a notification: attachments, actions, scheduled delivery, e-mail, and phone calls are not supported
type NtfyMessage struct {
	// Message is the body of the notification
	// If empty, ntfy uses a default text
	Message string
	// Title is the optional title of the notification
	// Non-ASCII characters are supported, and control characters (including line breaks) are replaced with spaces
	Title string
	// Priority is the optional priority of the notification, from NtfyPriorityMin (1) to NtfyPriorityMax (5)
	// If zero, the server default is used
	Priority NtfyPriority
	// Tags is the optional list of tags
	// Tags that match an emoji short code are shown as emojis
	// A tag must not contain commas
	Tags []string
	// Click is the optional URL to open when the notification is clicked
	Click string
	// Icon is the optional URL of the icon of the notification (JPEG or PNG)
	Icon string
}

// headers returns the HTTP headers for the message, validating and encoding the values
func (m NtfyMessage) headers() (map[string]string, error) {
	headers := make(map[string]string, 5)

	title := sanitizeNtfyHeader(m.Title)
	if title != "" {
		headers["Title"] = encodeNtfyHeader(title)
	}

	switch {
	case m.Priority == NtfyPriorityDefault:
		// Nop
	case m.Priority >= NtfyPriorityMin && m.Priority <= NtfyPriorityMax:
		headers["Priority"] = strconv.Itoa(int(m.Priority))
	default:
		return nil, fmt.Errorf("invalid priority %d: must be between %d and %d", m.Priority, NtfyPriorityMin, NtfyPriorityMax)
	}

	// Each tag is encoded on its own, so the separators between tags are not encoded
	tags := make([]string, 0, len(m.Tags))
	for _, tag := range m.Tags {
		if strings.Contains(tag, ",") {
			return nil, fmt.Errorf("invalid tag %q: must not contain commas", tag)
		}
		tag = sanitizeNtfyHeader(tag)
		if tag == "" {
			continue
		}
		tags = append(tags, encodeNtfyHeader(tag))
	}
	if len(tags) > 0 {
		headers["Tags"] = strings.Join(tags, ",")
	}

	if m.Click != "" {
		click, err := ntfyURL(m.Click)
		if err != nil {
			return nil, fmt.Errorf("invalid click URL: %w", err)
		}
		headers["Click"] = click
	}

	if m.Icon != "" {
		icon, err := ntfyURL(m.Icon)
		if err != nil {
			return nil, fmt.Errorf("invalid icon URL: %w", err)
		}
		headers["Icon"] = icon
	}

	return headers, nil
}

// sanitizeNtfyHeader replaces control characters, which include CR and LF, with spaces and trims the result
// This makes header injection impossible, and the replacement (rather than an error) means that a multi-line value coming from a monitoring system does not drop the notification
func sanitizeNtfyHeader(val string) string {
	val = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, val)
	return strings.TrimSpace(val)
}

// encodeNtfyHeader encodes a value as an RFC 2047 encoded-word when it contains non-ASCII characters
// ntfy supports raw UTF-8 in headers, but not every HTTP stack does, while the encoded form is plain ASCII and is decoded by ntfy
func encodeNtfyHeader(val string) string {
	return mime.QEncoding.Encode("UTF-8", val)
}

// ntfyURL validates a URL for a header and returns it in a form that is ASCII-only
func ntfyURL(val string) (string, error) {
	for _, r := range val {
		if unicode.IsControl(r) {
			return "", errors.New("must not contain control characters")
		}
	}

	parsed, err := url.Parse(strings.TrimSpace(val))
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" {
		return "", errors.New("must include a scheme")
	}

	// Percent-encode any non-ASCII byte that url.URL leaves as-is, such as in the query string
	var sb strings.Builder
	for _, c := range []byte(parsed.String()) {
		if c >= utf8.RuneSelf {
			fmt.Fprintf(&sb, "%%%02X", c)
			continue
		}
		sb.WriteByte(c)
	}

	return sb.String(), nil
}
