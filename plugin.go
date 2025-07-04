package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/nip53"
	"github.com/sirupsen/logrus"
)

type NostrPlugin struct {
	config             *Config
	logger             *logrus.Logger
	matterbridgeClient *MatterbridgeClient
	rooms              map[string]*NostrRoom
	publicKeyHex       string
	privateKeyHex      string
	watchNpubHex       string
	stopChan           chan struct{}
	wg                 sync.WaitGroup
	mu                 sync.RWMutex
	relays             *NostrRelays
}

type NostrRoom struct {
	addr  string
	naddr string
	ctx   context.Context
	stop  context.CancelFunc
}

func NewNostrPlugin(config *Config, logger *logrus.Logger) *NostrPlugin {
	return &NostrPlugin{
		config:             config,
		logger:             logger,
		matterbridgeClient: NewMatterbridgeClient(config.MatterbridgeURL, config.APIToken, logger),
		rooms:              make(map[string]*NostrRoom),
		stopChan:           make(chan struct{}),
		relays:             NewNostrRelays(context.Background(), config.DefaultRelays, config.MaxRetryAttempts, time.Duration(config.RetryTimeoutMinutes)*time.Minute, logger),
	}
}

func (p *NostrPlugin) Start() error {
	if err := p.setupNostrKeys(); err != nil {
		return fmt.Errorf("failed to setup Nostr keys: %w", err)
	}

	if err := p.setupWatchNpub(); err != nil {
		return fmt.Errorf("failed to setup watch npub: %w", err)
	}

	p.wg.Add(1)
	go p.discoverLiveEvents()

	// Start Matterbridge client with message handler
	p.matterbridgeClient.Start(p.handleMatterbridgeMessage)

	return nil
}

func (p *NostrPlugin) Stop() {
	p.logger.Info("Stopping Nostr relays...")
	p.relays.Close()

	p.logger.Info("Stopping Matterbridge client...")
	p.matterbridgeClient.Stop()

	p.logger.Info("Closing stop channel...")
	close(p.stopChan)

	p.logger.Info("Waiting for goroutines to finish...")
	p.wg.Wait()

	p.logger.Info("Stopping rooms...")
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, room := range p.rooms {
		room.stop()
	}
}

func (p *NostrPlugin) setupNostrKeys() error {
	prefix, pubHex, err := nip19.Decode(p.config.PublicKey)
	if err != nil || prefix != "npub" {
		return fmt.Errorf("invalid public key format: %w", err)
	}
	p.publicKeyHex = pubHex.(string)

	prefix, secHex, err := nip19.Decode(p.config.PrivateKey)
	if err != nil || prefix != "nsec" {
		return fmt.Errorf("invalid private key format: %w", err)
	}
	p.privateKeyHex = secHex.(string)

	p.logger.Debugf("Initialized with pubkey: %s", p.publicKeyHex)
	return nil
}

func (p *NostrPlugin) setupWatchNpub() error {
	prefix, pubHex, err := nip19.Decode(p.config.WatchNpub)
	if err != nil || prefix != "npub" {
		return fmt.Errorf("invalid watch npub format: %w", err)
	}
	p.watchNpubHex = pubHex.(string)

	p.logger.Infof("Auto-discovery enabled for npub: %s", p.config.WatchNpub)
	return nil
}

func (p *NostrPlugin) discoverLiveEvents() {
	defer p.wg.Done()
	p.logger.Debug("Discovering live events...")

	events := p.relays.StreamLiveEvents(p.watchNpubHex)

	for ev := range events {
		if ev == nil {
			continue
		}

		liveEvent := nip53.ParseLiveEvent(*ev)
		naddr, err := nip19.EncodeEntity(ev.PubKey, ev.Kind, liveEvent.Identifier, liveEvent.Relays)
		if err != nil {
			p.logger.Warnf("Failed to make naddr for event %s: %v", ev.ID, err)
			continue
		}

		addr := fmt.Sprintf("%v:%v:%v", ev.Kind, ev.PubKey, liveEvent.Identifier)

		if liveEvent.Status == "live" {
			if p.joinLiveEvent(naddr, addr) {
				p.logger.Infof("Joined live event: %s", liveEvent.Title)
				p.sendJoinEventToMatterbridge(naddr, liveEvent)
			}
		} else {
			if p.leaveLiveEvent(naddr) {
				p.logger.Infof("Left live event: %s", liveEvent.Title)
				p.sendLeaveEventToMatterbridge(naddr, liveEvent)
			}
		}
	}
}

func (p *NostrPlugin) joinLiveEvent(naddr string, addr string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.rooms[naddr]; exists {
		return false
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.rooms[naddr] = &NostrRoom{
		addr:  addr,
		naddr: naddr,
		ctx:   ctx,
		stop:  cancel,
	}

	p.wg.Add(1)
	go p.streamingMessages(p.rooms[naddr])

	return true
}

func (p *NostrPlugin) leaveLiveEvent(naddr string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	room, exists := p.rooms[naddr]
	if !exists {
		return false
	}

	room.stop()
	delete(p.rooms, naddr)

	return true
}

func (p *NostrPlugin) streamingMessages(room *NostrRoom) {
	defer p.wg.Done()
	p.logger.Infof("Streaming messages from %s", room.addr)

	events := p.relays.StreamLiveChatMessages(room.addr)

	for {
		select {
		case <-room.ctx.Done():
			return
		case <-p.stopChan:
			return
		case ev := <-events:
			if ev == nil || ev.PubKey == p.publicKeyHex {
				continue
			}

			js, _ := json.Marshal(ev)
			p.logger.Infof("Received event: %s", string(js))

			err := p.sendNostrEventToMatterbridge(ev)

			if err != nil {
				p.logger.Errorf("Failed to handle event: %v", err)
			}
		}
	}
}

func (p *NostrPlugin) sendJoinEventToMatterbridge(naddr string, liveEvent nip53.LiveEvent) error {
	mbMsg := MatterbridgeMessage{
		Text:     "Joined " + liveEvent.Title + ": https://nostrudel.ninja/streams/" + naddr,
		Username: "system",
		Gateway:  p.config.Gateway,
	}

	if err := p.matterbridgeClient.SendMessage(mbMsg); err != nil {
		p.logger.Errorf("Failed to send message to Matterbridge: %v", err)
	}

	return nil
}

func (p *NostrPlugin) sendLeaveEventToMatterbridge(naddr string, liveEvent nip53.LiveEvent) error {
	mbMsg := MatterbridgeMessage{
		Text:     "Left " + liveEvent.Title + ": https://nostrudel.ninja/streams/" + naddr,
		Username: "system",
		Gateway:  p.config.Gateway,
	}

	if err := p.matterbridgeClient.SendMessage(mbMsg); err != nil {
		p.logger.Errorf("Failed to send message to Matterbridge: %v", err)
	}

	return nil
}

func (p *NostrPlugin) sendNostrEventToMatterbridge(ev *nostr.Event) error {
	msg, err := NostrMessageFromEvent(ev, p.relays)

	if err != nil {
		return err
	}

	mbMsg := MatterbridgeMessage{
		Text:     msg.Content,
		Username: msg.Nick,
		Gateway:  p.config.Gateway,
	}

	if err := p.matterbridgeClient.SendMessage(mbMsg); err != nil {
		p.logger.Errorf("Failed to send message to Matterbridge: %v", err)
	}

	return nil
}

// handleMatterbridgeMessage handles messages received from Matterbridge
func (p *NostrPlugin) handleMatterbridgeMessage(msg MatterbridgeMessage) error {
	p.mu.RLock()
	rooms := make(map[string]*NostrRoom)
	for k, v := range p.rooms {
		rooms[k] = v
	}
	p.mu.RUnlock()

	js, _ := json.Marshal(msg)
	p.logger.Infof("Sending message to Nostr: %s", string(js))

	for _, room := range rooms {
		if err := p.sendMatterbridgeMessageToNostr(msg, room); err != nil {
			p.logger.Errorf("Failed to send message to Nostr: %v", err)
		}
	}

	return nil
}

func (p *NostrPlugin) sendMatterbridgeMessageToNostr(msg MatterbridgeMessage, room *NostrRoom) error {
	event := nostr.Event{
		PubKey:    p.publicKeyHex,
		CreatedAt: nostr.Now(),
		Kind:      nostr.KindLiveChatMessage,
		Tags:      nostr.Tags{{"a", room.addr}},
		Content:   msg.Username + ": " + msg.Text,
	}

	if err := event.Sign(p.privateKeyHex); err != nil {
		return fmt.Errorf("failed to sign event: %w", err)
	}

	published := p.relays.Publish(event)
	if published == 0 {
		return fmt.Errorf("failed to publish event to any relay")
	}

	return nil
}
