package htmxsse

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
)

// Fragment is a function that produces a fragment for a connection and topic.
// It is called with the original request and the topic name.
// Errors are transient and non-signalling; an error causes no bytes to be written
// for that event and does not close the stream.
type Fragment func(*http.Request, string) ([]byte, error)

// Handler creates an HTTP handler that upgrades a request to SSE and streams
// events for the given topics using the provided fragment function.
//
// The handler:
// - Accepts one or more topics
// - Produces full current-state fragments on connect/reconnect for each topic
// - Emits fragments as SSE events with swap/keepalive semantics (FR5)
// - Runs a heartbeat for each topic on a configurable interval (NFR11)
// - Closes the stream after a configurable maximum lifetime (NFR12)
// - Cleans up all subscriptions on stream exit (FR1)
func Handler(hub *Hub, topics []string, fragment Fragment) http.HandlerFunc {
	if len(topics) == 0 {
		panic("Handler requires at least one topic")
	}
	// Sort topics for consistent ordering in baseline set encoding
	sortedTopics := make([]string, len(topics))
	copy(sortedTopics, topics)
	sort.Strings(sortedTopics)

	return func(w http.ResponseWriter, r *http.Request) {
		// Set SSE response headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		// Set retry interval (FR5, NFR16)
		retryMs := hub.config.AdvertisedRetryInterval.Milliseconds()
		if retryMs > 0 {
			fmt.Fprintf(w, "retry: %d\n", retryMs)
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming not supported", http.StatusInternalServerError)
			return
		}

		// Flush to commit the response (FR2: headers committed before first fragment)
		flusher.Flush()

		// Parse the Last-Event-ID header for reconnect baseline (FR5)
		lastEventID := r.Header.Get("Last-Event-ID")
		clientBaseline := parseBaseline(lastEventID)

		// Subscribe to all topics and collect unsubscribe functions
		subscriptions := make(map[string]func())
		defer func() {
			// FR1: Release all subscriptions on exit
			for _, unsub := range subscriptions {
				unsub()
			}
		}()

		// Create a forwarding channel for topic events
		// This allows us to handle any number of topics in a single select
		eventChans := make(map[string]<-chan Event)
		forwardCh := make(chan Event, 1)

		for _, topic := range sortedTopics {
			eventCh, unsub := hub.Subscribe(topic)
			subscriptions[topic] = unsub
			eventChans[topic] = eventCh

			// Start a goroutine to forward events from this topic to forwardCh
			go func(topic string, ch <-chan Event) {
				for event := range ch {
					select {
					case forwardCh <- event:
					case <-r.Context().Done():
						return
					}
				}
			}(topic, eventCh)
		}

		// Produce full-state fragments on connect for each topic (FR5)
		// Build the initial baseline set
		currentBaseline := make(map[string][]byte)

		// Produce all fragments first to ensure complete baseline set for all emitted frames (PN15)
		for _, topic := range sortedTopics {
			frag, err := fragment(r, topic)
			if err != nil {
				// NFR11: Fragment error during connect means write nothing for that topic
				log.Printf("htmxsse: fragment error on connect for topic %s: %v", topic, err)
				currentBaseline[topic] = nil
			} else {
				currentBaseline[topic] = frag
			}
		}

		// Now emit swap or keepalive for each topic based on reconnect baseline (FR5)
		// At this point, currentBaseline contains all topics' fragments
		for _, topic := range sortedTopics {
			frag := currentBaseline[topic]
			if frag == nil {
				// Fragment was nil (error during production), emit nothing
				continue
			}
			clientHash, exists := clientBaseline[topic]
			fragmentHash := hashFragment(frag)
			if exists && clientHash == fragmentHash {
				// Client already has this state, emit keepalive (no id)
				emitKeepalive(w, flusher, topic)
			} else {
				// New or stale state, emit swap with id containing full baseline set
				emitSwap(w, flusher, topic, frag, encodeBaseline(sortedTopics, currentBaseline))
			}
		}

		// Set up heartbeat ticker (NFR11)
		heartbeatTicker := hub.clock.NewTicker(hub.config.HeartbeatInterval)
		defer heartbeatTicker.Stop()

		// Set up max stream lifetime (NFR12)
		lifetimeTicker := hub.clock.NewTicker(hub.config.MaxStreamLifetime)
		defer lifetimeTicker.Stop()

		// Request context for checking cancellation (FR2)
		ctx := r.Context()

		// Main event loop
		for {
			select {
			case <-ctx.Done():
				// Request context cancelled (FR2: stream ends when request context is done)
				return

			case <-lifetimeTicker.C():
				// NFR12: Max stream lifetime reached, close the stream
				return

			case <-heartbeatTicker.C():
				// NFR11: Run heartbeat for each topic
				for _, topic := range sortedTopics {
					frag, err := fragment(r, topic)
					if err != nil {
						// NFR11: Error branch - write nothing for this interval
						log.Printf("htmxsse: heartbeat fragment error for topic %s: %v", topic, err)
						continue
					}

					fragmentHash := hashFragment(frag)
					lastHash := hashFragment(currentBaseline[topic])

					if fragmentHash == lastHash {
						// Unchanged - emit keepalive (no id, no swap)
						emitKeepalive(w, flusher, topic)
					} else {
						// Changed - emit swap with id containing full baseline set
						currentBaseline[topic] = frag
						emitSwap(w, flusher, topic, frag, encodeBaseline(sortedTopics, currentBaseline))
					}
				}

			case event := <-forwardCh:
				topic := event.Topic
				// Fragment for this event (per-connection rendering, FR3)
				frag, err := fragment(r, topic)
				if err != nil {
					// FR3: Fragment error means no bytes written, stream stays open
					log.Printf("htmxsse: fragment error for topic %s: %v", topic, err)
					continue
				}

				currentBaseline[topic] = frag
				// Always emit swap for received events
				emitSwap(w, flusher, topic, frag, encodeBaseline(sortedTopics, currentBaseline))

			}
		}
	}
}

// emitSwap writes a full state event (swap) to the response.
// The event includes the topic name, the fragment data, and the baseline ID.
// All writes use the Flusher to ensure atomicity and immediate delivery.
func emitSwap(w http.ResponseWriter, flusher http.Flusher, topic string, fragment []byte, baselineID string) {
	fmt.Fprintf(w, "event: %s\n", topic)
	fmt.Fprintf(w, "id: %s\n", baselineID)
	fmt.Fprintf(w, "data: %s\n\n", bytes.TrimSpace(fragment))
	flusher.Flush()
}

// emitKeepalive writes a keepalive event (no swap, no id) to the response.
// This is emitted when the client already has the current state.
func emitKeepalive(w http.ResponseWriter, flusher http.Flusher, topic string) {
	fmt.Fprintf(w, "event: %s-keepalive\n", topic)
	fmt.Fprintf(w, "data: null\n\n")
	flusher.Flush()
}

// hashFragment returns a hex-encoded SHA256 hash of the fragment bytes.
// Used for content-based baseline comparison (FR5).
func hashFragment(fragment []byte) string {
	if len(fragment) == 0 {
		return ""
	}
	hash := sha256.Sum256(fragment)
	return hex.EncodeToString(hash[:])
}

// encodeBaseline encodes the baseline set as a pipe-separated list of "topic:hash" pairs.
// Topics are in the order provided (expected to be sorted).
// Format: "topic1:hash1|topic2:hash2|..."
// This encoding is ASCII-safe and length-bounded by the number of topics and fragment size.
// Truncation is handled by parseBaseline returning a partial map, which fails safe toward swapping.
func encodeBaseline(topics []string, baseline map[string][]byte) string {
	var parts []string
	for _, topic := range topics {
		hash := hashFragment(baseline[topic])
		if hash != "" {
			parts = append(parts, fmt.Sprintf("%s:%s", topic, hash))
		}
	}
	return strings.Join(parts, "|")
}

// parseBaseline decodes a baseline set from an encoded string.
// Format: "topic1:hash1|topic2:hash2|..."
// Returns a map of topic -> hash. Truncated or malformed entries are skipped.
// Missing topics or failed parses fail safe toward swapping.
func parseBaseline(encoded string) map[string]string {
	result := make(map[string]string)
	if encoded == "" {
		return result
	}

	parts := strings.Split(encoded, "|")
	for _, part := range parts {
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, ":", 2)
		if len(kv) == 2 {
			result[kv[0]] = kv[1]
		}
	}
	return result
}
