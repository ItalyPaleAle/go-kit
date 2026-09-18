package siem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/italypaleale/go-kit/internal/webhooktransport"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	kclock "k8s.io/utils/clock"
)

const (
	// requestTimeout for a single delivery attempt
	requestTimeout = 30 * time.Second

	// lagWarnThreshold is how far the cursor may fall behind before every flush logs a warning
	// A backlog becomes visible here long before the caller's retention grace period expires
	lagWarnThreshold = 24 * time.Hour

	// stallLogThreshold is how long a non-retryable response has to persist before it is logged at error level
	stallLogThreshold = 5 * time.Minute

	// nudgeBatchWindow is how long the loop holds a nudge before reading, so a burst of inserts ships as one batch instead of one request per event
	nudgeBatchWindow = 1500 * time.Millisecond
)

// errBatchTooLarge signals that the collector rejected the body as too large
// The batch size has already been reduced when this is returned, so the caller re-reads a smaller batch
var errBatchTooLarge = errors.New("collector rejected the batch as too large")

// ShipperOptions contains the options for NewShipper
type ShipperOptions struct {
	// Store is the backing store events are read from and the cursor is persisted to (required)
	Store Store

	// URL is the collector endpoint (required)
	URL string

	// Source identifies the application emitting the events, and is included in every event (required)
	Source string

	// InstanceID identifies the process emitting the events, and is included in every event
	InstanceID string

	// Format is the wire format
	// Defaults to FormatNDJSON
	Format Format

	// Key is the optional credential for the collector
	// It is sent verbatim, with no scheme prefix added, so values such as "Splunk abc123", "Bearer …" and "ApiKey …" all work
	Key string

	// AuthorizationHeader is the name of the header the Key is sent in
	// SIEMs like Splunk HEC do not use Authorization and need this overwritten
	// Defaults to "Authorization"
	AuthorizationHeader string

	// HeaderPrefix is the prefix for the informational request headers (-Instance, -Schema-Version, -Batch-Count)
	// Defaults to "X-Auditlog"
	HeaderPrefix string

	// UserAgent is the value of the User-Agent header
	UserAgent string

	// BatchSize is the maximum number of events sent in one request
	// It must be between MinBatchSize and MaxBatchSize
	// Defaults to DefaultBatchSize
	BatchSize int

	// FlushInterval is how long the loop waits before polling again when there is nothing to send
	// It must otherwise be between MinFlushInterval and MaxFlushInterval
	// Defaults to DefaultFlushInterval
	FlushInterval time.Duration

	// EventTypes restricts the feed to the listed event types
	// Entries are either exact ("account.login") or area wildcards ("account.*")
	// An empty list ships everything
	EventTypes []string

	// KnownEventTypes is the set of event types the application can emit
	// When set, every EventTypes entry is validated against it, so a typo fails at startup instead of silently dropping the feed
	KnownEventTypes []string

	// AllowPrivateIPs permits the collector to resolve to a private or otherwise non-routable address
	AllowPrivateIPs bool

	// Metrics receives the shipper's observability signals
	// Optional
	Metrics Metrics

	// Logger to use instead of the default slog instance
	Logger *slog.Logger

	clock kclock.Clock
}

// Shipper reads settled audit events from a Store and POSTs them to a collector.
type Shipper struct {
	store      Store
	client     *http.Client
	url        string
	format     Format
	key        string
	authHeader string
	hdrPrefix  string
	userAgent  string
	source     string
	instanceID string
	filter     filter

	// batchSize shrinks (and never grows back) when the collector rejects a body as too large
	batchSize     int
	flushInterval time.Duration

	metrics Metrics
	log     *slog.Logger
	clock   kclock.Clock

	// nudge wakes the loop early after an insert
	nudge chan struct{}

	// failureStreak counts consecutive failed delivery attempts, so the recovery can be logged once
	failureStreak int
	// stalledSince is when the current run of non-retryable responses started
	stalledSince time.Time
}

// NewShipper creates a new Shipper
func NewShipper(opts ShipperOptions) (*Shipper, error) {
	if opts.clock == nil {
		opts.clock = kclock.RealClock{}
	}

	if opts.Store == nil {
		return nil, errors.New("store is required")
	}
	if opts.Source == "" {
		return nil, errors.New("source is required")
	}

	err := validateURL(opts.URL)
	if err != nil {
		return nil, err
	}

	// Format defaults to NDJSON
	format := FormatNDJSON
	if opts.Format != "" {
		if !opts.Format.IsValid() {
			return nil, errors.New("format is not valid")
		}

		format = opts.Format
	}

	f, err := newFilter(opts.EventTypes, opts.KnownEventTypes)
	if err != nil {
		return nil, err
	}

	batchSize := opts.BatchSize
	if batchSize == 0 {
		batchSize = DefaultBatchSize
	}
	if batchSize < MinBatchSize || batchSize > MaxBatchSize {
		return nil, fmt.Errorf("batch size %d is out of range: must be between %d and %d", batchSize, MinBatchSize, MaxBatchSize)
	}

	flushInterval := opts.FlushInterval
	if flushInterval == 0 {
		flushInterval = DefaultFlushInterval
	}
	if flushInterval < MinFlushInterval || flushInterval > MaxFlushInterval {
		return nil, fmt.Errorf("flush interval %v is out of range: must be between %v and %v", flushInterval, MinFlushInterval, MaxFlushInterval)
	}

	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.AuthorizationHeader == "" {
		opts.AuthorizationHeader = DefaultAuthHeader
	}
	if opts.HeaderPrefix == "" {
		opts.HeaderPrefix = DefaultHeaderPrefix
	}

	client := &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Redirects are never followed, regardless of AllowPrivateIPs
			return http.ErrUseLastResponse
		},
		Transport: otelhttp.NewTransport(webhooktransport.New(webhooktransport.Options{
			AllowPrivateIPs: opts.AllowPrivateIPs,
		})),
	}

	return &Shipper{
		store:         opts.Store,
		client:        client,
		url:           opts.URL,
		format:        format,
		key:           opts.Key,
		authHeader:    opts.AuthorizationHeader,
		hdrPrefix:     opts.HeaderPrefix,
		userAgent:     opts.UserAgent,
		source:        opts.Source,
		instanceID:    opts.InstanceID,
		filter:        f,
		batchSize:     batchSize,
		flushInterval: flushInterval,
		metrics:       opts.Metrics,
		log:           opts.Logger,
		clock:         opts.clock,
		nudge:         make(chan struct{}, 1),
	}, nil
}

// validateURL checks that the collector URL parses and uses an allowed scheme
func validateURL(raw string) error {
	if raw == "" {
		return errors.New("URL is required")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	switch parsed.Scheme {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("URL has disallowed scheme %q: only http and https are permitted", parsed.Scheme)
	}
}

// Nudge wakes the loop early so a freshly-written event does not have to wait out the flush interval
// It never blocks
func (s *Shipper) Nudge() {
	select {
	case s.nudge <- struct{}{}:
	default:
		// A wake-up is already pending
	}
}

// clearNudge consumes a pending wake-up
// It runs immediately before a read, so that a nudge arriving during that read survives into the next cycle rather than being swallowed by it
func (s *Shipper) clearNudge() {
	select {
	case <-s.nudge:
	default:
	}
}

// nudgeWindow is how long a nudge is held before reading
func (s *Shipper) nudgeWindow() time.Duration {
	// Ensure it's never less than the flush interval
	return min(nudgeBatchWindow, s.flushInterval)
}

// Run ships events until ctx is canceled
// One Shipper runs once: a second concurrent Run would race on the batch size and the failure counters, and would drive the Store from two goroutines at once
func (s *Shipper) Run(ctx context.Context) error {
	s.log.InfoContext(ctx, "Audit log SIEM shipper started",
		slog.String("url", s.url),
		slog.String("format", string(s.format)),
		slog.Int("batchSize", s.batchSize),
		slog.Duration("flushInterval", s.flushInterval),
	)
	defer s.log.InfoContext(ctx, "Audit log SIEM shipper stopped")

	for {
		// Clear any nudge that may be present
		s.clearNudge()

		err := s.drain(ctx)
		if err != nil {
			// The only error that reaches here is a canceled context
			return nil
		}

		select {
		case <-ctx.Done():
			return nil
		case <-s.nudge:
			// Hold the burst together rather than reading on the first insert
			// Whatever else lands in the window is picked up by the same read, and the nudges it raises are cleared at the top of the loop
			err = s.wait(ctx, s.nudgeWindow())
			if err != nil {
				return nil
			}
		case <-s.clock.After(s.flushInterval):
		}
	}
}

// drain ships every event currently available, then returns
// A store error is logged and swallowed: the next flush retries from the same cursor
func (s *Shipper) drain(ctx context.Context) error {
	// The backlog gauge is sampled once per flush, at the point where it is most meaningful: how far behind the cursor was when this flush started
	sampleBacklog := true

	for {
		pos, err := s.store.GetPosition(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			if errors.Is(err, ErrPositionNotFound) {
				// The caller is expected to bootstrap the cursor before starting the shipper
				// Shipping from a zero cursor instead would replay the entire retained history to the collector
				s.log.ErrorContext(ctx, "The audit stream cursor has not been bootstrapped, no events will be shipped")
				return nil
			}

			s.log.ErrorContext(ctx, "Failed to read the audit stream cursor", slog.Any("error", err))
			return nil
		}

		if sampleBacklog {
			sampleBacklog = false
			s.sampleBacklog(ctx, pos)
		}

		requested := s.batchSize
		events, err := s.store.ListForShipping(ctx, pos, requested)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			s.log.ErrorContext(ctx, "Failed to read audit events to ship", slog.Any("error", err))
			return nil
		}

		if len(events) == 0 {
			s.reportLag(ctx, pos)
			return nil
		}

		// The cursor tracks what has been considered, not what has been sent, so a filtered-out event advances it too
		// Otherwise a narrow filter would permanently pin the retention clamp on the caller's side
		next := events[len(events)-1].Position
		kept := s.applyFilter(events)

		if len(kept) > 0 {
			err = s.deliver(ctx, kept)
			switch {
			case errors.Is(err, errBatchTooLarge):
				// The batch size has been reduced: re-read a smaller batch from the same cursor
				continue
			case err != nil:
				return err
			}
		}

		ok, err := s.store.SetPosition(ctx, pos, next)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			s.log.ErrorContext(ctx, "Failed to advance the audit stream cursor", slog.Any("error", err))
			return nil
		}
		if !ok {
			// Another replica advanced the cursor first
			// Its batch overlapped with ours, which at-least-once delivery already accounts for
			// Re-read and continue from wherever it left off
			s.log.DebugContext(ctx, "Audit stream cursor was advanced concurrently, re-reading")
			continue
		}

		s.reportLag(ctx, next)

		// A short read means the table is drained
		if len(events) < requested {
			return nil
		}
	}
}

// applyFilter returns the events that pass the event-type filter, and counts the ones that do not
func (s *Shipper) applyFilter(events []Event) []Event {
	if s.filter.matchAll() {
		return events
	}

	kept := make([]Event, len(events))
	var i int
	for _, e := range events {
		if s.filter.matches(e.EventType) {
			kept[i] = e
			i++
		}
	}
	kept = kept[:i]

	s.recordEvents(OutcomeFiltered, int64(len(events)-len(kept)))

	return kept
}

// deliver POSTs a batch, retrying the same batch until it is accepted or ctx is canceled.
// It returns errBatchTooLarge when the batch size has been reduced and the caller should re-read, and a context error when the shipper is shutting down.
// It never returns after giving up on a batch: the cursor must not advance past an event the collector has not accepted.
func (s *Shipper) deliver(ctx context.Context, events []Event) (err error) {
	var (
		body        []byte
		contentType string
	)
	for {
		body, contentType, err = encodeBatch(events, s.format, s.source, s.instanceID, s.clock.Now())
		if err == nil {
			break
		}

		// Encoding cannot fail on data that came out of the store, since metadata is normalized while stamping
		// Treat it as a stall rather than a skip anyway: dropping an audit event is never the right recovery
		s.log.ErrorContext(ctx, "Failed to encode an audit event batch", slog.Any("error", err))
		err = s.wait(ctx, jitter(backoffCap))
		if err != nil {
			return err
		}
	}

	count := len(events)
	attempt := 0
	for {
		attempt++

		status, retryAfter, err := s.attempt(ctx, body, contentType, count)
		switch {
		case err == nil && status >= 200 && status <= 299:
			s.onSuccess(ctx, count, attempt)
			return nil

		case status == http.StatusRequestEntityTooLarge:
			s.recordBatch(OutcomeFailed)
			shrunk := s.shrinkBatch(ctx, count)
			if !shrunk {
				// The batch is already a single event, so re-reading would produce the same body
				// Pause at the ceiling so a collector that refuses every event cannot turn into a hot loop
				err = s.wait(ctx, jitter(backoffCap))
				if err != nil {
					return err
				}
			}

			return errBatchTooLarge

		case err != nil || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || (status >= 500 && status <= 599):
			s.failureStreak++
			s.recordBatch(OutcomeRetried)

			delay := s.retryDelay(attempt, retryAfter)
			s.log.WarnContext(ctx, "Failed to deliver an audit event batch, retrying",
				slog.Any("error", err),
				slog.Int("status", status),
				slog.Int("attempt", attempt),
				slog.Int("events", count),
				slog.Duration("retryIn", delay),
			)

			err = s.wait(ctx, delay)
			if err != nil {
				return err
			}

		default:
			// A persistent 4xx is an operator error: a bad token, the wrong path, an unsupported content type
			// The right behaviour is to stall loudly rather than to skip events, but the retry rate is pinned at the ceiling so a misconfigured endpoint cannot hot-loop
			s.failureStreak++
			s.recordBatch(OutcomeFailed)
			s.logStall(ctx, status, attempt, count)

			err = s.wait(ctx, jitter(backoffCap))
			if err != nil {
				return err
			}
		}
	}
}

// attempt performs a single delivery and returns the response status
func (s *Shipper) attempt(ctx context.Context, body []byte, contentType string, count int) (status int, retryAfter time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return 0, 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(s.hdrPrefix+"-Schema-Version", strconv.Itoa(SchemaVersion))
	req.Header.Set(s.hdrPrefix+"-Batch-Count", strconv.Itoa(count))
	if s.instanceID != "" {
		req.Header.Set(s.hdrPrefix+"-Instance", s.instanceID)
	}
	if s.userAgent != "" {
		req.Header.Set("User-Agent", s.userAgent)
	}
	if s.key != "" {
		// Sent verbatim, with no scheme prefix added
		req.Header.Set(s.authHeader, s.key)
	}

	res, err := s.client.Do(req)
	if err != nil {
		return 0, 0, err
	}

	// Drain and close the body so the connection can be reused, reading at most 1KB
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<10))
	_ = res.Body.Close()

	return res.StatusCode, parseRetryAfter(res.Header.Get("Retry-After")), nil
}

// onSuccess records a delivered batch and logs the recovery when it ends a failure streak
func (s *Shipper) onSuccess(ctx context.Context, count int, attempt int) {
	s.recordBatch(OutcomeSent)
	s.recordEvents(OutcomeSent, int64(count))

	if s.failureStreak > 0 {
		s.log.InfoContext(ctx, "Audit event delivery recovered",
			slog.Int("failedAttempts", s.failureStreak),
			slog.Int("events", count),
		)
	} else {
		s.log.DebugContext(ctx, "Delivered an audit event batch",
			slog.Int("events", count),
			slog.Int("attempt", attempt),
		)
	}

	s.failureStreak = 0
	s.stalledSince = time.Time{}
}

// shrinkBatch halves the batch size, down to a floor of 1, and reports whether it could.
// The size only ever shrinks: growing it back would rediscover the cap on every batch.
func (s *Shipper) shrinkBatch(ctx context.Context, count int) bool {
	if s.batchSize <= MinBatchSize {
		s.log.ErrorContext(ctx, "The collector rejected a single audit event as too large, the feed is stalled",
			slog.Int("events", count),
		)
		return false
	}

	s.batchSize = max(s.batchSize/2, MinBatchSize)
	s.log.WarnContext(ctx, "The collector rejected an audit event batch as too large, reducing the batch size",
		slog.Int("events", count),
		slog.Int("batchSize", s.batchSize),
	)

	return true
}

// logStall logs a non-retryable response, escalating to error level once it has persisted for a while
func (s *Shipper) logStall(ctx context.Context, status int, attempt int, count int) {
	now := s.clock.Now()
	if s.stalledSince.IsZero() {
		s.stalledSince = now
	}

	stalledFor := now.Sub(s.stalledSince)
	attrs := []any{
		slog.Int("status", status),
		slog.Int("attempt", attempt),
		slog.Int("events", count),
		slog.Duration("stalledFor", stalledFor),
	}

	if stalledFor >= stallLogThreshold {
		s.log.ErrorContext(ctx, "The collector keeps rejecting audit event batches, the feed is stalled and no events are being dropped", attrs...)
		return
	}

	s.log.WarnContext(ctx, "The collector rejected an audit event batch", attrs...)
}

// sampleBacklog updates the backlog gauge with how many events sit past the cursor
func (s *Shipper) sampleBacklog(ctx context.Context, pos Position) {
	count, err := s.store.CountPending(ctx, pos)
	if err != nil {
		if ctx.Err() == nil {
			s.log.DebugContext(ctx, "Failed to count pending audit events", slog.Any("error", err))
		}
		return
	}

	s.recordBacklog(count)
}

// reportLag updates the lag gauge and warns when the cursor has fallen far behind
func (s *Shipper) reportLag(ctx context.Context, pos Position) {
	if pos.EventCreatedAt <= 0 {
		// The cursor was bootstrapped against an empty table and nothing has shipped yet
		s.recordLag(0)
		return
	}

	lag := max(s.clock.Now().Sub(time.Unix(pos.EventCreatedAt, 0)), 0)
	s.recordLag(lag.Seconds())

	if lag >= lagWarnThreshold {
		s.log.WarnContext(ctx, "The audit event stream is falling behind",
			slog.Duration("lag", lag),
		)
	}
}

// retryDelay returns how long to wait before the next attempt
func (s *Shipper) retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	// Honour Retry-After when the collector sent a sane value
	if retryAfter > 0 {
		return min(retryAfter, backoffCap)
	}

	return jitter(backoffFor(attempt))
}

// wait blocks for d, or until ctx is canceled
func (s *Shipper) wait(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.clock.After(d):
		return nil
	}
}

// backoffFor returns the un-jittered backoff for an attempt: 1s, 2s, 4s … capped at 5m
func backoffFor(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 30 {
		return backoffCap
	}

	d := backoffBase << (attempt - 1)
	if d > backoffCap || d <= 0 {
		return backoffCap
	}

	return d
}

// jitter spreads a delay over [d/2, d] so a fleet of replicas hitting the same collector does not retry in lockstep
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}

	half := d / 2

	//nolint:gosec // G404: spreading out retries does not need cryptographic randomness
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// parseRetryAfter reads a Retry-After header, accepting both the delay-seconds and the HTTP-date form
// Values that are missing, unparseable or out of range return 0, which means "use the backoff schedule"
func parseRetryAfter(val string) time.Duration {
	if val == "" {
		return 0
	}

	seconds, err := strconv.Atoi(val)
	if err == nil {
		if seconds < 1 {
			return 0
		}
		return min(time.Duration(seconds)*time.Second, backoffCap)
	}

	at, err := http.ParseTime(val)
	if err != nil {
		return 0
	}

	d := time.Until(at)
	if d < time.Second {
		return 0
	}

	return min(d, backoffCap)
}
