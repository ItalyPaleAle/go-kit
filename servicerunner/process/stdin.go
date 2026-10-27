package process

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
)

const stdinBufferSize = 32 << 10 // 32KB

// stdinPump reads the stream set with WithStdin on behalf of every run of the process
// A single goroutine owns all reads from the stream, so a run that exits doesn't leave behind a pending read that would race with the next run for the data
type stdinPump struct {
	// Chunks of data read from the stream
	// Closed when the stream ends
	data chan []byte
	// Closed when the service returns, to stop the reader goroutine
	stop chan struct{}
	// Data the previous run didn't receive before exiting, which the next run receives first
	// Only feed accesses it, and runs never feed at the same time
	carry []byte
}

func newStdinPump(r io.Reader, log *slog.Logger) *stdinPump {
	p := &stdinPump{
		data: make(chan []byte),
		stop: make(chan struct{}),
	}

	go p.read(r, log)

	return p
}

func (p *stdinPump) close() {
	close(p.stop)
}

func (p *stdinPump) read(r io.Reader, log *slog.Logger) {
	defer close(p.data)

	buf := make([]byte, stdinBufferSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			// The buffer is reused for the next read, so each chunk needs its own copy
			select {
			case p.data <- bytes.Clone(buf[:n]):
			case <-p.stop:
				return
			}
		}

		if errors.Is(err, io.EOF) {
			return
		} else if err != nil {
			log.Warn("Failed to read the standard input stream for the process", slog.Any("error", err))
			return
		}
	}
}

// feed writes data from the stream to the standard input of a single run of the process, until the stream ends or the process exits
// It closes w when done, so the process sees the end of its input when the stream ends
func (p *stdinPump) feed(w io.WriteCloser, exited <-chan struct{}) {
	defer w.Close() //nolint:errcheck

	for {
		chunk := p.carry
		p.carry = nil
		if len(chunk) == 0 {
			var ok bool
			select {
			case chunk, ok = <-p.data:
				if !ok {
					return
				}
			case <-exited:
				return
			}
		}

		n, err := w.Write(chunk)
		if err != nil {
			// The process exited or closed its standard input before receiving the whole chunk, so the rest goes to the next run
			p.carry = chunk[n:]
			return
		}
	}
}
