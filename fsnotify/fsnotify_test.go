package fsnotify

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWatchFolder(t *testing.T) {
	t.Run("notifies on file creation", func(t *testing.T) {
		dir := t.TempDir()

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		msgChan, err := WatchFolder(ctx, dir)
		require.NoError(t, err)
		require.NotNil(t, msgChan)

		// Create a file in the watched folder
		filePath := filepath.Join(dir, "test.txt")
		err = os.WriteFile(filePath, []byte("hello"), 0644)
		require.NoError(t, err)

		// Should receive a notification
		select {
		case <-msgChan:
			// Success
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for notification")
		}
	})

	t.Run("notifies on file write", func(t *testing.T) {
		dir := t.TempDir()

		// Create a file before starting the watcher
		filePath := filepath.Join(dir, "existing.txt")
		err := os.WriteFile(filePath, []byte("initial"), 0644)
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		msgChan, err := WatchFolder(ctx, dir)
		require.NoError(t, err)

		// Modify the existing file
		err = os.WriteFile(filePath, []byte("modified"), 0644)
		require.NoError(t, err)

		// Should receive a notification
		select {
		case <-msgChan:
			// Success
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for notification")
		}
	})

	t.Run("batches multiple changes", func(t *testing.T) {
		dir := t.TempDir()

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		msgChan, err := WatchFolder(ctx, dir)
		require.NoError(t, err)

		// Create multiple files in quick succession
		for i := range 5 {
			filePath := filepath.Join(dir, fmt.Sprintf("file%d.txt", i))
			err = os.WriteFile(filePath, []byte("content"), 0644)
			require.NoError(t, err)
		}

		// Should receive exactly one notification due to batching
		select {
		case <-msgChan:
			// Success - received first notification
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for notification")
		}

		// Wait a bit longer than the batch window and verify no extra notifications
		select {
		case <-msgChan:
			t.Fatal("received unexpected extra notification")
		case <-time.After(700 * time.Millisecond):
			// Success - no extra notification
		}
	})

	t.Run("closes channel on context cancellation", func(t *testing.T) {
		dir := t.TempDir()

		ctx, cancel := context.WithCancel(t.Context())

		msgChan, err := WatchFolder(ctx, dir)
		require.NoError(t, err)

		// Cancel the context
		cancel()

		// Channel should be closed
		select {
		case _, ok := <-msgChan:
			assert.False(t, ok, "channel should be closed")
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for channel to close")
		}
	})

	t.Run("returns error for non-existent folder", func(t *testing.T) {
		ctx := t.Context()

		msgChan, err := WatchFolder(ctx, "/non/existent/path/that/does/not/exist")
		require.Error(t, err)
		assert.Nil(t, msgChan)
		assert.Contains(t, err.Error(), "failed to add watched folder")
	})

	t.Run("does not block when channel is full", func(t *testing.T) {
		dir := t.TempDir()

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		msgChan, err := WatchFolder(ctx, dir)
		require.NoError(t, err)

		// Create a file to trigger notification
		filePath := filepath.Join(dir, "test1.txt")
		err = os.WriteFile(filePath, []byte("hello"), 0644)
		require.NoError(t, err)

		// Wait for batching to complete
		time.Sleep(600 * time.Millisecond)

		// Create another file without draining the channel
		filePath2 := filepath.Join(dir, "test2.txt")
		err = os.WriteFile(filePath2, []byte("world"), 0644)
		require.NoError(t, err)

		// Wait for batching - this should not block even though channel has unread message
		time.Sleep(600 * time.Millisecond)

		// Now drain the channel - should get at least one message
		select {
		case <-msgChan:
			// Success
		case <-time.After(1 * time.Second):
			t.Fatal("expected at least one notification")
		}
	})
}
