/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package server

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/metrics"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 4096
	sendBufSize    = 256
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// WSMessage is the envelope for all WebSocket messages.
type WSMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data,omitempty"`
}

// Hub manages WebSocket clients and topic-based broadcasting.
type Hub struct {
	mu         sync.RWMutex
	clients    map[*WSClient]struct{}
	register   chan *WSClient
	unregister chan *WSClient
	log        *logrus.Entry
}

// WSClient represents a single WebSocket connection.
type WSClient struct {
	hub    *Hub
	conn   *websocket.Conn
	send   chan []byte
	topics map[string]bool
	mu     sync.Mutex
}

func newHub(log *logrus.Entry) *Hub {
	return &Hub{
		clients:    make(map[*WSClient]struct{}),
		register:   make(chan *WSClient),
		unregister: make(chan *WSClient),
		log:        log.WithField("component", "ws-hub"),
	}
}

// Run processes register/unregister events. Must be called as a goroutine.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = struct{}{}
			h.mu.Unlock()
			metrics.WebSocketConnections.Inc()
			h.log.WithField("clients", h.clientCount()).Debug("Client connected")

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				metrics.WebSocketConnections.Dec()
			}
			h.mu.Unlock()
			h.log.WithField("clients", h.clientCount()).Debug("Client disconnected")
		}
	}
}

func (h *Hub) clientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// BroadcastJSON sends a message to all clients subscribed to the given topic.
func (h *Hub) BroadcastJSON(topic string, data interface{}) {
	msg := WSMessage{Type: topic, Data: data}
	bytes, err := json.Marshal(msg)
	if err != nil {
		h.log.WithError(err).Warn("Failed to marshal broadcast message")
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for client := range h.clients {
		client.mu.Lock()
		subscribed := client.topics[topic]
		client.mu.Unlock()

		if !subscribed {
			continue
		}

		select {
		case client.send <- bytes:
		default:
			// Client send buffer full, drop message
		}
	}
}

func (c *WSClient) readPump() {
	defer func() {
		c.hub.unregister <- c
		_ = c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			break
		}

		var msg struct {
			Type  string `json:"type"`
			Topic string `json:"topic"`
		}
		if err := json.Unmarshal(message, &msg); err != nil {
			continue
		}

		c.mu.Lock()
		switch msg.Type {
		case "subscribe":
			if msg.Topic != "" {
				c.topics[msg.Topic] = true
			}
		case "unsubscribe":
			delete(c.topics, msg.Topic)
		case "ping":
			// Client-level ping, respond with pong
			select {
			case c.send <- mustMarshal(WSMessage{Type: "pong"}):
			default:
			}
		}
		c.mu.Unlock()
	}
}

func (c *WSClient) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func mustMarshal(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

// handleWebSocket upgrades HTTP to WebSocket and registers the client.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.WithError(err).Warn("WebSocket upgrade failed")
		return
	}

	client := &WSClient{
		hub:    s.hub,
		conn:   conn,
		send:   make(chan []byte, sendBufSize),
		topics: make(map[string]bool),
	}

	s.hub.register <- client

	go client.writePump()
	go client.readPump()
}
