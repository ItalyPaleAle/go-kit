package awsses

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesv2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"github.com/italypaleale/go-kit/emailer/internal"
)

// AWSSES is an Emailer that uses AWS SES.
type AWSSES struct {
	sesClient *sesv2.Client
	from      string
}

func (a *AWSSES) Init(ctx context.Context, opts internal.InitOpts) error {
	const connStringFormat = "awsses://<access-key-id>:<secret-access-key>@<region>?fromAddress=<address>&fromName=<name>"

	// Validate the fields in the connection string
	accessKeyID := opts.ConnString.User.Username()
	secretAccessKey, _ := opts.ConnString.User.Password()
	region := opts.ConnString.Hostname()
	if accessKeyID == "" || secretAccessKey == "" {
		return fmt.Errorf("invalid connection string: missing AWS access key ID and/or secret access key; required format is '%s'", connStringFormat)
	}
	if region == "" {
		return fmt.Errorf("invalid connection string: missing AWS region; required format is '%s'", connStringFormat)
	}
	fromAddress := opts.ConnString.Query().Get("fromAddress")
	fromName := opts.ConnString.Query().Get("fromName") // fromName is optional
	if fromAddress == "" {
		return fmt.Errorf("invalid connection string: missing from address; required format is '%s'", connStringFormat)
	}

	if fromName != "" {
		a.from = fmt.Sprintf("%s <%s>", fromAddress, fromName)
	} else {
		a.from = fromAddress
	}

	// Initialize AWS SES client
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
	)
	if err != nil {
		return fmt.Errorf("failed to initialize AWS SES v2 client: %w", err)
	}
	a.sesClient = sesv2.NewFromConfig(cfg)

	return nil
}

func (a AWSSES) SendEmail(ctx context.Context, toEmail string, subject string, message internal.SendEmailMessage) error {
	body := &sesv2types.Body{
		Text: &sesv2types.Content{
			Data:    &message.Text,
			Charset: new("UTF-8"),
		},
	}
	if message.HTML != "" {
		body.Html = &sesv2types.Content{
			Data:    &message.HTML,
			Charset: new("UTF-8"),
		}
	}

	_, err := a.sesClient.SendEmail(ctx, &sesv2.SendEmailInput{
		Content: &sesv2types.EmailContent{
			Simple: &sesv2types.Message{
				Body: body,
				Subject: &sesv2types.Content{
					Data:    &subject,
					Charset: new("UTF-8"),
				},
			},
		},
		Destination: &sesv2types.Destination{
			ToAddresses: []string{toEmail},
		},
		FromEmailAddress: new(a.from),
	})
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	return nil
}
