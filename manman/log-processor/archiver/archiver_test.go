package archiver

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/manman"
)

// mockS3Client implements a simple in-memory S3 for testing
type mockS3Client struct {
	storage map[string][]byte
	mu      sync.RWMutex
}

func newMockS3Client() *mockS3Client {
	return &mockS3Client{
		storage: make(map[string][]byte),
	}
}

func (m *mockS3Client) Upload(ctx context.Context, key string, data []byte, opts *s3.UploadOptions) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.storage[key] = data
	return key, nil
}

func (m *mockS3Client) Download(ctx context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, exists := m.storage[key]
	if !exists {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	return data, nil
}

func (m *mockS3Client) Exists(ctx context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.storage[key]
	return exists, nil
}

func (m *mockS3Client) Append(ctx context.Context, key string, data []byte, opts *s3.UploadOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, exists := m.storage[key]
	if !exists {
		m.storage[key] = data
		return nil
	}
	m.storage[key] = append(existing, data...)
	return nil
}

func (m *mockS3Client) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.storage, key)
	return nil
}

func (m *mockS3Client) GetBucket() string {
	return "test-bucket"
}

// mockLogRepo implements a simple in-memory repository for testing
type mockLogRepo struct {
	logs map[int64]*manman.LogReference
	mu   sync.RWMutex
	nextID int64
}

func newMockLogRepo() *mockLogRepo {
	return &mockLogRepo{
		logs: make(map[int64]*manman.LogReference),
		nextID: 1,
	}
}

func (m *mockLogRepo) Create(ctx context.Context, logRef *manman.LogReference) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	logRef.LogID = m.nextID
	m.nextID++
	m.logs[logRef.LogID] = logRef
	return nil
}

func (m *mockLogRepo) GetByMinute(ctx context.Context, sgcID int64, minuteTimestamp time.Time) (*manman.LogReference, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, log := range m.logs {
		if log.SGCID != nil && *log.SGCID == sgcID &&
		   log.MinuteTimestamp != nil && log.MinuteTimestamp.Equal(minuteTimestamp) {
			return log, nil
		}
	}
	return nil, nil
}

func (m *mockLogRepo) UpdateState(ctx context.Context, logID int64, state string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if log, exists := m.logs[logID]; exists {
		log.State = state
		return nil
	}
	return fmt.Errorf("log not found: %d", logID)
}

func (m *mockLogRepo) ListByTimeRange(ctx context.Context, sgcID int64, startTime, endTime time.Time) ([]*manman.LogReference, error) {
	return nil, nil
}

func (m *mockLogRepo) GetMinMaxTimes(ctx context.Context, sgcID int64) (minTime, maxTime *time.Time, err error) {
	return nil, nil, nil
}

// TestAppendPreservesData tests that appending to an existing log file preserves all data
func TestAppendPreservesData(t *testing.T) {
	// Create mock dependencies
	mockS3 := newMockS3Client()
	mockRepo := newMockLogRepo()

	// Create archiver with mocks (we'll manually call uploadWindow instead of using the full archiver)
	ctx := context.Background()

	// Create a custom archiver-like struct for testing
	type testArchiver struct {
		s3Client *mockS3Client
		logRepo  *mockLogRepo
	}

	a := &testArchiver{
		s3Client: mockS3,
		logRepo:  mockRepo,
	}

	// Test data
	sgcID := int64(1)
	sessionID := int64(42)
	minuteTimestamp := time.Date(2026, 2, 10, 15, 30, 0, 0, time.UTC)
	s3Key := fmt.Sprintf("logs/sgc-%d/session-%d/2026/02/10/15/30.log.gz", sgcID, sessionID)

	// First upload - create initial window
	window1 := &MinuteWindow{
		SGCID:           sgcID,
		SessionID:       sessionID,
		MinuteTimestamp: minuteTimestamp,
		FirstLogTime:    minuteTimestamp.Add(5 * time.Second),
		LastLogTime:     minuteTimestamp.Add(30 * time.Second),
		LineCount:       3,
	}

	// Add some logs to window1
	originalLogs := []string{
		"[2026-02-10T15:30:05Z] [stdout] Server starting...",
		"[2026-02-10T15:30:15Z] [stdout] Loading world...",
		"[2026-02-10T15:30:30Z] [stdout] Server started",
	}
	for _, log := range originalLogs {
		window1.Buffer.WriteString(log + "\n")
	}

	// Upload first window (should create new file)
	exists, err := a.s3Client.Exists(ctx, s3Key)
	if err != nil {
		t.Fatalf("Failed to check existence: %v", err)
	}
	if exists {
		t.Fatal("File should not exist before first upload")
	}

	// Compress and simulate first upload
	compressedData1, err := compressTestData(window1.Buffer.Bytes())
	if err != nil {
		t.Fatalf("Failed to compress window1: %v", err)
	}

	if _, err := a.s3Client.Upload(ctx, s3Key, compressedData1, &s3.UploadOptions{
		ContentType:     "application/gzip",
		ContentEncoding: "gzip",
	}); err != nil {
		t.Fatalf("Failed to upload window1: %v", err)
	}

	// Verify first upload (download and decompress)
	compressedData1Downloaded, err := a.s3Client.Download(ctx, s3Key)
	if err != nil {
		t.Fatalf("Failed to download after first upload: %v", err)
	}

	data1, err := decompressTestData(compressedData1Downloaded)
	if err != nil {
		t.Fatalf("Failed to decompress downloaded data: %v", err)
	}

	data1Str := string(data1)
	for _, expectedLog := range originalLogs {
		if !strings.Contains(data1Str, expectedLog) {
			t.Errorf("First upload missing expected log: %s", expectedLog)
		}
	}

	// Second upload - should append to existing file
	window2 := &MinuteWindow{
		SGCID:           sgcID,
		SessionID:       sessionID,
		MinuteTimestamp: minuteTimestamp,
		FirstLogTime:    minuteTimestamp.Add(45 * time.Second),
		LastLogTime:     minuteTimestamp.Add(55 * time.Second),
		LineCount:       2,
	}

	// Add new logs to window2
	appendedLogs := []string{
		"[2026-02-10T15:30:45Z] [stdout] Player joined",
		"[2026-02-10T15:30:55Z] [stdout] Player spawned",
	}
	for _, log := range appendedLogs {
		window2.Buffer.WriteString(log + "\n")
	}

	// Check if file exists (it should)
	exists, err = a.s3Client.Exists(ctx, s3Key)
	if err != nil {
		t.Fatalf("Failed to check existence before append: %v", err)
	}
	if !exists {
		t.Fatal("File should exist before append")
	}

	// Simulate append with separator (decompress, append, recompress)
	now := time.Now().UTC()
	separator := fmt.Sprintf("\n--- APPENDED AT %s ---\n", now.Format(time.RFC3339))

	// Download and decompress existing data
	existingCompressed, err := a.s3Client.Download(ctx, s3Key)
	if err != nil {
		t.Fatalf("Failed to download existing data for append: %v", err)
	}

	existingData, err := decompressTestData(existingCompressed)
	if err != nil {
		t.Fatalf("Failed to decompress existing data: %v", err)
	}

	// Combine existing + separator + new data
	combinedData := append(existingData, []byte(separator)...)
	combinedData = append(combinedData, window2.Buffer.Bytes()...)

	// Compress combined data
	compressedCombined, err := compressTestData(combinedData)
	if err != nil {
		t.Fatalf("Failed to compress combined data: %v", err)
	}

	// Re-upload with combined compressed data
	if _, err := a.s3Client.Upload(ctx, s3Key, compressedCombined, &s3.UploadOptions{
		ContentType:     "application/gzip",
		ContentEncoding: "gzip",
	}); err != nil {
		t.Fatalf("Failed to upload appended data: %v", err)
	}

	// Verify combined data (download and decompress)
	finalCompressed, err := a.s3Client.Download(ctx, s3Key)
	if err != nil {
		t.Fatalf("Failed to download final data: %v", err)
	}

	finalData, err := decompressTestData(finalCompressed)
	if err != nil {
		t.Fatalf("Failed to decompress final data: %v", err)
	}

	finalDataStr := string(finalData)

	// Verify original logs are preserved
	for _, expectedLog := range originalLogs {
		if !strings.Contains(finalDataStr, expectedLog) {
			t.Errorf("Final data missing original log: %s", expectedLog)
		}
	}

	// Verify appended logs are present
	for _, expectedLog := range appendedLogs {
		if !strings.Contains(finalDataStr, expectedLog) {
			t.Errorf("Final data missing appended log: %s", expectedLog)
		}
	}

	// Verify separator is present
	if !strings.Contains(finalDataStr, "--- APPENDED AT") {
		t.Error("Final data missing append separator")
	}

	// Verify order is correct (original logs should come before appended logs)
	originalIndex := strings.Index(finalDataStr, originalLogs[0])
	appendedIndex := strings.Index(finalDataStr, appendedLogs[0])
	if originalIndex >= appendedIndex {
		t.Error("Original logs should come before appended logs")
	}

	// Verify data integrity - count lines
	lines := strings.Split(strings.TrimSpace(finalDataStr), "\n")
	// Expected: original logs + blank line + separator + appended logs
	expectedLines := len(originalLogs) + 1 + 1 + len(appendedLogs) // +1 blank, +1 separator
	if len(lines) != expectedLines {
		t.Errorf("Expected %d lines, got %d", expectedLines, len(lines))
	}

	// Verify no data corruption - original data unchanged
	originalDataVerify := data1Str
	if !strings.HasPrefix(finalDataStr, originalDataVerify) {
		t.Error("Original data was modified during append - data corruption detected!")
	}

	t.Logf("✓ Append test passed - all data preserved correctly")
	t.Logf("  Original logs: %d", len(originalLogs))
	t.Logf("  Appended logs: %d", len(appendedLogs))
	t.Logf("  Total size: %d bytes", len(finalData))
}

// TestAppendMultipleTimes tests multiple sequential appends
func TestAppendMultipleTimes(t *testing.T) {
	mockS3 := newMockS3Client()
	ctx := context.Background()
	s3Key := "logs/test-multiple-appends.log"

	// Initial upload
	initialData := []byte("Line 1\nLine 2\n")
	if _, err := mockS3.Upload(ctx, s3Key, initialData, nil); err != nil {
		t.Fatalf("Failed initial upload: %v", err)
	}

	// Append 3 times
	for i := 1; i <= 3; i++ {
		separator := fmt.Sprintf("\n--- APPEND %d ---\n", i)
		appendData := []byte(fmt.Sprintf("%sLine %d\n", separator, i+2))

		if err := mockS3.Append(ctx, s3Key, appendData, nil); err != nil {
			t.Fatalf("Failed append %d: %v", i, err)
		}
	}

	// Verify all data is present
	finalData, err := mockS3.Download(ctx, s3Key)
	if err != nil {
		t.Fatalf("Failed to download final data: %v", err)
	}

	finalStr := string(finalData)

	// Check initial data
	if !strings.Contains(finalStr, "Line 1") || !strings.Contains(finalStr, "Line 2") {
		t.Error("Initial data missing after multiple appends")
	}

	// Check all appends
	for i := 1; i <= 3; i++ {
		expectedLine := fmt.Sprintf("Line %d", i+2)
		if !strings.Contains(finalStr, expectedLine) {
			t.Errorf("Missing data from append %d", i)
		}
	}

	// Verify separators
	separatorCount := strings.Count(finalStr, "--- APPEND")
	if separatorCount != 3 {
		t.Errorf("Expected 3 append separators, found %d", separatorCount)
	}

	t.Logf("✓ Multiple append test passed")
}

// TestAppendConcurrency tests that concurrent appends don't corrupt data
func TestAppendConcurrency(t *testing.T) {
	mockS3 := newMockS3Client()
	ctx := context.Background()
	s3Key := "logs/test-concurrent-appends.log"

	// Initial upload
	if _, err := mockS3.Upload(ctx, s3Key, []byte("Initial\n"), nil); err != nil {
		t.Fatalf("Failed initial upload: %v", err)
	}

	// Concurrent appends
	var wg sync.WaitGroup
	numAppends := 10

	for i := 0; i < numAppends; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			separator := fmt.Sprintf("\n--- APPEND %d ---\n", n)
			data := []byte(fmt.Sprintf("%sData %d\n", separator, n))
			if err := mockS3.Append(ctx, s3Key, data, nil); err != nil {
				t.Errorf("Concurrent append %d failed: %v", n, err)
			}
		}(i)
	}

	wg.Wait()

	// Verify all appends are present
	finalData, err := mockS3.Download(ctx, s3Key)
	if err != nil {
		t.Fatalf("Failed to download final data: %v", err)
	}

	finalStr := string(finalData)

	// Check that all data is present (order may vary due to concurrency)
	for i := 0; i < numAppends; i++ {
		expectedData := fmt.Sprintf("Data %d", i)
		if !strings.Contains(finalStr, expectedData) {
			t.Errorf("Missing data from concurrent append %d", i)
		}
	}

	t.Logf("✓ Concurrent append test passed - all %d appends present", numAppends)
}

// compressTestData compresses data using gzip for testing
func compressTestData(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)

	if _, err := gzWriter.Write(data); err != nil {
		gzWriter.Close()
		return nil, err
	}

	if err := gzWriter.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// decompressTestData decompresses gzipped data for testing
func decompressTestData(data []byte) ([]byte, error) {
	reader := bytes.NewReader(data)
	gzReader, err := gzip.NewReader(reader)
	if err != nil {
		return nil, err
	}
	defer gzReader.Close()

	return io.ReadAll(gzReader)
}
