package emailer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/italypaleale/go-kit/emailer/awsses"
	"github.com/italypaleale/go-kit/emailer/console"
	"github.com/italypaleale/go-kit/emailer/internal"
	"github.com/italypaleale/go-kit/emailer/sendgrid"
	"github.com/italypaleale/go-kit/emailer/smtp"
)

// Emailer is the interface for objects that send email notifications
type Emailer = internal.Emailer

// SendEmailMessage is the content of an email
type SendEmailMessage = internal.SendEmailMessage

// NewEmailerOpts is the options struct for NewEmailer
type NewEmailerOpts struct {
	// Connection string
	ConnString string
	// Optional logger
	// Uses the default slog if unset
	Logger *slog.Logger
}

// NewEmailer returns a configured Emailer object based on the connection string
func NewEmailer(ctx context.Context, opts NewEmailerOpts) (Emailer, error) {
	// Parse the connection string
	if opts.ConnString == "" {
		return nil, errors.New("emailer connection string is empty")
	}
	connString, err := url.Parse(opts.ConnString)
	if err != nil {
		return nil, fmt.Errorf("emailer connection string is invalid: %w", err)
	}

	// Set default logger
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	// Get the correct emailer based on the connection string
	var e Emailer
	switch connString.Scheme {
	case "awsses":
		e = &awsses.AWSSES{}
	case "sendgrid":
		e = &sendgrid.SendGridEmailer{}
	case "smtp":
		e = &smtp.SMTPEmailer{}
	case "console":
		opts.Logger.WarnContext(ctx, "The 'console' emailer is meant to be used for development only")
		e = &console.ConsoleEmailer{}
	default:
		return nil, fmt.Errorf("invalid email sender type '%s'", connString.Scheme)
	}

	// Init the emailer
	err = e.Init(ctx, internal.InitOpts{
		ConnString: connString,
		Logger:     opts.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to init email sender '%s': %w", connString.Scheme, err)
	}

	return e, nil
}
