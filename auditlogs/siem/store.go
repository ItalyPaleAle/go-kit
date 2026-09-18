package siem

import (
	"context"
	"errors"
)

// ErrPositionNotFound is returned by Store.GetPosition when the cursor has never been written
// Absence of the cursor is the "not yet bootstrapped" sentinel: unlike a zero-valued cursor, it is unambiguous even when the audit table was empty at first enable
var ErrPositionNotFound = errors.New("audit stream cursor not found")

// Store is the application-provided backing store the Shipper reads events from and persists its cursor to
// Implementations must only return events whose transaction has settled, so that no event can ever appear behind the cursor after it has advanced past it
type Store interface {
	// GetPosition returns the current cursor
	// It returns ErrPositionNotFound when the cursor has not been bootstrapped yet
	GetPosition(ctx context.Context) (Position, error)

	// SetPosition advances the cursor with a compare-and-swap against prev
	// It returns false, with no error, when the stored cursor no longer matches prev: the caller then re-reads the cursor and continues from there
	SetPosition(ctx context.Context, prev Position, next Position) (bool, error)

	// ListForShipping returns up to limit settled events that sort after pos, in shipping-key order
	// Every returned event must carry its own Position
	ListForShipping(ctx context.Context, pos Position, limit int) ([]Event, error)

	// CountPending returns how many settled events sort after pos
	// It backs the backlog gauge and is called at most once per flush
	CountPending(ctx context.Context, pos Position) (int64, error)
}
