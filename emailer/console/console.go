package console

import (
	"context"
	"log/slog"

	"github.com/italypaleale/go-kit/emailer/internal"
)

// ConsoleEmailer is an object that implements the Emailer interface and prints all messages to the console.
// This is meant to be used for development only
type ConsoleEmailer struct {
	log *slog.Logger
}

func (s *ConsoleEmailer) Init(ctx context.Context, opts internal.InitOpts) error {
	s.log = opts.Logger

	return nil
}

// SendEmail sends an email using SendGrid.
func (s *ConsoleEmailer) SendEmail(ctx context.Context, toEmail string, subject string, message internal.SendEmailMessage) error {
	// Print the email as log
	s.log.InfoContext(ctx, "Invoked SendEmail on ConsoleEmailer",
		slog.String("to", toEmail),
		slog.String("subject", subject),
		slog.String("text", message.Text),
		slog.String("html", message.HTML),
	)
	return nil
}
