# Histogram-Based Log Viewer

## Overview

Interactive histogram visualization for navigating and viewing historical session logs. Replaces the old datetime picker (±10 minute window) with a visual timeline showing log activity across the entire session duration.

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    Session Detail Page                   │
│  ┌───────────────────────────────────────────────────┐  │
│  │    Histogram Component (Canvas + JS)              │  │
│  │    [Fetches JSON via fetch(), renders canvas]     │  │
│  │    [Drag interaction updates hidden form fields]  │  │
│  └───────────────────────────────────────────────────┘  │
│  ┌───────────────────────────────────────────────────┐  │
│  │  <form hx-get="/sessions/{id}/logs/load">        │  │
│  │    <input type="hidden" name="start" value="...">│  │
│  │    <input type="hidden" name="end" value="...">  │  │
│  │    <button hx-target="#log-content">Load</button>│  │
│  │  </form>                                          │  │
│  └───────────────────────────────────────────────────┘  │
│  ┌───────────────────────────────────────────────────┐  │
│  │  <div id="log-content">                           │  │
│  │    <!-- HTMX swaps log HTML here -->             │  │
│  │    <button hx-get="...&offset=10000">Load More   │  │
│  │  </div>                                           │  │
│  └───────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────┘
```

## Features

### Implemented ✅

1. **Adaptive Histogram Granularity**
   - < 1 hour session → 1-minute buckets
   - 1-24 hours session → 5-minute buckets
   - > 24 hours session → 1-hour buckets
   - Caps at 1000 buckets max for UI performance

2. **Stacked Bar Visualization**
   - stdout: gray (#d4d4d4)
   - stderr: red (#f48771)
   - host: blue (#4fc1ff)
   - Y-axis: line count scale
   - X-axis: time labels (adaptive formatting)

3. **Interactive Selection**
   - Click single bucket to select
   - Drag to select range
   - Blue overlay shows selected range
   - Auto-truncates selections > 6 hours with warning

4. **Hover Tooltips**
   - Shows exact date/time for bucket
   - Displays line counts by source
   - "Double-click to zoom" hint in overview mode

5. **Zoom/Drill-Down**
   - Double-click bucket to zoom into ~20 buckets around it
   - "← Back to overview" button when zoomed
   - Higher resolution view of selected time range

6. **Auto-Load on Page Load**
   - Automatically selects last 10 buckets
   - Loads logs for selected range after 500ms
   - Provides immediate value without user interaction

7. **Progressive Loading**
   - Loads ~10,000 lines per chunk
   - "Load More" button if more data available
   - HTMX appends chunks with `hx-swap="beforeend"`

8. **HTMX-First Architecture**
   - JavaScript only for canvas rendering and interaction
   - HTMX handles all log content fetching and updates
   - Server-rendered HTML for log content

### Partially Implemented ⚠️

1. **Visual Loaded Range Indicator**
   - Currently only shows selection (blue overlay)
   - Should also show loaded range (green overlay)
   - Allows visual distinction between selected vs loaded

2. **Zoom Range Filtering**
   - Zoom UI works but fetches full histogram
   - Backend doesn't filter histogram by time range yet
   - Need to add `start_time`/`end_time` params to `GetLogHistogram` RPC

### Not Implemented ❌

1. **Mobile Responsive Design**
   - Canvas is fixed 800x200px
   - May need touch event handlers for mobile drag
   - Tooltip positioning may need adjustment on small screens

2. **Edge Case Handling**
   - Empty sessions (no logs) - basic handling exists
   - Very long sessions (> 7 days) - untested
   - Sessions with gaps in log data - untested

## API Endpoints

### gRPC (API Server)

**`GetLogHistogram`**
- Request: `session_id`
- Response: `buckets[]`, `granularity`, `session_start`, `session_end`
- Location: `manmanv2/api/handlers/logs.go`
- Aggregates log references by time buckets without downloading S3 files

**`GetHistoricalLogs`** (Extended)
- Request: `session_id`, `start_timestamp`, `end_timestamp`, `offset`, `limit`
- Response: `batches[]`, `total_lines`, `has_more`
- Max range: 6 hours (up from 30 minutes)
- Default limit: 10,000 lines per request

### HTTP (UI Server)

**`GET /sessions/{id}/logs/histogram`**
- Returns JSON histogram data for canvas rendering
- Optional query params: `start`, `end` (for zoom - not yet implemented)
- Location: `manmanv2/ui/handlers_sessions.go::handleLogHistogram`

**`GET /sessions/{id}/logs/load`**
- Query params: `start`, `end`, `offset`, `limit`
- Returns HTML partial with formatted logs
- Includes "Load More" button if `has_more=true`
- Auto-truncates if range > 6 hours
- Location: `manmanv2/ui/handlers_sessions.go::handleLoadHistoricalLogs`

## Database Schema

Uses existing `log_references` table:
- `session_id` - Session identifier
- `start_time` - Log batch start timestamp
- `end_time` - Log batch end timestamp
- `line_count` - Number of lines in batch
- `source` - Log source (stdout, stderr, host)
- `state` - Processing state (only 'complete' logs shown)

Histogram aggregation query:
```sql
SELECT 
  (EXTRACT(EPOCH FROM start_time)::bigint / $bucket_seconds) * $bucket_seconds AS bucket_timestamp,
  source,
  SUM(line_count) AS total_lines
FROM log_references
WHERE session_id = $1 AND state = 'complete'
GROUP BY bucket_timestamp, source
ORDER BY bucket_timestamp
```

## UI Components

### Template Location
`manmanv2/ui/templates/session_detail.html` - Historical Logs section (non-running sessions only)

### JavaScript State
- `fullHistogramData` - Full session histogram (for zoom out)
- `histogramData` - Current view (full or zoomed)
- `zoomRange` - Current zoom range `{start, end}` or null
- `selection` - Selected range `{startIdx, endIdx}` or null
- `isSelecting` - Boolean for drag state

### Key Functions
- `loadHistogram(startTime, endTime)` - Fetches and renders histogram
- `renderHistogram()` - Draws canvas (bars, axes, labels, selection overlay)
- `zoomIntoBucket(idx)` - Zooms into ~20 buckets around clicked bucket
- `updateSelection()` - Updates form fields and selection info display

## User Interactions

1. **Hover over bar** → Tooltip shows date/time and line counts
2. **Click bar** → Selects single bucket
3. **Click + drag** → Selects range of buckets
4. **Double-click bar** → Zooms into that time range (overview mode only)
5. **Click "Load Logs"** → Fetches logs for selected range via HTMX
6. **Click "Load More"** → Appends next chunk of logs (progressive loading)
7. **Click "← Back to overview"** → Returns to full session histogram
8. **Click "Clear"** → Clears logs and resets selection

## Configuration

### Limits
- **Max download range**: 6 hours (auto-truncates with warning)
- **Max buckets**: 1000 (adjusts granularity if needed)
- **Lines per chunk**: 10,000 (progressive loading)
- **Auto-select on load**: Last 10 buckets

### Bucket Granularity
```go
if duration < time.Hour {
    bucketSeconds = 60        // 1 minute
    granularity = "1m"
} else if duration < 24*time.Hour {
    bucketSeconds = 5 * 60    // 5 minutes
    granularity = "5m"
} else {
    bucketSeconds = 60 * 60   // 1 hour
    granularity = "1h"
}

// Cap at 1000 buckets
if estimatedBuckets > 1000 {
    bucketSeconds = duration.Seconds() / 1000
    granularity = fmt.Sprintf("%ds", bucketSeconds)
}
```

## Future Improvements

### High Priority

1. **Implement Zoom Range Filtering**
   - Add `start_time`/`end_time` to `GetLogHistogramRequest`
   - Filter histogram query by time range in repository
   - Reduces data transfer for zoomed views

2. **Visual Loaded Range Indicator**
   - Track which time ranges have been loaded
   - Draw green overlay on histogram for loaded ranges
   - Distinct from blue selection overlay

3. **Fix Progressive Loading**
   - Verify "Load More" button appends correctly
   - Test with large time ranges (> 10k lines)
   - Ensure offset calculation is correct across batches

### Medium Priority

4. **Mobile Responsive Design**
   - Add touch event handlers for drag selection
   - Adjust canvas size for small screens
   - Fix tooltip positioning on mobile

5. **Better Zoom UX**
   - Show zoom level indicator (e.g., "Zoomed: 2026-03-10 14:00-15:00")
   - Allow zoom via selection + "Zoom In" button (not just double-click)
   - Add keyboard shortcuts (Escape to zoom out)

6. **Performance Optimization**
   - Cache histogram data in browser
   - Debounce selection updates during drag
   - Virtual scrolling for very large log outputs

### Low Priority

7. **Enhanced Visualization**
   - Add gridlines for easier reading
   - Show gaps in log data (missing buckets)
   - Color intensity based on log volume

8. **Export/Download**
   - Download logs for selected range as .log file
   - Export histogram as PNG/SVG
   - Copy logs to clipboard

9. **Search Integration**
   - Search within loaded logs
   - Highlight matching lines
   - Jump to next/previous match

## Testing Recommendations

### Unit Tests
- Bucket calculation logic (various session durations)
- Pagination offset/limit calculations
- Auto-truncation logic (> 6 hours)

### Integration Tests
- Histogram endpoint with empty/populated sessions
- Paginated log loading with various ranges
- "Load More" functionality with large datasets

### Manual Testing
- Sessions with different durations (< 1hr, 1-24hr, > 24hr)
- Drag selection across multiple buckets
- Auto-truncation with > 6 hour selections
- Progressive loading with large log volumes
- Mobile devices for responsive design
- Double-click zoom and navigation
- Tooltip display on hover

## Known Issues

1. **Zoom fetches full histogram** - Backend doesn't support range filtering yet
2. **No loaded range indicator** - Can't visually see which logs are already loaded
3. **Canvas font rendering** - Slightly different from page font (minor visual inconsistency)
4. **No mobile optimization** - Touch events and responsive sizing not implemented

## Related Files

- `manmanv2/protos/api.proto` - gRPC definitions
- `manmanv2/api/handlers/logs.go` - Log handlers (GetLogHistogram, GetHistoricalLogs)
- `manmanv2/api/repository/postgres/log_reference.go` - Database queries
- `manmanv2/api/repository/repository.go` - Repository interface
- `manmanv2/ui/handlers_sessions.go` - HTTP handlers
- `manmanv2/ui/templates/session_detail.html` - UI template
