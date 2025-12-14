// Package fsnotify watches for changes to files or folders in the filesystem and sends a message on a channel.
// It batches updates happening within 500ms to avoid sending multiple notifications when several file operations occur in quick succession
// Only file creation and write events trigger notifications.
package fsnotify

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatchFolder returns a channel that receives a notification when a file is changed in a folder.
func WatchFolder(ctx context.Context, folder string) (<-chan struct{}, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create watcher: %w", err)
	}

	msgChan := make(chan struct{}, 1)
	batcher := make(chan struct{}, 1)
	var wg sync.WaitGroup

	// Watch for FS events in background
	go func() {
		defer watcher.Close() //nolint:errcheck
		defer func() {
			wg.Wait()
			close(msgChan)
		}()

		for {
			select {
			case <-ctx.Done():
				// Stop the watcher on context cancellation
				return

			case event := <-watcher.Events:
				// Only listen to events where a file is created (included renamed files) or written to
				if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) {
					continue
				}

				// Batch changes so we don't send notifications when multiple operations are happening at once
				select {
				case batcher <- struct{}{}:
					wg.Go(func() {
						select {
						case <-time.After(500 * time.Millisecond):
							// Continue after delay
						case <-ctx.Done():
							// Context cancelled during sleep, don't send
							<-batcher
							return
						}
						<-batcher

						// If the channel is full, do not block
						select {
						case msgChan <- struct{}{}:
							// Nop - signal sent
						default:
							// Nop - channel is full
						}
					})
				default:
					// Nop - there's already a signal batched
				}

			case watchErr := <-watcher.Errors:
				// Log errors only
				slog.WarnContext(ctx, "Error while watching for changes to files on disk",
					slog.Any("error", watchErr),
					slog.String("folder", folder),
				)
			}
		}
	}()

	err = watcher.Add(folder)
	if err != nil {
		return nil, fmt.Errorf("failed to add watched folder: %w", err)
	}

	return msgChan, nil
}
