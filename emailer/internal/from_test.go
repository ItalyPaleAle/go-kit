package internal

import (
	"net/mail"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatFromAddress(t *testing.T) {
	tests := []struct {
		name        string
		displayName string
		want        string
	}{
		{name: "no display name", want: "sender@example.com"},
		{name: "ASCII", displayName: "Sender Name", want: `"Sender Name" <sender@example.com>`},
		{name: "umlaut", displayName: "Max Müller", want: "=?utf-8?q?Max_M=C3=BCller?= <sender@example.com>"},
		{name: "comma", displayName: "Doe, Jane", want: `"Doe, Jane" <sender@example.com>`},
		{name: "quotes", displayName: `Jane "JJ" Doe`, want: `"Jane \"JJ\" Doe" <sender@example.com>`},
		{name: "backslash", displayName: `Jane \ Doe`, want: `"Jane \\ Doe" <sender@example.com>`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Check both sender and recipient formatting against valid wire representations
			sender := FormatFromAddress(tt.displayName, "sender@example.com")
			recipient := (EmailAddress{Name: tt.displayName, Address: "sender@example.com"}).Format()
			assert.Equal(t, tt.want, sender)
			assert.Equal(t, tt.want, recipient)

			// Parsing must recover the original identity without creating extra recipients
			addresses, err := mail.ParseAddressList(sender)
			require.NoError(t, err)
			require.Len(t, addresses, 1)
			assert.Equal(t, tt.displayName, addresses[0].Name)
			assert.Equal(t, "sender@example.com", addresses[0].Address)
		})
	}
}

func TestValidateEmailAddressAndHeaderValue(t *testing.T) {
	err := ValidateEmailAddress("recipient address", "user@example.com")
	require.NoError(t, err)

	err = ValidateEmailAddress("recipient address", "user+tag@sub.example.com")
	require.NoError(t, err)

	invalidAddrs := []struct {
		name string
		addr string
	}{
		{name: "CRLF in RFC 5322 comment", addr: "user@example.com (\r\nBcc: attacker@evil.example)"},
		{name: "display name with angle brackets", addr: "Attacker <user@example.com>"},
		{name: "comment without CRLF", addr: "user@example.com (comment)"},
		{name: "surrounding whitespace", addr: " user@example.com "},
		{name: "control char", addr: "user\x00@example.com"},
		{name: "multiple addresses", addr: "a@example.com, b@example.com"},
	}
	for _, tc := range invalidAddrs {
		t.Run(tc.name, func(t *testing.T) {
			vErr := ValidateEmailAddress("recipient address", tc.addr)
			require.Error(t, vErr)
		})
	}

	err = ValidateHeaderValue("subject", "Normal UTF-8 subject")
	require.NoError(t, err)

	err = ValidateHeaderValue("subject", "Line 1\r\nBcc: attacker@evil.example")
	require.ErrorContains(t, err, "must not contain CR or LF characters")
}
