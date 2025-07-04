package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/sirupsen/logrus"
)

type ProfileMetadata struct {
	Name                  string `json:"name"`
	DisplayName           string `json:"display_name,omitempty"`
	DeprecatedDisplayName string `json:"displayName,omitempty"`
}

type NostrMessage struct {
	Content string
	Nick    string
}

func NostrMessageFromEvent(ev *nostr.Event, relays *NostrRelays) (*NostrMessage, error) {
	if ev.Kind == nostr.KindZap {
		description := ev.Tags.Find("description").Value()
		if description == "" {
			return nil, fmt.Errorf("no description found")
		}

		descriptionJSON := description
		var zapRequest nostr.Event
		if err := json.Unmarshal([]byte(descriptionJSON), &zapRequest); err != nil {
			return nil, fmt.Errorf("failed to unmarshal zap request: %w", err)
		}

		nick := relays.GetNick(zapRequest.PubKey)
		amount := zapRequest.Tags.Find("amount").Value()
		amountInt, err := strconv.ParseInt(amount, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to convert amount to int: %w", err)
		}

		text := "⚡ zapped " + strconv.FormatInt(amountInt/1000, 10) + " sats"

		if zapRequest.Content != "" {
			text += " saying: " + zapRequest.Content
		}

		return &NostrMessage{
			Content: text,
			Nick:    nick,
		}, nil
	} else {
		nick := relays.GetNick(ev.PubKey)

		return &NostrMessage{
			Content: ev.Content,
			Nick:    nick,
		}, nil
	}
}

type NostrRelay struct {
	url         string
	relay       *nostr.Relay
	failures    int
	nextAttempt time.Time
	maxRetries  int
	retryDelay  time.Duration
}

func NewNostrRelay(ctx context.Context, url string, maxRetries int, retryDelay time.Duration) *NostrRelay {
	relay, err := nostr.RelayConnect(ctx, url)
	if err != nil {
		return nil
	}

	return &NostrRelay{
		url:         url,
		relay:       relay,
		failures:    0,
		nextAttempt: time.Now(),
		maxRetries:  maxRetries,
		retryDelay:  retryDelay,
	}
}

func (r *NostrRelay) reconnect(ctx context.Context) error {
	r.relay.Close()
	newRelay, err := nostr.RelayConnect(ctx, r.url)
	if err != nil {
		return err
	}
	r.relay = newRelay
	return nil
}

func (r *NostrRelay) GetNick(ctx context.Context, npub string) (string, error) {
	filters := nostr.Filters{{
		Kinds:   []int{nostr.KindProfileMetadata},
		Authors: []string{npub},
		Limit:   1,
	}}

	sub, err := r.relay.Subscribe(ctx, filters)
	if err != nil {
		return "", err
	}
	defer sub.Close()

	for ev := range sub.Events {
		var pm ProfileMetadata
		if err := json.Unmarshal([]byte(ev.Content), &pm); err != nil {
			continue
		}

		// Return first non-empty display name
		for _, name := range []string{pm.DisplayName, pm.DeprecatedDisplayName, pm.Name} {
			if name != "" {
				return name, nil
			}
		}
	}

	return "", nil
}

func (r *NostrRelay) StreamEvents(ctx context.Context, filters nostr.Filters, events chan *nostr.Event) {
	sub, err := r.relay.Subscribe(ctx, filters)
	if err != nil {
		return
	}
	defer sub.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.Events:
			if !ok {
				// Channel closed
				return
			}
			if ev != nil {
				// fmt.Println("Event received", ev)
				events <- ev
			}
		}
	}
}

func (r *NostrRelay) Publish(ctx context.Context, event nostr.Event) error {
	// Check cooldown
	if time.Now().Before(r.nextAttempt) {
		return fmt.Errorf("relay in cooldown")
	}

	err := r.relay.Publish(ctx, event)
	if err == nil {
		r.failures = 0
		return nil
	}

	// Try reconnect and publish again
	if reconnectErr := r.reconnect(ctx); reconnectErr == nil {
		if err = r.relay.Publish(ctx, event); err == nil {
			r.failures = 0
			return nil
		}
	}

	// Handle failure
	r.failures++
	if r.failures >= r.maxRetries {
		r.nextAttempt = time.Now().Add(r.retryDelay)
	}

	return err
}

func (r *NostrRelay) Close() {
	r.relay.Close()
}

type NostrRelays struct {
	relays     []*NostrRelay
	logger     *logrus.Logger
	maxRetries int
	retryDelay time.Duration
	ctx        context.Context
	cancel     context.CancelFunc
}

func NewNostrRelays(ctx context.Context, urls []string, maxRetries int, retryDelay time.Duration, logger *logrus.Logger) *NostrRelays {
	ctx, cancel := context.WithCancel(ctx)
	relays := make([]*NostrRelay, 0, len(urls))

	for _, url := range urls {
		if relay := NewNostrRelay(ctx, url, maxRetries, retryDelay); relay != nil {
			relays = append(relays, relay)
			logger.Infof("Connected to relay: %s", url)
		} else {
			logger.Warnf("Failed to connect to relay: %s", url)
		}
	}

	return &NostrRelays{
		relays:     relays,
		maxRetries: maxRetries,
		retryDelay: retryDelay,
		logger:     logger,
		ctx:        ctx,
		cancel:     cancel,
	}
}

func (r *NostrRelays) GetNick(npub string) string {
	for _, relay := range r.relays {
		if nick, err := relay.GetNick(r.ctx, npub); err == nil && nick != "" {
			return nick
		}
	}
	return "unknown"
}

func (r *NostrRelays) StreamEvents(filters nostr.Filters) chan *nostr.Event {
	r.logger.Infof("Subscribing to messages with filters %v", filters)

	rawEvents := make(chan *nostr.Event, 100)
	deduplicatedEvents := make(chan *nostr.Event, 100)

	// Start streaming from all relays
	for _, relay := range r.relays {
		if relay != nil {
			go relay.StreamEvents(r.ctx, filters, rawEvents)
			r.logger.Infof("StreamEvents started for relay %s", relay.url)
		}
	}

	// Handle deduplication
	go func() {
		defer close(deduplicatedEvents)
		handled := make(map[string]bool)

		for {
			select {
			case <-r.ctx.Done():
				return
			case ev, ok := <-rawEvents:
				if !ok {
					// Channel closed
					return
				}
				if ev == nil || handled[ev.ID] {
					continue
				}

				handled[ev.ID] = true

				select {
				case deduplicatedEvents <- ev:
				case <-r.ctx.Done():
					return
				}

				// Simple cleanup when map gets too large
				if len(handled) > 1000 {
					// Clear half the entries
					count := 0
					for id := range handled {
						delete(handled, id)
						count++
						if count >= 500 {
							break
						}
					}
				}
			}
		}
	}()

	return deduplicatedEvents
}

func (r *NostrRelays) StreamLiveEvents(npub string) chan *nostr.Event {
	ts := time.Now().Add(-24 * time.Hour)
	since := nostr.Timestamp(ts.Unix())

	// Query for live events from the watched npub
	filters := nostr.Filters{{
		Kinds:   []int{nostr.KindLiveEvent},
		Authors: []string{npub},
		Since:   &since, // Look back 24 hours
	}}

	return r.StreamEvents(filters)
}

func (r *NostrRelays) StreamLiveChatMessages(addr string) chan *nostr.Event {
	timestamp := nostr.Timestamp(time.Now().Unix())

	filters := nostr.Filters{{
		Kinds: []int{nostr.KindLiveChatMessage, nostr.KindZap},
		Tags:  nostr.TagMap{"a": {addr}},
		Since: &timestamp,
	}}

	return r.StreamEvents(filters)
}

func (r *NostrRelays) Publish(event nostr.Event) int {
	r.logger.Infof("Publishing event to %d relays", len(r.relays))
	published := 0

	for _, relay := range r.relays {
		if err := relay.Publish(r.ctx, event); err == nil {
			published++
		} else {
			r.logger.Warnf("Failed to publish to %s: %v", relay.url, err)
		}
	}

	return published
}

func (r *NostrRelays) Close() {
	for _, relay := range r.relays {
		relay.Close()
	}
	r.cancel()
}
