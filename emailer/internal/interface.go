package internal

import (
	"context"
	"log/slog"
	"net/url"
)

// InitOpts is the options struct for the Init method
type InitOpts struct {
	// Connection string
	ConnString *url.URL
	// Optional logger
	// Uses the default slog if unset
	Logger *slog.Logger
}

// Emailer is the interface for objects that send email notifications
type Emailer interface {
	// Init the object with the connection string.
	Init(ctx context.Context, opts InitOpts) error
	// SendEmail sends an email to the specified address.
	SendEmail(ctx context.Context, toEmail string, subject string, message SendEmailMessage) error
}

// SendEmailMessage is the content of an email
type SendEmailMessage struct {
	// Email content as plain-text
	Text string
	// Email content as HTML, which can be empty
	HTML string
}
