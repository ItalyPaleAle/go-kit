package siem

// Metrics receives the shipper's observability signals
// Implementations must be safe for concurrent use
// A nil Metrics is valid and disables reporting
type Metrics interface {
	// RecordEvents records that count events reached the given outcome, one of OutcomeSent or OutcomeFiltered
	RecordEvents(outcome string, count int64)

	// RecordBatch records the outcome of a batch delivery, one of OutcomeSent, OutcomeRetried or OutcomeFailed
	RecordBatch(outcome string)

	// RecordLag records how many seconds elapsed between the creation of the last event the shipper considered and now
	// This is the signal to alert on: it covers collector outages, misconfiguration and a stalled shipper goroutine at once
	RecordLag(seconds float64)

	// RecordBacklog records how many events are still waiting past the cursor
	RecordBacklog(count int64)
}

// recordEvents reports an event count, tolerating a nil Metrics
func (s *Shipper) recordEvents(outcome string, count int64) {
	if s.metrics == nil || count == 0 {
		return
	}

	s.metrics.RecordEvents(outcome, count)
}

// recordBatch reports a batch outcome, tolerating a nil Metrics
func (s *Shipper) recordBatch(outcome string) {
	if s.metrics == nil {
		return
	}

	s.metrics.RecordBatch(outcome)
}

// recordLag reports the current lag, tolerating a nil Metrics
func (s *Shipper) recordLag(seconds float64) {
	if s.metrics == nil {
		return
	}

	s.metrics.RecordLag(seconds)
}

// recordBacklog reports the current backlog, tolerating a nil Metrics
func (s *Shipper) recordBacklog(count int64) {
	if s.metrics == nil {
		return
	}

	s.metrics.RecordBacklog(count)
}
