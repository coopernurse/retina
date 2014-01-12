package main

import (
	"flag"
	"fmt"
	"github.com/coopernurse/retina/ws"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func run(url string, workers int, done chan bool, msgs chan string) {
	handler := func(headers map[string][]string, body []byte) (map[string][]string, []byte) {
		msgs <- string(body)
		queue, ok := headers["X-Hub-Queue"]
		if !ok || len(queue) < 1 {
			log.Println("backend: message missing X-Hub-Queue header")
			return map[string][]string{"X-Hub-Status": []string{"500"}}, []byte("Missing X-Hub-Queue header")
		} else {
			switch queue[0] {
			case "echo":
				return nil, body
			case "add":
				parts := strings.Split(string(body), ",")
				sum := 0
				for _, part := range parts {
					x, _ := strconv.Atoi(part)
					sum += x
				}
				return nil, []byte(strconv.Itoa(sum))
			case "sleep":
				parts := strings.Split(string(body), ",")
				if len(parts) == 2 {
					sleepMillis, _ := strconv.Atoi(parts[0])
					time.Sleep(time.Duration(sleepMillis) * time.Millisecond)
				}
				return nil, body
			default:
				return map[string][]string{"X-Hub-Status": []string{"500"}}, []byte("Unknown queue: " + queue[0])
			}
		}
	}
	url = url + "echo,add,sleep"
	retinaws.BackendServer(url, workers, handler, done)
}

func initSignalHandlers(done chan bool) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGKILL)
	go func() {
		for sig := range c {
			log.Printf("backend: sending shutdown message - got signal: %v\n", sig)
			done <- true
		}
	}()
}

func main() {
	var wsUrl string
	var logFname string
	var msgFname string
	var workers int
	flag.StringVar(&wsUrl, "u", "ws://localhost:9391/", "Retina websocket endpoint URL")
	flag.StringVar(&logFname, "l", "", "Path to log file to write to")
	flag.StringVar(&msgFname, "m", "", "Path to msg file to write to")
	flag.IntVar(&workers, "w", 10, "Number of workers")
	flag.Parse()

	if msgFname == "" {
		log.Fatalln("-m flag not provided")
	}

	if logFname != "" {
		logFile, err := os.Create(logFname)
		if err != nil {
			log.Fatalln("Cannot write to log file:", logFile, err)
		}
		defer logFile.Close()
		log.SetOutput(logFile)
	}

	msgFile, err := os.Create(msgFname)
	if err != nil {
		log.Fatalln("Cannot write to msg file:", msgFname, err)
	}
	defer msgFile.Close()

	done := make(chan bool)
	initSignalHandlers(done)

	msgs := make(chan string, workers)
	go func() {
		for {
			msg, ok := <-msgs
			if !ok {
				return
			}
			fmt.Fprintln(msgFile, msg)
		}
	}()

	log.Println("backend: starting")
	run(wsUrl, workers, done, msgs)
	close(msgs)
	msgFile.Sync()
	log.Println("backend: exiting")
}
