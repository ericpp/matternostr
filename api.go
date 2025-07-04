package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// MatterbridgeMessage represents a message from/to Matterbridge
type MatterbridgeMessage struct {
	Text     string `json:"text"`
	Username string `json:"username"`
	Gateway  string `json:"gateway"`
	Event    string `json:"event,omitempty"`
}

// MessageHandler is a callback function type for handling messages from Matterbridge
type MessageHandler func(msg MatterbridgeMessage) error

// MatterbridgeClient handles all communication with Matterbridge
type MatterbridgeClient struct {
	url              string
	apiToken         string
	logger           *logrus.Logger
	httpClient       *http.Client
	streamHTTPClient *http.Client
	stopChan         chan struct{}
	wg               sync.WaitGroup
}

// NewMatterbridgeClient creates a new Matterbridge client
func NewMatterbridgeClient(url string, apiToken string, logger *logrus.Logger) *MatterbridgeClient {
	return &MatterbridgeClient{
		url:              url,
		apiToken:         apiToken,
		logger:           logger,
		httpClient:       &http.Client{Timeout: 30 * time.Second},
		streamHTTPClient: &http.Client{}, // No timeout for streaming
		stopChan:         make(chan struct{}),
	}
}

// Start begins the Matterbridge client operations
func (c *MatterbridgeClient) Start(messageHandler MessageHandler) {
	c.wg.Add(1)
	go c.streamMessages(messageHandler)
}

// Stop stops the Matterbridge client
func (c *MatterbridgeClient) Stop() {
	c.logger.Debug("Stopping Matterbridge client...")
	close(c.stopChan)
	c.wg.Wait()
}

// SendMessage sends a message to Matterbridge
func (c *MatterbridgeClient) SendMessage(msg MatterbridgeMessage) error {
	jsonData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	req, err := http.NewRequest("POST", c.url+"/api/message", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

// streamMessages continuously streams for messages from Matterbridge
func (c *MatterbridgeClient) streamMessages(messageHandler MessageHandler) {
	defer c.wg.Done()
	c.logger.Info("Streaming Matterbridge messages...")

	retryDelay := 5 * time.Second
	maxRetryDelay := 60 * time.Second

	for {
		select {
		case <-c.stopChan:
			return
		default:
			if err := c.doStreamMessages(messageHandler); err != nil {
				c.logger.Errorf("Failed to stream messages from Matterbridge: %v", err)
				c.logger.Infof("Retrying in %v...", retryDelay)

				select {
				case <-c.stopChan:
					return
				case <-time.After(retryDelay):
					// Exponential backoff
					retryDelay *= 2
					if retryDelay > maxRetryDelay {
						retryDelay = maxRetryDelay
					}
				}
			} else {
				// Reset retry delay on successful connection
				retryDelay = 5 * time.Second
			}
		}
	}
}

// doStreamMessages streams messages from Matterbridge and calls the handler for each message
func (c *MatterbridgeClient) doStreamMessages(messageHandler MessageHandler) error {
	req, err := http.NewRequest("GET", c.url+"/api/stream", nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	if c.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiToken)
	}

	// Create a context that gets cancelled when stopChan is closed
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel the context when stopChan is closed
	go func() {
		select {
		case <-c.stopChan:
			cancel()
		case <-ctx.Done():
		}
	}()

	req = req.WithContext(ctx)

	resp, err := c.streamHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	c.logger.Info("Connected to Matterbridge stream")
	decoder := json.NewDecoder(resp.Body)

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("Stopping stream")
			return nil
		default:
			var msg MatterbridgeMessage
			if err := decoder.Decode(&msg); err != nil {
				if err.Error() == "EOF" || ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("failed to decode message: %w", err)
			}

			// Skip empty messages and connection events
			if msg.Username == "" || msg.Text == "" || msg.Event == "api_connected" {
				continue
			}

			js, _ := json.Marshal(msg)
			c.logger.Infof("Received message from Matterbridge: %s", string(js))

			// Call the message handler
			if err := messageHandler(msg); err != nil {
				c.logger.Errorf("Message handler failed: %v", err)
			}
		}
	}
}
