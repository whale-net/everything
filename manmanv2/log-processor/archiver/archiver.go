package archiver

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/manmanv2/models"
	"github.com/whale-net/everything/manmanv2/api/repository"
)

// s3Uploader is the subset of s3.Client operations the archiver needs.
// Defined as an interface so tests can inject a fake without a real S3 backend.
type s3Uploader interface {
	Upload(ctx context.Context, key string, data []byte, opts *s3.UploadOptions) (string, error)
	Download(ctx context.Context, key string) ([]byte, error)
	Exists(ctx context.Context, key string) (bool, error)
	GetBucket() string
}

// MinuteWindow represents a batch of logs within a specific minute for a session
type MinuteWindow struct {
	SGCID           int64
	SessionID       int64
	MinuteTimestamp time.Time
	Buffer          bytes.Buffer
	LineCount       int32
	FirstLogTime    time.Time
	LastLogTime     time.Time
	RetryCount      int32
	mu              sync.Mutex
}

// AddLog adds a log line to the window
func (w *MinuteWindow) AddLog(timestamp time.Time, source, message string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Track first and last log times
	if w.FirstLogTime.IsZero() || timestamp.Before(w.FirstLogTime) {
		w.FirstLogTime = timestamp
	}
	if w.LastLogTime.IsZero() || timestamp.After(w.LastLogTime) {
		w.LastLogTime = timestamp
	}

	// Format: [timestamp] [source] message
	fmt.Fprintf(&w.Buffer, "[%s] [%s] %s\n", timestamp.Format(time.RFC3339), source, message)
	w.LineCount++
}

// GetKey returns the MinuteWindow's unique key
func (w *MinuteWindow) GetKey() string {
	return fmt.Sprintf("%d_%d_%d", w.SGCID, w.SessionID, w.MinuteTimestamp.Unix())
}

// Archiver manages log batching and S3 uploads
type Archiver struct {
	s3Client   s3Uploader
	logRepo    repository.LogReferenceRepository
	windows    map[string]*MinuteWindow
	windowsMu  sync.RWMutex
	workers    chan struct{}
	uploadChan chan *MinuteWindow
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	closerWg   sync.WaitGroup
	retryWg    sync.WaitGroup
}

const (
	// Window closes 2 minutes after the minute boundary
	windowClosureDelay = 2 * time.Minute
	// Check for windows to close every 30 seconds
	windowCheckInterval = 30 * time.Second
	// Number of concurrent upload workers
	numUploadWorkers = 4
	// Concurrency protection window - abort if pending record created within this time
	concurrencyProtectionWindow = 15 * time.Second
)

var (
	// maxUploadRetries bounds how many times a failed window upload is retried
	// before the window is dropped for good. A var (not const) so tests can
	// shrink it for fast, deterministic exercising of the give-up path.
	maxUploadRetries int32 = 5
	// baseUploadRetryDelay is the delay before the first retry; it doubles
	// with each subsequent attempt, capped at maxUploadRetryDelay.
	baseUploadRetryDelay = 5 * time.Second
	// maxUploadRetryDelay caps the exponential backoff between retries.
	maxUploadRetryDelay = 2 * time.Minute
)

// NewArchiver creates a new log archiver
func NewArchiver(s3Client s3Uploader, logRepo repository.LogReferenceRepository) *Archiver {
	ctx, cancel := context.WithCancel(context.Background())

	a := &Archiver{
		s3Client:   s3Client,
		logRepo:    logRepo,
		windows:    make(map[string]*MinuteWindow),
		workers:    make(chan struct{}, numUploadWorkers),
		uploadChan: make(chan *MinuteWindow, 100),
		ctx:        ctx,
		cancel:     cancel,
	}

	// Start upload workers
	for i := 0; i < numUploadWorkers; i++ {
		a.wg.Add(1)
		go a.uploadWorker()
	}

	// Start background window closer
	a.closerWg.Add(1)
	go a.windowCloser()

	return a
}

// AddLog adds a log to the appropriate minute window
func (a *Archiver) AddLog(sgcID, sessionID int64, timestamp time.Time, source, message string) {
	// Truncate to minute boundary
	minuteTimestamp := timestamp.Truncate(time.Minute)

	a.windowsMu.Lock()
	defer a.windowsMu.Unlock()

	// Get or create window (key includes session ID for per-session batching)
	key := fmt.Sprintf("%d_%d_%d", sgcID, sessionID, minuteTimestamp.Unix())
	window, exists := a.windows[key]
	if !exists {
		window = &MinuteWindow{
			SGCID:           sgcID,
			SessionID:       sessionID,
			MinuteTimestamp: minuteTimestamp,
		}
		a.windows[key] = window
		slog.Info("created new archive window", "sgc_id", sgcID, "session_id", sessionID, "minute", minuteTimestamp.Format("15:04"))
	}

	// Add log to window
	window.AddLog(timestamp, source, message)
}

// windowCloser periodically checks for windows that should be closed and uploaded
// Uses a drift-free approach by calculating the next check time based on wall clock
func (a *Archiver) windowCloser() {
	defer a.closerWg.Done()

	// Calculate next check time aligned to windowCheckInterval boundaries
	// This prevents drift and ensures consistent timing
	nextCheck := func() time.Time {
		now := time.Now().UTC()
		// Round up to next interval boundary
		intervalNanos := windowCheckInterval.Nanoseconds()
		currentNanos := now.UnixNano()
		nextNanos := ((currentNanos / intervalNanos) + 1) * intervalNanos
		return time.Unix(0, nextNanos)
	}

	for {
		next := nextCheck()
		sleepDuration := time.Until(next)

		select {
		case <-a.ctx.Done():
			return
		case <-time.After(sleepDuration):
			a.closeStaleWindows()
		}
	}
}

// closeStaleWindows finds windows older than windowClosureDelay and queues them for upload
func (a *Archiver) closeStaleWindows() {
	now := time.Now().UTC()
	cutoff := now.Add(-windowClosureDelay)

	a.windowsMu.Lock()
	defer a.windowsMu.Unlock()

	closedCount := 0
	for key, window := range a.windows {
		if window.MinuteTimestamp.Before(cutoff) {
			// Queue for upload
			select {
			case a.uploadChan <- window:
				delete(a.windows, key)
				closedCount++
			case <-a.ctx.Done():
				return
			}
		}
	}

	if closedCount > 0 {
		slog.Info("closed stale windows for upload", "closed_count", closedCount, "active_windows", len(a.windows))
	}
}

// uploadWorker processes windows from the upload queue
func (a *Archiver) uploadWorker() {
	defer a.wg.Done()

	for {
		select {
		case <-a.ctx.Done():
			return
		case window := <-a.uploadChan:
			if err := a.uploadWindow(a.ctx, window); err != nil {
				a.scheduleRetry(window, err)
			}
		}
	}
}

// scheduleRetry re-queues a window for another upload attempt after an
// exponential backoff with jitter, up to maxUploadRetries. This is what
// keeps a transient failure (e.g. the DB briefly failing over) from
// permanently dropping the window's buffered logs.
func (a *Archiver) scheduleRetry(window *MinuteWindow, uploadErr error) {
	window.RetryCount++
	if window.RetryCount > maxUploadRetries {
		slog.Error("giving up on window upload after max retries",
			"window_key", window.GetKey(), "retries", window.RetryCount-1, "error", uploadErr)
		return
	}

	delay := uploadRetryBackoff(window.RetryCount)
	slog.Warn("window upload failed, scheduling retry",
		"window_key", window.GetKey(), "retry_count", window.RetryCount, "retry_in", delay, "error", uploadErr)

	a.retryWg.Add(1)
	go func() {
		defer a.retryWg.Done()
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(delay):
		}
		select {
		case a.uploadChan <- window:
		case <-a.ctx.Done():
		}
	}()
}

// uploadRetryBackoff computes an exponential backoff delay (capped, with
// jitter) for the given retry attempt number (1-indexed).
func uploadRetryBackoff(retryCount int32) time.Duration {
	delay := baseUploadRetryDelay * time.Duration(int64(1)<<uint(retryCount-1))
	if delay <= 0 || delay > maxUploadRetryDelay {
		delay = maxUploadRetryDelay
	}
	half := int64(delay) / 2
	jitter := time.Duration(0)
	if half > 0 {
		jitter = time.Duration(rand.Int63n(half))
	}
	return time.Duration(half) + jitter
}

// uploadWindow uploads a minute window to S3
func (a *Archiver) uploadWindow(ctx context.Context, window *MinuteWindow) error {
	window.mu.Lock()
	defer window.mu.Unlock()

	// Check if window has any logs
	if window.LineCount == 0 {
		return nil
	}

	// Check for existing pending record (concurrency protection)
	existingLog, err := a.logRepo.GetByMinute(ctx, window.SGCID, window.MinuteTimestamp)
	if err != nil {
		return fmt.Errorf("failed to check for existing log record: %w", err)
	}

	if existingLog != nil {
		// Check if it's a recent pending record. Skip this check when we're
		// retrying our own earlier failed attempt (RetryCount > 0) - that
		// pending record is one we created ourselves, not evidence of a
		// concurrent worker, and waiting it out would just silently drop
		// the window once the retry backoff exceeds this window.
		if existingLog.State == manman.LogStatePending && window.RetryCount == 0 {
			age := time.Since(existingLog.CreatedAt)
			if age < concurrencyProtectionWindow {
				// Another worker is handling this, abort
				slog.Warn("aborting window upload: recent pending record exists", "window_key", window.GetKey(), "age", age)
				return nil
			}
		}
		// If it's complete or old pending, we'll append
	}

	// Generate S3 key: logs/sgc-{sgc_id}/session-{session_id}/YYYY/MM/DD/HH/mm.log.gz
	s3Key := fmt.Sprintf("logs/sgc-%d/session-%d/%04d/%02d/%02d/%02d/%02d.log.gz",
		window.SGCID,
		window.SessionID,
		window.MinuteTimestamp.Year(),
		window.MinuteTimestamp.Month(),
		window.MinuteTimestamp.Day(),
		window.MinuteTimestamp.Hour(),
		window.MinuteTimestamp.Minute(),
	)

	// Create pending log reference
	logRef := &manman.LogReference{
		SessionID:       window.SessionID,
		SGCID:           &window.SGCID,
		FilePath:        fmt.Sprintf("s3://%s/%s", a.s3Client.GetBucket(), s3Key),
		StartTime:       window.FirstLogTime,
		EndTime:         window.LastLogTime,
		LineCount:       window.LineCount,
		Source:          "host", // Aggregated from multiple sources
		MinuteTimestamp: &window.MinuteTimestamp,
		State:           manman.LogStatePending,
		CreatedAt:       time.Now().UTC(),
	}

	// Create database record
	if err := a.logRepo.Create(ctx, logRef); err != nil {
		return fmt.Errorf("failed to create log reference: %w", err)
	}

	// Check if we need to append
	exists, err := a.s3Client.Exists(ctx, s3Key)
	if err != nil {
		return fmt.Errorf("failed to check S3 object existence: %w", err)
	}

	logData := window.Buffer.Bytes()

	if exists {
		// Append to existing gzipped object
		now := time.Now().UTC()
		separator := fmt.Sprintf("\n--- APPENDED AT %s ---\n", now.Format(time.RFC3339))

		// Download and decompress existing data
		existingCompressed, err := a.s3Client.Download(ctx, s3Key)
		if err != nil {
			return fmt.Errorf("failed to download existing object for append: %w", err)
		}

		existingData, err := decompressGzip(existingCompressed)
		if err != nil {
			return fmt.Errorf("failed to decompress existing object: %w", err)
		}

		// Combine existing + separator + new data
		combinedData := append(existingData, []byte(separator)...)
		combinedData = append(combinedData, logData...)

		// Compress combined data
		compressedData, err := compressGzip(combinedData)
		if err != nil {
			return fmt.Errorf("failed to compress appended data: %w", err)
		}

		slog.Info("appending to existing S3 object", "s3_key", s3Key)
		if _, err := a.s3Client.Upload(ctx, s3Key, compressedData, &s3.UploadOptions{
			ContentType:     "application/gzip",
			ContentEncoding: "gzip",
		}); err != nil {
			return fmt.Errorf("failed to upload appended data: %w", err)
		}

		// Update appended_at timestamp
		logRef.AppendedAt = &now
	} else {
		// Compress log data
		compressedData, err := compressGzip(logData)
		if err != nil {
			return fmt.Errorf("failed to compress log data: %w", err)
		}

		// Upload new gzipped object
		if _, err := a.s3Client.Upload(ctx, s3Key, compressedData, &s3.UploadOptions{
			ContentType:     "application/gzip",
			ContentEncoding: "gzip",
		}); err != nil {
			return fmt.Errorf("failed to upload to S3: %w", err)
		}

		slog.Info("uploaded compressed log",
			"raw_bytes", len(logData), "compressed_bytes", len(compressedData),
			"reduction_pct", 100.0*(1-float64(len(compressedData))/float64(len(logData))))
	}

	// Mark as complete
	if err := a.logRepo.UpdateState(ctx, logRef.LogID, manman.LogStateComplete); err != nil {
		return fmt.Errorf("failed to update log state: %w", err)
	}

	slog.Info("successfully uploaded window", "window_key", window.GetKey(), "line_count", window.LineCount, "s3_key", s3Key)
	return nil
}

// FlushSession uploads all pending windows for a specific session (called on session stop)
func (a *Archiver) FlushSession(ctx context.Context, sessionID int64) error {
	slog.Info("flushing pending log windows for session", "session_id", sessionID)

	a.windowsMu.Lock()
	sessionWindows := make([]*MinuteWindow, 0)
	for key, window := range a.windows {
		if window.SessionID == sessionID {
			sessionWindows = append(sessionWindows, window)
			delete(a.windows, key)
		}
	}
	a.windowsMu.Unlock()

	if len(sessionWindows) == 0 {
		slog.Info("no pending windows for session", "session_id", sessionID)
		return nil
	}

	// Upload all session windows synchronously
	var errs []error
	for _, window := range sessionWindows {
		if err := a.uploadWindow(ctx, window); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to flush %d windows for session %d: %v", len(errs), sessionID, errs)
	}

	slog.Info("successfully flushed windows for session", "window_count", len(sessionWindows), "session_id", sessionID)
	return nil
}

// FlushAll uploads all pending windows synchronously (for graceful shutdown)
func (a *Archiver) FlushAll(ctx context.Context) error {
	slog.Info("flushing all pending log windows")

	a.windowsMu.Lock()
	windows := make([]*MinuteWindow, 0, len(a.windows))
	for _, window := range a.windows {
		windows = append(windows, window)
	}
	a.windows = make(map[string]*MinuteWindow) // Clear the map
	a.windowsMu.Unlock()

	// Upload all windows synchronously
	var errs []error
	for _, window := range windows {
		if err := a.uploadWindow(ctx, window); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to flush %d windows: %v", len(errs), errs)
	}

	slog.Info("successfully flushed windows", "window_count", len(windows))
	return nil
}

// Close gracefully shuts down the archiver
func (a *Archiver) Close() error {
	slog.Info("shutting down archiver")

	// Flush all pending windows
	if err := a.FlushAll(context.Background()); err != nil {
		slog.Error("error during archiver shutdown flush", "error", err)
	}

	// Stop the background closer
	a.cancel()
	a.closerWg.Wait()

	// Wait for any in-flight retry timers to observe cancellation before
	// closing the channel they'd otherwise send into.
	a.retryWg.Wait()

	// Close upload channel and wait for workers
	close(a.uploadChan)
	a.wg.Wait()

	slog.Info("archiver shutdown complete")
	return nil
}

// compressGzip compresses data using gzip
func compressGzip(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)

	if _, err := gzWriter.Write(data); err != nil {
		gzWriter.Close()
		return nil, fmt.Errorf("failed to write to gzip: %w", err)
	}

	if err := gzWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close gzip writer: %w", err)
	}

	return buf.Bytes(), nil
}

// decompressGzip decompresses gzipped data
func decompressGzip(data []byte) ([]byte, error) {
	reader := bytes.NewReader(data)
	gzReader, err := gzip.NewReader(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzReader.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(gzReader); err != nil {
		return nil, fmt.Errorf("failed to read from gzip: %w", err)
	}

	return buf.Bytes(), nil
}
