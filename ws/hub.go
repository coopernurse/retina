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
	Ack        chan bool
	ReplyTo    chan *Response
	Deadline   time.Time
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
	resend  int
}

func (me *Router) Destroy() {
	me.lock.Lock()
	defer me.lock.Unlock()
	for _, ch := range me.byQueue {
		close(ch)
	}
	me.byQueue = make(map[string]chan *Request)
}

func (me *Router) getQueueChannel(queue string) chan *Request {
	me.lock.Lock()
	defer me.lock.Unlock()

	ch, ok := me.byQueue[queue]
	if !ok {
		ch = make(chan *Request)
		me.byQueue[queue] = ch
	}
	return ch
}

func (me *Router) send(req *Request) {
	ch := me.getQueueChannel(req.Queue)
	ackTimeout := req.Deadline.Sub(time.Now()) / 3
	for time.Now().Before(req.Deadline) {
		select {
		case ch <- req:
			select {
			case <-req.Ack:
				return
			case <-time.After(ackTimeout):
				// re-send
				me.lock.Lock()
				me.resend++
				me.lock.Unlock()
				log.Println("router resend: ", me.resend, string(req.Body))
			}
		case <-time.After(time.Second):
			// try again
		}
	}
}

////////////////////////////////////////////

var timeoutResponse = &Response{
	HTTPStatus: 504,
	Body:       []byte("Request timed out"),
}

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
		r, err := me.fromHttpRequest(queue, req)
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "Error reading req: %v", err)
		} else {
			resp := me.send(r)
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

func (me *External) send(req *Request) *Response {
	me.Router.send(req)

	select {
	case res := <-req.ReplyTo:
		return res
	case <-time.After(req.Deadline.Sub(time.Now())):
		return timeoutResponse
	}
}

func (me *External) fromHttpRequest(queue string, hr *http.Request) (*Request, error) {
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
		Ack:        make(chan bool, 1),
		ReplyTo:    make(chan *Response, 1),
		Deadline:   time.Now().Add(me.Timeout),
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
	queues := parseQueues(r.RequestURI)
	if len(queues) < 1 {
		http.Error(w, fmt.Sprintf("Invalid URI: %s - Must specify at least one queue", r.RequestURI), 400)
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
		ch := me.Router.getQueueChannel(queue)
		log.Println("registering with queue:", queue)
		channels[i+1] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ch)}
	}

	prefix := RandHex(8) + "_"
	count := 0
	requestMap := make(map[string]*Request)

	reapRequestMapInterval := 5 * time.Minute
	nextReap := time.Now().Add(reapRequestMapInterval)

	for {
		now := time.Now()
		if now.After(nextReap) {
			for id, req := range requestMap {
				if req.Deadline.Before(now) {
					log.Println("retinaws: removing timed out request:", id)
					delete(requestMap, id)
				}
			}
			nextReap = time.Now().Add(reapRequestMapInterval)
		}

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
						op, ok := headers["X-Hub-ControlOp"]
						if ok && len(op) > 0 && op[0] == "ack" {
							req.Ack <- true
						} else {
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
						}
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

	queues := make([]string, 0)
	for _, s := range strings.Split(uri, ",") {
		if s != "" {
			queues = append(queues, s)
		}
	}
	return queues
}
