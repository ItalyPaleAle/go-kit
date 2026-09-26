package siem

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clocktesting "k8s.io/utils/clock/testing"
)

// fakeStore is an in-memory Store that behaves like the real one: events sort by Position.Seq and the cursor advances by compare-and-swap
type fakeStore struct {
	mu sync.Mutex

	events []Event
	pos    Position
	hasPos bool

	// loseCAS makes the next N SetPosition calls report a lost race, as a concurrent replica would
	loseCAS int

	listErr error
	setErr  error

	listCalls int
	setCalls  int
}

func newFakeStore(events ...Event) *fakeStore {
	return &fakeStore{
		events: events,
		pos:    Position{V: PositionVersion},
		hasPos: true,
	}
}

func (s *fakeStore) GetPosition(_ context.Context) (Position, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.hasPos {
		return Position{}, ErrPositionNotFound
	}

	return s.pos, nil
}

func (s *fakeStore) SetPosition(_ context.Context, prev Position, next Position) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.setCalls++
	if s.setErr != nil {
		return false, s.setErr
	}

	if s.loseCAS > 0 {
		s.loseCAS--
		return false, nil
	}

	if s.pos != prev {
		return false, nil
	}

	s.pos = next

	return true, nil
}

func (s *fakeStore) ListForShipping(_ context.Context, pos Position, limit int) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.listCalls++
	if s.listErr != nil {
		return nil, s.listErr
	}

	out := make([]Event, 0, limit)
	for _, e := range s.events {
		if e.Position.Seq <= pos.Seq {
			continue
		}
		out = append(out, e)
		if len(out) == limit {
			break
		}
	}

	return out, nil
}

func (s *fakeStore) CountPending(_ context.Context, pos Position) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var count int64
	for _, e := range s.events {
		if e.Position.Seq > pos.Seq {
			count++
		}
	}

	return count, nil
}

func (s *fakeStore) position() Position {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.pos
}

// add appends an event, as a write to the audit table would
func (s *fakeStore) add(events ...Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.events = append(s.events, events...)
}

// reads returns how many times the shipper has gone to the store for events
func (s *fakeStore) reads() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.listCalls
}

// seqEvent builds a test event whose shipping key is seq
func seqEvent(seq int64, eventType string) Event {
	return Event{
		ID:         "0199" + strconv.FormatInt(seq, 10),
		Time:       time.Date(2026, 9, 15, 20, 11, 3, 0, time.UTC),
		EventType:  eventType,
		Outcome:    "success",
		AuthMethod: "session",
		Position: Position{
			V:              PositionVersion,
			Seq:            seq,
			EventID:        "0199" + strconv.FormatInt(seq, 10),
			EventCreatedAt: time.Date(2026, 9, 15, 20, 11, 3, 0, time.UTC).Unix(),
		},
	}
}

// capturedRequest is one delivery observed by the test collector
type capturedRequest struct {
	header http.Header
	body   string
}

// testCollector is an httptest.Server that records every delivery and replies with the statuses the test queued up
type testCollector struct {
	*httptest.Server

	mu       sync.Mutex
	requests []capturedRequest
	statuses []int
	headers  []http.Header
	received chan struct{}
}

func newTestCollector(t *testing.T, statuses ...int) *testCollector {
	t.Helper()

	c := &testCollector{
		statuses: statuses,
		received: make(chan struct{}, 128),
	}

	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		c.mu.Lock()
		c.requests = append(c.requests, capturedRequest{header: r.Header.Clone(), body: string(body)})
		status := http.StatusOK
		if len(c.statuses) > 0 {
			status = c.statuses[0]
			c.statuses = c.statuses[1:]
		}
		extra := http.Header{}
		if len(c.headers) > 0 {
			extra = c.headers[0]
			c.headers = c.headers[1:]
		}
		c.mu.Unlock()

		for k, vals := range extra {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(status)

		select {
		case c.received <- struct{}{}:
		default:
		}
	}))

	t.Cleanup(c.Close)

	return c
}

func (c *testCollector) all() []capturedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]capturedRequest(nil), c.requests...)
}

func (c *testCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.requests)
}

// newTestShipper builds a Shipper pointed at url, with private IPs allowed since httptest listens on loopback
func newTestShipper(t *testing.T, store Store, url string, mutate func(*ShipperOptions)) (*Shipper, *clocktesting.FakeClock) {
	t.Helper()

	clock := clocktesting.NewFakeClock(time.Date(2026, 9, 15, 20, 12, 0, 0, time.UTC))
	opts := ShipperOptions{
		Store:           store,
		URL:             url,
		Source:          "testapp",
		InstanceID:      "inst-1",
		AllowPrivateIPs: true,
		BatchSize:       10,
		UserAgent:       "testapp/test",
		HeaderPrefix:    "X-Testapp",
		Logger:          slog.New(slog.DiscardHandler),
		clock:           clock,
	}
	if mutate != nil {
		mutate(&opts)
	}

	s, err := NewShipper(opts)
	require.NoError(t, err)

	return s, clock
}

// autoAdvance steps the fake clock whenever the shipper is waiting, so retry backoffs do not slow the test down
func autoAdvance(t *testing.T, clock *clocktesting.FakeClock) {
	t.Helper()

	done := make(chan struct{})
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if clock.HasWaiters() {
					clock.Step(10 * time.Minute)
				}
			}
		}
	}()

	t.Cleanup(func() {
		close(done)
		<-stopped
	})
}

func TestNewShipperValidation(t *testing.T) {
	base := func() ShipperOptions {
		return ShipperOptions{
			Store:  newFakeStore(),
			URL:    "https://collector.example.com/ingest",
			Source: "testapp",
			Logger: slog.New(slog.DiscardHandler),
		}
	}

	t.Run("requires a store", func(t *testing.T) {
		opts := base()
		opts.Store = nil

		_, err := NewShipper(opts)

		require.ErrorContains(t, err, "store is required")
	})

	t.Run("requires a source", func(t *testing.T) {
		opts := base()
		opts.Source = ""

		_, err := NewShipper(opts)

		require.ErrorContains(t, err, "source is required")
	})

	t.Run("requires a URL", func(t *testing.T) {
		opts := base()
		opts.URL = ""

		_, err := NewShipper(opts)

		require.ErrorContains(t, err, "URL is required")
	})

	t.Run("rejects a non-HTTP scheme", func(t *testing.T) {
		opts := base()
		opts.URL = "ftp://collector.example.com"

		_, err := NewShipper(opts)

		require.ErrorContains(t, err, "disallowed scheme")
	})

	t.Run("rejects an out-of-range batch size", func(t *testing.T) {
		opts := base()
		opts.BatchSize = MaxBatchSize + 1

		_, err := NewShipper(opts)

		require.ErrorContains(t, err, "out of range")
	})

	t.Run("rejects an out-of-range flush interval", func(t *testing.T) {
		opts := base()
		opts.FlushInterval = MaxFlushInterval + time.Second

		_, err := NewShipper(opts)

		require.ErrorContains(t, err, "out of range")
	})

	t.Run("rejects an invalid event type filter", func(t *testing.T) {
		opts := base()
		opts.EventTypes = []string{"nope"}

		_, err := NewShipper(opts)

		require.ErrorContains(t, err, "invalid event type filter")
	})

	t.Run("applies the defaults", func(t *testing.T) {
		s, err := NewShipper(base())

		require.NoError(t, err)
		assert.Equal(t, FormatNDJSON, s.format)
		assert.Equal(t, DefaultBatchSize, s.batchSize)
		assert.Equal(t, DefaultFlushInterval, s.flushInterval)
		assert.Equal(t, DefaultAuthHeader, s.authHeader)
		assert.Equal(t, DefaultHeaderPrefix, s.hdrPrefix)
	})
}

func TestShipperDeliversAndAdvancesTheCursor(t *testing.T) {
	collector := newTestCollector(t)
	store := newFakeStore(seqEvent(1, "order.create"), seqEvent(2, "order.confirm"))

	s, _ := newTestShipper(t, store, collector.URL, func(o *ShipperOptions) {
		o.Key = "Splunk 00000000-0000-0000-0000-000000000000"
		o.AuthorizationHeader = "Authorization"
	})

	err := s.drain(t.Context())
	require.NoError(t, err)

	requests := collector.all()
	require.Len(t, requests, 1)

	// Both events went out in a single NDJSON body, newline-terminated
	lines := strings.Split(strings.TrimSuffix(requests[0].body, "\n"), "\n")
	require.Len(t, lines, 2)

	assert.Equal(t, ndjsonContentType, requests[0].header.Get("Content-Type")) //nolint:testifylint // comparing a content type, not JSON
	assert.Equal(t, "testapp/test", requests[0].header.Get("User-Agent"))
	assert.Equal(t, "inst-1", requests[0].header.Get("X-Testapp-Instance"))
	assert.Equal(t, "1", requests[0].header.Get("X-Testapp-Schema-Version"))
	assert.Equal(t, "2", requests[0].header.Get("X-Testapp-Batch-Count"))

	// The key is sent verbatim, with no scheme prefix added
	assert.Equal(t, "Splunk 00000000-0000-0000-0000-000000000000", requests[0].header.Get("Authorization"))

	assert.Equal(t, int64(2), store.position().Seq)
}

func TestShipperSupportsACustomAuthHeader(t *testing.T) {
	collector := newTestCollector(t)
	store := newFakeStore(seqEvent(1, "order.create"))

	s, _ := newTestShipper(t, store, collector.URL, func(o *ShipperOptions) {
		o.Key = "abc123"
		o.AuthorizationHeader = "X-Splunk-Token"
	})

	err := s.drain(t.Context())
	require.NoError(t, err)

	requests := collector.all()
	require.Len(t, requests, 1)
	assert.Equal(t, "abc123", requests[0].header.Get("X-Splunk-Token"))
	assert.Empty(t, requests[0].header.Get("Authorization"))
}

func TestShipperSendsAJSONEnvelope(t *testing.T) {
	collector := newTestCollector(t)
	store := newFakeStore(seqEvent(1, "order.create"), seqEvent(2, "order.confirm"))

	s, _ := newTestShipper(t, store, collector.URL, func(o *ShipperOptions) {
		o.Format = FormatJSON
	})

	err := s.drain(t.Context())
	require.NoError(t, err)

	requests := collector.all()
	require.Len(t, requests, 1)
	assert.Equal(t, jsonContentType, requests[0].header.Get("Content-Type")) //nolint:testifylint // comparing a content type, not JSON

	var decoded envelope
	err = json.Unmarshal([]byte(requests[0].body), &decoded)
	require.NoError(t, err)
	assert.Equal(t, 2, decoded.Count)
	require.Len(t, decoded.Events, 2)
}

func TestShipperDrainsInSuccessiveBatches(t *testing.T) {
	collector := newTestCollector(t)
	store := newFakeStore(seqEvent(1, "order.create"), seqEvent(2, "order.confirm"), seqEvent(3, "order.cancel"))

	s, _ := newTestShipper(t, store, collector.URL, func(o *ShipperOptions) {
		o.BatchSize = 2
	})

	err := s.drain(t.Context())
	require.NoError(t, err)

	requests := collector.all()
	require.Len(t, requests, 2)
	assert.Equal(t, int64(3), store.position().Seq)
}

func TestShipperRetriesTheSameBatchOnAServerError(t *testing.T) {
	collector := newTestCollector(t, http.StatusInternalServerError, http.StatusOK)
	store := newFakeStore(seqEvent(1, "order.create"))

	s, clock := newTestShipper(t, store, collector.URL, nil)
	autoAdvance(t, clock)

	err := s.drain(t.Context())
	require.NoError(t, err)

	requests := collector.all()
	require.Len(t, requests, 2)

	// At-least-once: the identical batch is re-sent, and the collector deduplicates on the event id
	assert.Equal(t, requests[0].body, requests[1].body)
	assert.Equal(t, int64(1), store.position().Seq)
}

func TestShipperHonoursRetryAfter(t *testing.T) {
	collector := newTestCollector(t, http.StatusTooManyRequests, http.StatusOK)
	collector.headers = []http.Header{{"Retry-After": []string{"30"}}}

	store := newFakeStore(seqEvent(1, "order.create"))
	s, clock := newTestShipper(t, store, collector.URL, nil)
	autoAdvance(t, clock)

	err := s.drain(t.Context())

	require.NoError(t, err)
	assert.Equal(t, 2, collector.count())
	assert.Equal(t, int64(1), store.position().Seq)
}

func TestShipperStallsOnANonRetryableStatus(t *testing.T) {
	// A persistent 401 is an operator error: the feed must stall loudly rather than skip the batch
	collector := newTestCollector(t)
	collector.statuses = []int{http.StatusUnauthorized, http.StatusUnauthorized, http.StatusUnauthorized, http.StatusUnauthorized}

	store := newFakeStore(seqEvent(1, "order.create"))
	s, clock := newTestShipper(t, store, collector.URL, nil)
	autoAdvance(t, clock)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Stop the loop once we have seen it retry a few times
	go func() {
		for range 3 {
			select {
			case <-collector.received:
			case <-time.After(10 * time.Second):
			}
		}
		cancel()
	}()

	err := s.drain(ctx)

	require.ErrorIs(t, err, context.Canceled)
	assert.GreaterOrEqual(t, collector.count(), 3)

	// The cursor never moved past the batch the collector refused
	assert.Equal(t, int64(0), store.position().Seq)
}

func TestShipperHalvesTheBatchOnPayloadTooLarge(t *testing.T) {
	collector := newTestCollector(t, http.StatusRequestEntityTooLarge, http.StatusOK, http.StatusOK)
	store := newFakeStore(seqEvent(1, "order.create"), seqEvent(2, "order.confirm"), seqEvent(3, "order.cancel"), seqEvent(4, "order.expire"))

	s, clock := newTestShipper(t, store, collector.URL, func(o *ShipperOptions) {
		o.BatchSize = 4
	})
	autoAdvance(t, clock)

	err := s.drain(t.Context())
	require.NoError(t, err)

	assert.Equal(t, 2, s.batchSize)

	requests := collector.all()
	require.Len(t, requests, 3)
	assert.Equal(t, "4", requests[0].header.Get("X-Testapp-Batch-Count"))
	assert.Equal(t, "2", requests[1].header.Get("X-Testapp-Batch-Count"))
	assert.Equal(t, "2", requests[2].header.Get("X-Testapp-Batch-Count"))
	assert.Equal(t, int64(4), store.position().Seq)
}

func TestShipperAdvancesThePositionForFilteredEvents(t *testing.T) {
	collector := newTestCollector(t)
	store := newFakeStore(seqEvent(1, "account.login"), seqEvent(2, "order.confirm"))

	s, _ := newTestShipper(t, store, collector.URL, func(o *ShipperOptions) {
		o.EventTypes = []string{"order.*"}
	})

	err := s.drain(t.Context())
	require.NoError(t, err)

	requests := collector.all()
	require.Len(t, requests, 1)
	assert.Equal(t, "1", requests[0].header.Get("X-Testapp-Batch-Count"))
	assert.Contains(t, requests[0].body, "order.confirm")
	assert.NotContains(t, requests[0].body, "account.login")

	// The cursor tracks what was considered, not what was sent: a narrow filter must not pin it in place
	assert.Equal(t, int64(2), store.position().Seq)
}

func TestShipperSendsNothingWhenEverythingIsFiltered(t *testing.T) {
	collector := newTestCollector(t)
	store := newFakeStore(seqEvent(1, "account.login"))

	s, _ := newTestShipper(t, store, collector.URL, func(o *ShipperOptions) {
		o.EventTypes = []string{"order.*"}
	})

	err := s.drain(t.Context())
	require.NoError(t, err)

	assert.Equal(t, 0, collector.count())
	assert.Equal(t, int64(1), store.position().Seq)
}

func TestShipperRereadsAfterALostCompareAndSwap(t *testing.T) {
	collector := newTestCollector(t)
	store := newFakeStore(seqEvent(1, "order.create"))
	store.loseCAS = 1

	s, _ := newTestShipper(t, store, collector.URL, nil)

	err := s.drain(t.Context())
	require.NoError(t, err)

	// The batch was delivered twice, which at-least-once delivery accounts for, and the cursor still ends up correct
	assert.Equal(t, 2, collector.count())
	assert.Equal(t, int64(1), store.position().Seq)
	assert.Equal(t, 2, store.setCalls)
}

func TestShipperStopsWhenTheCursorIsMissing(t *testing.T) {
	collector := newTestCollector(t)
	store := newFakeStore(seqEvent(1, "order.create"))
	store.hasPos = false

	s, _ := newTestShipper(t, store, collector.URL, nil)

	err := s.drain(t.Context())

	require.NoError(t, err)
	assert.Equal(t, 0, collector.count())
}

func TestShipperNudgeNeverBlocks(t *testing.T) {
	collector := newTestCollector(t)
	s, _ := newTestShipper(t, newFakeStore(), collector.URL, nil)

	for range 10 {
		s.Nudge()
	}

	assert.Len(t, s.nudge, 1)
}

func TestShipperRunStopsOnContextCancellation(t *testing.T) {
	collector := newTestCollector(t)
	store := newFakeStore(seqEvent(1, "order.create"))

	s, _ := newTestShipper(t, store, collector.URL, nil)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- s.Run(ctx)
	}()

	require.Eventually(t, func() bool {
		return store.position().Seq == 1
	}, 10*time.Second, 5*time.Millisecond, "timed out waiting for the first delivery")

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for Run to return")
	}
}

func TestBackoffSchedule(t *testing.T) {
	assert.Equal(t, time.Second, backoffFor(1))
	assert.Equal(t, 2*time.Second, backoffFor(2))
	assert.Equal(t, 4*time.Second, backoffFor(3))
	assert.Equal(t, 8*time.Second, backoffFor(4))

	// The schedule saturates at the ceiling and never overflows
	assert.Equal(t, backoffCap, backoffFor(20))
	assert.Equal(t, backoffCap, backoffFor(200))
	assert.Equal(t, time.Second, backoffFor(0))
}

func TestJitterStaysWithinBounds(t *testing.T) {
	for range 200 {
		d := jitter(8 * time.Second)
		assert.GreaterOrEqual(t, d, 4*time.Second)
		assert.LessOrEqual(t, d, 8*time.Second)
	}

	assert.Equal(t, time.Duration(0), jitter(0))
}

func TestParseRetryAfter(t *testing.T) {
	assert.Equal(t, 30*time.Second, parseRetryAfter("30"))
	assert.Equal(t, backoffCap, parseRetryAfter("100000"))
	assert.Equal(t, time.Duration(0), parseRetryAfter(""))
	assert.Equal(t, time.Duration(0), parseRetryAfter("0"))
	assert.Equal(t, time.Duration(0), parseRetryAfter("-5"))
	assert.Equal(t, time.Duration(0), parseRetryAfter("soon"))

	// The HTTP-date form is accepted too
	d := parseRetryAfter(time.Now().Add(45 * time.Second).UTC().Format(http.TimeFormat))
	assert.Greater(t, d, 30*time.Second)
	assert.LessOrEqual(t, d, 45*time.Second)
}

// recordingMetrics captures every signal the shipper emits
type recordingMetrics struct {
	mu sync.Mutex

	events  map[string]int64
	batches map[string]int
	lag     float64
	backlog int64
}

func newRecordingMetrics() *recordingMetrics {
	return &recordingMetrics{
		events:  map[string]int64{},
		batches: map[string]int{},
	}
}

func (m *recordingMetrics) RecordEvents(outcome string, count int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.events[outcome] += count
}

func (m *recordingMetrics) RecordBatch(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.batches[outcome]++
}

func (m *recordingMetrics) RecordLag(seconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lag = seconds
}

func (m *recordingMetrics) RecordBacklog(count int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.backlog = count
}

func (m *recordingMetrics) snapshot() recordingMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()

	return recordingMetrics{
		events:  map[string]int64{OutcomeSent: m.events[OutcomeSent], OutcomeFiltered: m.events[OutcomeFiltered]},
		batches: map[string]int{OutcomeSent: m.batches[OutcomeSent], OutcomeRetried: m.batches[OutcomeRetried], OutcomeFailed: m.batches[OutcomeFailed]},
		lag:     m.lag,
		backlog: m.backlog,
	}
}

func TestShipperRecordsMetrics(t *testing.T) {
	collector := newTestCollector(t, http.StatusInternalServerError, http.StatusOK)
	store := newFakeStore(seqEvent(1, "account.login"), seqEvent(2, "order.confirm"))
	recorder := newRecordingMetrics()

	s, clock := newTestShipper(t, store, collector.URL, func(o *ShipperOptions) {
		o.EventTypes = []string{"order.*"}
		o.Metrics = recorder
	})
	autoAdvance(t, clock)

	err := s.drain(t.Context())
	require.NoError(t, err)

	got := recorder.snapshot()
	assert.Equal(t, int64(1), got.events[OutcomeSent])
	assert.Equal(t, int64(1), got.events[OutcomeFiltered])
	assert.Equal(t, 1, got.batches[OutcomeSent])
	assert.Equal(t, 1, got.batches[OutcomeRetried])
	assert.Equal(t, 0, got.batches[OutcomeFailed])

	// The backlog is sampled once, before anything is shipped
	assert.Equal(t, int64(2), got.backlog)

	// The lag is measured against the last event the shipper considered
	// The exact value moves with the retry the fake clock steps through, so only the sign matters here
	assert.Positive(t, got.lag)
}

func TestShipperRefreshesMetricsDuringAnOutage(t *testing.T) {
	// The collector never accepts the batch, so the cursor stays put
	// Lag and backlog must keep moving anyway, or they would hide the outage they are meant to reveal
	collector := newTestCollector(t, slices.Repeat([]int{http.StatusServiceUnavailable}, 1000)...)
	recorder := newRecordingMetrics()

	// Event 1 was shipped before the outage started, 57 seconds before the fake clock's starting time
	store := newFakeStore(seqEvent(1, "order.create"), seqEvent(2, "order.confirm"))
	store.pos = seqEvent(1, "order.create").Position

	s, clock := newTestShipper(t, store, collector.URL, func(o *ShipperOptions) {
		o.Metrics = recorder
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	drained := make(chan error, 1)
	go func() {
		drained <- s.drain(ctx)
	}()

	// Insert another event once the first delivery has failed, then let the retries run
	select {
	case <-collector.received:
	case <-time.After(5 * time.Second):
		t.Fatal("the shipper never contacted the collector")
	}
	store.add(seqEvent(3, "order.cancel"))
	autoAdvance(t, clock)

	// Every retry advances the clock by 10 minutes
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		got := recorder.snapshot()
		assert.Equal(c, int64(2), got.backlog)
		assert.Greater(c, got.lag, time.Hour.Seconds())
	}, 10*time.Second, 10*time.Millisecond)

	cancel()
	err := <-drained
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int64(1), store.position().Seq)
}

func TestShipperToleratesNilMetrics(t *testing.T) {
	collector := newTestCollector(t)
	store := newFakeStore(seqEvent(1, "order.confirm"))

	s, _ := newTestShipper(t, store, collector.URL, func(o *ShipperOptions) {
		o.Metrics = nil
	})

	err := s.drain(t.Context())

	require.NoError(t, err)
	assert.Equal(t, 1, collector.count())
}

func TestShipperHonoursThePrivateIPPolicy(t *testing.T) {
	// A collector is usually an internal host, so this option decides whether the feed works at all
	// Inverting it would be a security bug in one direction and an outage in the other, so check the wiring in both
	collector := newTestCollector(t)

	t.Run("refuses a private collector by default", func(t *testing.T) {
		s, _ := newTestShipper(t, newFakeStore(), collector.URL, func(o *ShipperOptions) {
			o.AllowPrivateIPs = false
		})

		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.url, nil)
		require.NoError(t, err)

		res, err := s.client.Do(req)
		if res != nil {
			_ = res.Body.Close()
		}

		require.Error(t, err)
		require.ErrorContains(t, err, "refusing to dial private/internal IP")
	})

	t.Run("reaches a private collector when enabled", func(t *testing.T) {
		s, _ := newTestShipper(t, newFakeStore(), collector.URL, func(o *ShipperOptions) {
			o.AllowPrivateIPs = true
		})

		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.url, nil)
		require.NoError(t, err)

		res, err := s.client.Do(req)
		require.NoError(t, err)
		_ = res.Body.Close()

		assert.Equal(t, http.StatusOK, res.StatusCode)
	})
}

func TestShipperCoalescesABurstOfNudges(t *testing.T) {
	collector := newTestCollector(t)
	store := newFakeStore()
	s, clock := newTestShipper(t, store, collector.URL, nil)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Run(ctx)
	}()

	// Let the first, empty read happen so the loop is parked on its select
	require.Eventually(t, func() bool {
		return store.reads() > 0
	}, 10*time.Second, 5*time.Millisecond, "timed out waiting for the first read")

	readsBeforeNudge := store.reads()

	// The first event of a burst
	store.add(seqEvent(1, "order.create"))
	s.Nudge()

	// Acting on it straight away would ship it alone, and send the rest of the burst one request at a time
	require.Never(t, func() bool {
		return store.reads() > readsBeforeNudge
	}, 100*time.Millisecond, 10*time.Millisecond, "the shipper read the store before the batch window closed")

	// The rest of the burst lands while the window is open
	store.add(seqEvent(2, "order.confirm"), seqEvent(3, "order.cancel"))

	// Closing the window ships all three together
	// The clock is stepped repeatedly because the loop registers its wait asynchronously
	require.Eventually(t, func() bool {
		clock.Step(nudgeBatchWindow)
		return collector.count() > 0
	}, 10*time.Second, 10*time.Millisecond, "timed out waiting for the batch")

	cancel()
	<-done

	requests := collector.all()
	require.Len(t, requests, 1)
	assert.Equal(t, "3", requests[0].header.Get("X-Testapp-Batch-Count"))
	assert.Equal(t, int64(3), store.position().Seq)
}

func TestShipperNudgeWindowNeverExceedsTheFlushInterval(t *testing.T) {
	collector := newTestCollector(t)

	t.Run("uses the batch window when the flush interval is longer", func(t *testing.T) {
		s, _ := newTestShipper(t, newFakeStore(), collector.URL, func(o *ShipperOptions) {
			o.FlushInterval = time.Minute
		})

		assert.Equal(t, nudgeBatchWindow, s.nudgeWindow())
	})

	t.Run("clamps to the flush interval when that is shorter", func(t *testing.T) {
		// Holding a nudge longer than the regular poll would make it worse than no nudge at all
		s, _ := newTestShipper(t, newFakeStore(), collector.URL, func(o *ShipperOptions) {
			o.FlushInterval = time.Second
		})

		assert.Equal(t, time.Second, s.nudgeWindow())
	})
}
