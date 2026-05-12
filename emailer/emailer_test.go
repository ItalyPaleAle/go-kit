package emailer

import (
	"testing"

	smtpemailer "github.com/italypaleale/go-kit/emailer/smtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEmailerSMTP(t *testing.T) {
	// Use the SMTP scheme here so the test verifies the factory forwards InitOpts correctly
	emailer, err := NewEmailer(t.Context(), NewEmailerOpts{
		ConnString: "smtp://mailer:secret@mail.example.com:2525?fromAddress=sender@example.com&fromName=Sender+Name&tls=none",
	})
	require.NoError(t, err)

	// A successful construction proves the parsed connection string reached the SMTP initializer
	_, ok := emailer.(*smtpemailer.SMTPEmailer)
	assert.True(t, ok)
}
