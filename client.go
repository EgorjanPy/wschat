// Copyright 2013 The Gorilla WebSocket Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// Время, отведенное на написание сообщения партнеру.
	writeWait = 10 * time.Second

	// Время, отведенное на прочтение очередного сообщения от партнера.
	pongWait = 60 * time.Second

	// Посылать пинги на пир с этим периодом. Должно быть меньше pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Максимально допустимый размер сообщения.
	maxMessageSize = 512
)

var (
	newline = []byte{'\n'}
	space   = []byte{' '}
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// Клиент является посредником между соединением WebSocket и хабом.
type Client struct {
	hub *Hub

	// Соединение через веб-сокет.
	conn *websocket.Conn

	// Буферизованный канал исходящих сообщений.
	send chan []byte
}

// Приложение запускает readPump в goroutine для каждого соединения. Приложение
// гарантирует, что в соединении есть не более одного читателя, выполняя все
// чтения из этой goroutine.
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(
		func(string) error {
			c.conn.SetReadDeadline(time.Now().Add(pongWait))
			return nil
		})
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}
		message = bytes.TrimSpace(bytes.Replace(message, newline, space, -1))
		c.hub.broadcast <- message
	}
}

// writePump перекачивает сообщения из хаба в соединение WebSocket.
//
// Для каждого соединения запускается goroutine, запускающая writePump.
// Приложение гарантирует, что в соединении есть не более одного писателя,
// выполняя все записи из этой goroutine.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Хаб закрыл канал.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Добавить сообщения чата из очереди к текущему сообщению веб-сокета.
			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write(newline)
				w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// serveWs обрабатывает запросы websocket от пира.
func serveWs(hub *Hub, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	client := &Client{hub: hub, conn: conn, send: make(chan []byte, 256)}
	client.hub.register <- client

	// Разрешить сбор памяти, на которую ссылается вызывающий, выполняя всю работу в
	// новых горутинах.
	go client.writePump()
	go client.readPump()
}
