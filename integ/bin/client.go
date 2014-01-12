package main

import (
	"bytes"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	mrand "math/rand"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

func main() {
	var url string
	var cmd string
	var concur int
	var seconds int
	flag.StringVar(&url, "u", "", "Base HTTP URL")
	flag.StringVar(&cmd, "m", "all", "Command to run (echo, add, sleep, all) - all randomly selects from the others per-request")
	flag.IntVar(&concur, "c", 1, "Concurrency")
	flag.IntVar(&seconds, "s", 5, "Test duration (seconds)")
	flag.Parse()

	if url == "" {
		fmt.Println("-u flag not provided")
		os.Exit(1)
	}

	if concur < 1 {
		concur = 1
	}

	mrand.Seed(time.Now().UnixNano())

	wg := &sync.WaitGroup{}

	run := make(chan bool)
	req := make(chan error)
	res := make(chan *Result, 1)

	go accum(req, res)
	for i := 0; i < concur; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for {
				_, ok := <-run
				if !ok {
					return
				}

				err := runOnce(url, cmd)
				req <- err
			}
		}()
	}

	deadline := time.Tick(time.Duration(seconds) * time.Second)
	running := true

	start := time.Now().UnixNano()

	for running {
		select {
		case <-deadline:
			running = false
		default:
			run <- true
		}
	}

	close(run)
	wg.Wait()
	elapsed := time.Now().UnixNano() - start

	close(req)
	result := <-res

	reqSec := float64(result.reqCount) / (float64(elapsed) / 1e9)

	fmt.Printf("Requests: %d\n", result.reqCount)
	fmt.Printf("  Errors: %d\n", result.errCount)
	fmt.Printf(" Req/sec: %.1f\n", reqSec)
}

type Result struct {
	reqCount int
	errCount int
}

func accum(reqs chan error, result chan *Result) {
	reqCount := 0
	errCount := 0
	for {
		err, ok := <-reqs
		if !ok {
			break
		} else {
			reqCount++
			if err != nil {
				errCount++
			}
		}
	}

	result <- &Result{reqCount, errCount}
}

var cmds = []string{"echo", "add", "sleep"}

func runOnce(url string, cmd string) error {
	if cmd == "all" {
		cmd = cmds[mrand.Intn(3)]
	}

	switch cmd {
	case "echo":
		return echo(url)
	case "add":
		return add(url)
	default:
		return sleep(url)
	}
}

func echo(url string) error {
	s := RandHex(10)
	resp, err := HTTPReq("POST", url+"/echo", "", nil, bytes.NewBufferString(s))
	if err == nil && string(resp) != s {
		err = fmt.Errorf("echo: %s != %s", string(resp), s)
	}
	return err
}

func add(url string) error {
	a := mrand.Intn(50000)
	b := mrand.Intn(50000)
	c := a + b
	s := fmt.Sprintf("%d,%d", a, b)
	resp, err := HTTPReq("POST", url+"/add", "", nil, bytes.NewBufferString(s))
	if err == nil {
		respInt, _ := strconv.Atoi(string(resp))
		if respInt != c {
			err = fmt.Errorf("add: %d != %d", respInt, c)
		}
	}
	return err
}

func sleep(url string) error {
	sleepMillis := mrand.Intn(5000) + 50
	x := RandHex(10)
	s := fmt.Sprintf("%d,%s", sleepMillis, x)
	resp, err := HTTPReq("POST", url+"/sleep", "", nil, bytes.NewBufferString(s))
	if err == nil && string(resp) != s {
		err = fmt.Errorf("sleep: %s != %s", string(resp), s)
	}
	return err
}

func RandHex(bytes int) string {
	buf := make([]byte, bytes)
	io.ReadFull(rand.Reader, buf)
	return fmt.Sprintf("%x", buf)
}

func HTTPReq(method, url, opaque string, headers map[string]string, reqData io.Reader) ([]byte, error) {
	req, err := http.NewRequest(method, url, reqData)
	if err != nil {
		return nil, err
	}

	if opaque != "" {
		req.URL.Opaque = opaque
	}

	if headers != nil {
		for k, v := range headers {
			req.Header.Add(k, v)
		}
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("aa: non-2xx status for %s %s: %v %v", method, url, resp.StatusCode, resp.Status)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("aa: Unable to read resp.Body: %v", err)
	}
	return body, nil
}
