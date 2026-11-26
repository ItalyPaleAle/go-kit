package internal

import (
	"fmt"
	"net/mail"
	"strings"
	"unicode"
)

// FormatFromAddress returns the display form expected by mail providers when a sender name is present
func FormatFromAddress(fromName string, fromAddress string) string {
	// Keep the raw address when no display name was configured so callers do not emit an empty label
	if fromName == "" {
		return fromAddress
	}

	// Encode non-ASCII display names and quote special characters so mail providers receive valid address headers
	return (&mail.Address{Name: fromName, Address: fromAddress}).String()
}

// Format renders the address in the "Name <address>" form when a display name is set, or the bare address otherwise
func (a EmailAddress) Format() string {
	return FormatFromAddress(a.Name, a.Address)
}

// ValidateHeaderValue rejects values containing CR or LF, which could otherwise inject extra headers
func ValidateHeaderValue(field string, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("invalid %s: must not contain CR or LF characters", field)
	}

	return nil
}

// ValidateEmailAddress returns an error if addr is not a bare, single RFC 5322 addr-spec without comments, display names, or control characters
func ValidateEmailAddress(field string, addr string) error {
	err := ValidateHeaderValue(field, addr)
	if err != nil {
		return err
	}

	for _, r := range addr {
		if unicode.IsControl(r) {
			return fmt.Errorf("invalid %s: must not contain control characters", field)
		}
	}

	parsed, err := mail.ParseAddress(addr)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", field, err)
	}
	if parsed.Name != "" || parsed.Address != addr {
		return fmt.Errorf("invalid %s: must be a bare email address", field)
	}

	return nil
}
