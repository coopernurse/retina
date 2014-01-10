package retinaws

import (
	"github.com/gorilla/websocket"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

// This code inspired by the chat example by Gary Burd here:
// https://github.com/gorilla/websocket/blob/master/examples/chat/conn.go
//
// But all bugs are mine

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 2 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 4) / 10

	// Maximum message size allowed from peer.
	maxMessageSize = 4096 * 1024
)

type Message struct {
	Type int
	Data []byte
}

func NewConnection(ws *websocket.Conn, send, recv chan *Message) *Connection {
	return &Connection{
		ws:   ws,
		send: send,
		recv: recv,
		stop: false,
		lock: &sync.Mutex{},
	}
}

func HandleConnection(ws *websocket.Conn, send, recv chan *Message) {
	conn := NewConnection(ws, send, recv)
	go conn.readPump()
	conn.writePump()
}

// Connection is an middleman between the websocket connection and the hub.
type Connection struct {
	// The websocket connection.
	ws *websocket.Conn

	// Buffered channel of outbound messages.
	// Caller should close this channel to terminate the ws connection
	send <-chan *Message

	// Buffered channel of inbound messages.
	// This channel will be closed by readPump if the ws connection is closed
	recv chan<- *Message

	lock *sync.Mutex
	stop bool
}

func (c *Connection) stopRead() {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.stop = true
}

func (c *Connection) reading() bool {
	c.lock.Lock()
	defer c.lock.Unlock()
	return !c.stop
}

// readPump pumps messages from the websocket connection to the hub.
func (c *Connection) readPump() {
	defer close(c.recv)

	c.ws.SetReadLimit(maxMessageSize)
	c.ws.SetReadDeadline(time.Now().Add(pongWait))
	c.ws.SetPongHandler(func(string) error {
		if c.reading() {
			c.ws.SetReadDeadline(time.Now().Add(pongWait))
		}
		return nil
	})
	for c.reading() {
		messageType, data, err := c.ws.ReadMessage()
		if err != nil {
			if err == io.EOF || !c.reading() && isTimeout(err) {
				// no msg
			} else {
				log.Println("retinaws: readPump unknown error: ", err)
			}
			break
		} else {
			c.recv <- &Message{Type: messageType, Data: data}
		}
	}
}

// write writes a message with the given message type and payload.
func (c *Connection) write(mt int, payload []byte) error {
	c.ws.SetWriteDeadline(time.Now().Add(writeWait))
	return c.ws.WriteMessage(mt, payload)
}

// writePump pumps messages from the hub to the websocket connection.
func (c *Connection) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.stopRead()
		log.Println("retinaws: writePump exiting")
	}()
	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				c.write(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.write(message.Type, message.Data); err != nil {
				log.Println("retinaws: writePump write unknown error: ", err)
				return
			}
		case <-ticker.C:
			if err := c.write(websocket.PingMessage, []byte{}); err != nil {
				log.Println("retinaws: writePump Ping unknown error: ", err)
				return
			}
		}
	}
}

func isTimeout(err error) bool {
	e, ok := err.(net.Error)
	return ok && e.Timeout()
}
