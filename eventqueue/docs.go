// Package eventqueue implements a queue processor for delayed events.
// Events are maintained in an in-memory queue, where items are in the order of when they are to be executed.
// Users should interact with the Processor to process events in the queue.
// When the queue has at least 1 item, the processor uses a single background goroutine to wait on the next item to be executed.
package eventqueue
