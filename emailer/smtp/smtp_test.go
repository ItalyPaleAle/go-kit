package smtp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/italypaleale/go-kit/emailer/internal"
)

func TestInit(t *testing.T) {
	// Use a full connection string so Init validates the same fields production code depends on
	connString, err := url.Parse("smtp://mailer:secret@mail.example.com:2525?fromAddress=sender@example.com&fromName=Sender+Name&tls=none")
	require.NoError(t, err)

	var emailer SMTPEmailer
	err = emailer.Init(t.Context(), internal.InitOpts{ConnString: connString})
	require.NoError(t, err)

	// Verify the normalized SMTP configuration is preserved for the later send path
	assert.Equal(t, "mail.example.com", emailer.host)
	assert.Equal(t, "2525", emailer.port)
	assert.Equal(t, "mail.example.com:2525", emailer.address)
	assert.Equal(t, "mailer", emailer.username)
	assert.Equal(t, "secret", emailer.password)
	assert.Equal(t, "Sender Name <sender@example.com>", emailer.from)
	assert.Equal(t, "sender@example.com", emailer.fromAddress)
	assert.Equal(t, smtpTLSNone, emailer.tlsMode)
	require.NotNil(t, emailer.tlsConfig)
	require.NotNil(t, emailer.dialContext)
}

func TestSendEmail(t *testing.T) {
	// Start a local SMTP server so the test can verify the wire-level SMTP session and MIME payload
	server := newSMTPTestServer(t)
	host, port, err := net.SplitHostPort(server.address())
	require.NoError(t, err)

	connString, err := url.Parse(fmt.Sprintf("smtp://mailer:secret@%s:%s?fromAddress=sender@example.com&fromName=Sender+Name&tls=none", host, port))
	require.NoError(t, err)

	var emailer SMTPEmailer
	err = emailer.Init(t.Context(), internal.InitOpts{ConnString: connString})
	require.NoError(t, err)

	// Send a multipart message so the test covers auth, envelope, and MIME body generation together
	err = emailer.SendEmail(t.Context(), "recipient@example.com", "Hello", internal.SendEmailMessage{
		Text: "Plain body",
		HTML: "<p>HTML body</p>",
	})
	require.NoError(t, err)

	// Validate the full SMTP session after the handler goroutine has finished processing the connection
	session, sessionErr := server.wait()
	require.NoError(t, sessionErr)
	assert.Contains(t, session.authCommand, "AUTH PLAIN")
	assert.Equal(t, "<sender@example.com>", session.mailFrom)
	assert.Equal(t, "<recipient@example.com>", session.rcptTo)
	assert.Contains(t, session.message, "From: Sender Name <sender@example.com>\r\n")
	assert.Contains(t, session.message, "To: recipient@example.com\r\n")
	assert.Contains(t, session.message, "Subject: Hello\r\n")
	assert.Contains(t, session.message, "Content-Type: multipart/alternative; boundary=")
	assert.Contains(t, session.message, "Content-Type: text/plain; charset=UTF-8")
	assert.Contains(t, session.message, "Content-Type: text/html; charset=UTF-8")
	assert.Contains(t, session.message, "Plain body")
	assert.Contains(t, session.message, "<p>HTML body</p>")
	assert.Contains(t, session.commands, "MAIL FROM:<sender@example.com>")
	assert.Contains(t, session.commands, "RCPT TO:<recipient@example.com>")
}

func TestInitRejectsHeaderInjection(t *testing.T) {
	t.Run("CRLF in from address is rejected", func(t *testing.T) {
		// %0d%0a decodes to CR/LF in the query value, simulating an attacker-controlled fromAddress
		connString, err := url.Parse("smtp://mail.example.com:2525?fromAddress=sender@example.com%0d%0aBcc:+attacker@evil.example&tls=none")
		require.NoError(t, err)

		var emailer SMTPEmailer
		err = emailer.Init(t.Context(), internal.InitOpts{ConnString: connString})
		require.ErrorContains(t, err, "from address")
		require.ErrorContains(t, err, "must not contain CR or LF")
	})

	t.Run("CRLF in from name is rejected", func(t *testing.T) {
		connString, err := url.Parse("smtp://mail.example.com:2525?fromAddress=sender@example.com&fromName=Joe%0d%0aBcc:+attacker@evil.example&tls=none")
		require.NoError(t, err)

		var emailer SMTPEmailer
		err = emailer.Init(t.Context(), internal.InitOpts{ConnString: connString})
		require.ErrorContains(t, err, "from name")
		require.ErrorContains(t, err, "must not contain CR or LF")
	})
}

func TestSendEmailRejectsHeaderInjection(t *testing.T) {
	// Configure a valid emailer; buildMessage validates before any network dial, so no server is needed
	connString, err := url.Parse("smtp://mail.example.com:2525?fromAddress=sender@example.com&tls=none")
	require.NoError(t, err)

	var emailer SMTPEmailer
	err = emailer.Init(t.Context(), internal.InitOpts{ConnString: connString})
	require.NoError(t, err)

	t.Run("CRLF in recipient is rejected", func(t *testing.T) {
		err := emailer.SendEmail(t.Context(), "victim@example.com\r\nBcc: attacker@evil.example", "Hello", internal.SendEmailMessage{Text: "Body"})
		require.ErrorContains(t, err, "recipient address")
		require.ErrorContains(t, err, "must not contain CR or LF")
	})

	t.Run("bare LF in subject is rejected", func(t *testing.T) {
		err := emailer.SendEmail(t.Context(), "recipient@example.com", "Hello\nBcc: attacker@evil.example", internal.SendEmailMessage{Text: "Body"})
		require.ErrorContains(t, err, "subject")
		require.ErrorContains(t, err, "must not contain CR or LF")
	})
}

type smtpTestSession struct {
	authCommand string
	mailFrom    string
	rcptTo      string
	message     string
	commands    []string
}

type smtpTestServer struct {
	listener  net.Listener
	sessionCh chan smtpTestSession
	errorCh   chan error
}

func newSMTPTestServer(t *testing.T) *smtpTestServer {
	// Bind an ephemeral local port so tests can run in parallel without conflicting listeners
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	server := &smtpTestServer{
		listener:  listener,
		sessionCh: make(chan smtpTestSession, 1),
		errorCh:   make(chan error, 1),
	}
	go server.serve()

	t.Cleanup(func() {
		listener.Close()
	})

	return server
}

func (s *smtpTestServer) address() string {
	return s.listener.Addr().String()
}

func (s *smtpTestServer) wait() (smtpTestSession, error) {
	// Wait for either a recorded SMTP session or a server-side error from the goroutine
	select {
	case session := <-s.sessionCh:
		return session, nil
	case err := <-s.errorCh:
		return smtpTestSession{}, err
	case <-time.After(5 * time.Second):
		return smtpTestSession{}, context.DeadlineExceeded
	}
}

func (s *smtpTestServer) serve() {
	// Accept a single client because each test only performs one SMTP delivery
	conn, err := s.listener.Accept()
	if err != nil {
		s.errorCh <- err
		return
	}

	session, err := s.handleConn(conn)
	if err != nil {
		s.errorCh <- err
		return
	}

	s.sessionCh <- session
}

func (s *smtpTestServer) handleConn(conn net.Conn) (smtpTestSession, error) {
	// Close the connection on every exit path so the test server never leaks sockets
	defer conn.Close()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	session := smtpTestSession{}

	// Start with the standard SMTP greeting so the client can proceed with EHLO
	err := writeSMTPResponse(writer, "220 localhost ESMTP test")
	if err != nil {
		return smtpTestSession{}, err
	}

	// Process commands until the client sends QUIT after the message is accepted
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return smtpTestSession{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		session.commands = append(session.commands, line)

		switch {
		case strings.HasPrefix(line, "EHLO "):
			err = writeSMTPResponse(writer, "250-localhost ESMTP test", "250 AUTH PLAIN")
		case strings.HasPrefix(line, "AUTH "):
			session.authCommand = line
			err = writeSMTPResponse(writer, "235 2.7.0 Authentication successful")
		case strings.HasPrefix(line, "MAIL FROM:"):
			session.mailFrom = strings.TrimPrefix(line, "MAIL FROM:")
			err = writeSMTPResponse(writer, "250 2.1.0 Ok")
		case strings.HasPrefix(line, "RCPT TO:"):
			session.rcptTo = strings.TrimPrefix(line, "RCPT TO:")
			err = writeSMTPResponse(writer, "250 2.1.5 Ok")
		case line == "DATA":
			err = writeSMTPResponse(writer, "354 End data with <CR><LF>.<CR><LF>")
			if err != nil {
				return smtpTestSession{}, err
			}
			session.message, err = readSMTPData(reader)
			if err != nil {
				return smtpTestSession{}, err
			}
			err = writeSMTPResponse(writer, "250 2.0.0 Ok: queued")
		case line == "QUIT":
			err = writeSMTPResponse(writer, "221 2.0.0 Bye")
			if err != nil {
				return smtpTestSession{}, err
			}
			return session, nil
		default:
			err = writeSMTPResponse(writer, "250 2.0.0 Ok")
		}

		if err != nil {
			return smtpTestSession{}, err
		}
	}
}

func writeSMTPResponse(writer *bufio.Writer, lines ...string) error {
	// Flush after every response so the client does not block waiting for server output
	for _, line := range lines {
		_, err := writer.WriteString(line + "\r\n")
		if err != nil {
			return err
		}
	}

	return writer.Flush()
}

func readSMTPData(reader *bufio.Reader) (string, error) {
	// DATA ends on a single dot line, with doubled leading dots used for transparent transport
	var body strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		if line == ".\r\n" {
			return body.String(), nil
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		_, err = io.WriteString(&body, line)
		if err != nil {
			return "", err
		}
	}
}
