package internal

import (
	"fmt"
	"net/mail"
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

// ValidateEmailAddress returns an error if addr is not a parseable RFC 5322 email address
func ValidateEmailAddress(field, addr string) error {
	_, err := mail.ParseAddress(addr)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", field, err)
	}

	return nil
}
