package retinaws

import (
	"github.com/gorilla/websocket"
	"log"
	"sync"
)

type MessageHandler func(headers map[string][]string, body []byte) (map[string][]string, []byte)

func BackendServer(wsUrl string, workers int, handler MessageHandler, stop <-chan bool) {
	dialer := websocket.Dialer{ReadBufferSize: 2048, WriteBufferSize: 2048}
	ws, _, err := dialer.Dial(wsUrl, nil)
	if err != nil {
		log.Fatalln("BackendServer: Dial err", wsUrl, err)
	}

	if workers < 1 {
		workers = 1
	}

	log.Println("BackendServer: started")

	// messages outbound to retina
	// we always close this channel
	toRetina := make(chan *Message)

	// messages inbound from retina
	fromRetina := make(chan *Message)

	// start pump to send/receive data on websocket
	conn := NewConnection(ws, toRetina, fromRetina)

	// internal channel for worker goroutines
	toWorkers := make(chan *Message)

	workerWg := &sync.WaitGroup{}
	for i := 0; i < workers; i++ {
		workerWg.Add(1)
		go backendWorker(handler, workerWg, toWorkers, toRetina)
	}

	go conn.readPump()
	go func() {
		conn.writePump()
		ws.Close()
		log.Println("BackendServer: websocket closed")
	}()

	for {
		select {
		case msg, ok := <-fromRetina:
			if !ok {
				log.Println("BackendServer: fromRetina closed, stopping workers")
				close(toWorkers)
				workerWg.Wait()
				close(toRetina)
				return
			} else if msg.Type == websocket.BinaryMessage {
				toWorkers <- msg
			}
		case <-stop:
			log.Println("BackendServer: stop received")
			conn.stopRead()
		}
	}

	log.Println("BackendServer: exiting")
}

func backendWorker(handler MessageHandler, wg *sync.WaitGroup, in, out chan *Message) {
	defer wg.Done()
	for {
		msg, ok := <-in
		if !ok {
			log.Println("BackendServer: worker done")
			return
		}
		headers, body := ParseFrame(msg.Data)
		id, ok := headers["X-Hub-Id"]
		if !ok {
			log.Println("BackendServer: worker got request without X-Hub-Id header")
		} else {
			respHeaders, respBody := handler(headers, body)
			if respHeaders == nil {
				respHeaders = make(map[string][]string)
			}
			respHeaders["X-Hub-Id"] = id
			respFrame := WriteFrame(respHeaders, respBody)
			out <- &Message{Type: websocket.BinaryMessage, Data: respFrame}
		}
	}
}
