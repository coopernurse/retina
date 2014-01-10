package retinaws

import (
	"bytes"
	"fmt"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"log"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Request struct {
	Queue      string
	HTTPMethod string
	HTTPURI    string
	Headers    map[string][]string
	Body       []byte
	ReplyTo    chan *Response
}

type Response struct {
	HTTPStatus int
	Headers    map[string][]string
	Body       []byte
}

////////////////////////////////////////////

func NewRouter() *Router {
	return &Router{
		byQueue: make(map[string]chan *Request),
		lock:    &sync.Mutex{},
	}
}

type Router struct {
	byQueue map[string]chan *Request
	lock    *sync.Mutex
}

func (me *Router) Destroy() {
	me.lock.Lock()
	defer me.lock.Unlock()
	for _, ch := range me.byQueue {
		close(ch)
	}
	me.byQueue = make(map[string]chan *Request)
}

func (me *Router) GetQueueChannel(queue string) chan *Request {
	me.lock.Lock()
	defer me.lock.Unlock()

	ch, ok := me.byQueue[queue]
	if !ok {
		ch = make(chan *Request)
		me.byQueue[queue] = ch
	}
	return ch
}

////////////////////////////////////////////

type External struct {
	Router  *Router
	Timeout time.Duration
}

func (me *External) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	queue, ok := vars["queue"]
	if queue == "" || !ok {
		fmt.Fprintf(w, "queue is undefined on URL")
	} else {
		r, err := fromHttpRequest(queue, req)
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "Error reading req: %v", err)
		} else {
			resp := me.process(r, me.Timeout)
			if resp.Headers != nil {
				headers := w.Header()
				for name, val := range resp.Headers {
					if !strings.HasPrefix(name, "X-Hub-") {
						headers[name] = val
					}
				}
			}
			status := resp.HTTPStatus
			if status == 0 {
				status = 200
			}
			w.WriteHeader(status)
			w.Write(resp.Body)
		}
	}
}

func (me *External) process(req *Request, timeout time.Duration) *Response {
	ch := me.Router.GetQueueChannel(req.Queue)

	// send - may block waiting for backends to attach
	select {
	case ch <- req:
		// ok
	case <-time.After(timeout):
		return &Response{
			HTTPStatus: 504,
			Body:       []byte("Request timed out"),
		}
	}

	// sent - wait for response
	ch <- req
	select {
	case res := <-req.ReplyTo:
		return res
	case <-time.After(timeout):
		return &Response{
			HTTPStatus: 504,
			Body:       []byte("Request timed out"),
		}
	}
}

func fromHttpRequest(queue string, hr *http.Request) (*Request, error) {
	buf := bytes.Buffer{}
	_, err := buf.ReadFrom(hr.Body)
	if err != nil {
		return nil, err
	}

	return &Request{
		HTTPMethod: hr.Method,
		HTTPURI:    hr.RequestURI,
		Queue:      queue,
		Headers:    hr.Header,
		Body:       buf.Bytes(),
		ReplyTo:    make(chan *Response, 1),
	}, nil
}

////////////////////////////////////////////

func NewInternal() *Internal {
	return &Internal{
		Router: NewRouter(),
	}
}

type Internal struct {
	Router *Router
}

func (me *Internal) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Println("URI:", r.RequestURI)
	queues := parseQueues(r.RequestURI)
	if len(queues) < 1 {
		http.Error(w, "Invalid URI. Must specify at least one queue", 400)
		return
	}

	ws, err := websocket.Upgrade(w, r, nil, 2048, 2048)
	if _, ok := err.(websocket.HandshakeError); ok {
		http.Error(w, "Not a websocket handshake", 400)
		return
	} else if err != nil {
		log.Println("some issue here", err)
		http.Error(w, "Unknown server error", 500)
		return
	}

	// messages outbound to backend process
	// we always close this channel
	send := make(chan *Message)
	defer close(send)

	// messages inbound from backend process
	recv := make(chan *Message)

	// start the connection handler, which manages
	// reading/writing to the websocket connection
	go HandleConnection(ws, send, recv)

	channels := make([]reflect.SelectCase, len(queues)+1)
	channels[0] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(recv)}

	for i, queue := range queues {
		ch := me.Router.GetQueueChannel(queue)
		log.Println("registering with queue:", queue)
		channels[i+1] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ch)}
	}

	prefix := RandHex(8) + "_"
	count := 0
	requestMap := make(map[string]*Request)

	for {
		chosen, value, ok := reflect.Select(channels)
		if !ok {
			log.Println("retinaws: reflect.Select returned closed channel - exiting:", chosen)
			return
		}

		if chosen == 0 {
			// Message from backend process
			msg, ok := value.Interface().(*Message)
			if !ok {
				log.Printf("retinaws: ERROR chosen=0 but value is not *Message: %+v", value.Interface())
				return
			}

			//log.Println("Message from backend: ", msg.MessageType, string(msg.Data), msg.Error)
			if msg.Type == websocket.BinaryMessage {
				headers, body := ParseFrame(msg.Data)
				ids, ok := headers["X-Hub-Id"]
				if ok && len(ids) > 0 {
					id := ids[0]
					req, ok := requestMap[id]
					if ok {
						statusCode := 200
						status, ok := headers["X-Hub-Status"]
						if ok && len(status) > 0 {
							statusCode, _ = strconv.Atoi(status[0])
						}
						req.ReplyTo <- &Response{
							HTTPStatus: statusCode,
							Headers:    headers,
							Body:       body,
						}
						delete(requestMap, id)
					} else {
						log.Printf("retinaws: request not found with id: %s", id)
					}
				} else {
					log.Printf("retinaws: X-Hub-Id header not found: %v", headers)
				}
			} else {
				log.Println("retinaws: Unknown Message from backend: ", msg.Type, string(msg.Data))
			}
		} else {
			// Inbound HTTP request from external
			req, ok := value.Interface().(*Request)
			if !ok {
				log.Printf("retinaws: value is not *Request: %+v", value.Interface())
				return
			}

			count++
			if count < 0 {
				count = 0
			}
			id := prefix + strconv.Itoa(count)
			requestMap[id] = req

			headers := req.Headers
			headers["X-Hub-Id"] = []string{id}
			headers["X-Hub-Queue"] = []string{req.Queue}
			frame := WriteFrame(headers, req.Body)
			send <- &Message{Type: websocket.BinaryMessage, Data: frame}
		}
	}

}

func parseQueues(uri string) []string {
	if uri == "" {
		return []string{}
	}

	if uri[0] == '/' {
		uri = uri[1:]
	}

	return strings.Split(uri, ",")
}
