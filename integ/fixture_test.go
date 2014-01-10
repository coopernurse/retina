package integ

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"io/ioutil"
	. "launchpad.net/gocheck"
	"log"
	mrand "math/rand"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type Backend struct {
	Runner  *CmdRunner
	LogFile string
	MsgFile string
}

type CmdRunner struct {
	C       *C
	Cmd     *exec.Cmd
	Done    chan bool
	running bool
}

func (me *CmdRunner) Run() {
	me.running = true
	err := me.Cmd.Run()
	me.Done <- true
	me.C.Assert(err, IsNil)
}

func (me *CmdRunner) Stop() {
	if me.running {
		me.running = false
		select {
		case <-me.Done:
			me.C.Assert(me.Cmd.ProcessState.Success(), Equals, true)
		default:
			me.Cmd.Process.Signal(syscall.SIGTERM)
			me.Cmd.Process.Wait()
		}
	}
}

func NewFixture(c *C) *Fixture {
	return &Fixture{
		C:        c,
		ClientWG: &sync.WaitGroup{},
		lock:     &sync.Mutex{},
	}
}

type Fixture struct {
	C        *C
	ClientWG *sync.WaitGroup
	TmpFiles []string
	Commands []*CmdRunner
	Backends []*Backend

	OutMsgs []string
	lock    *sync.Mutex

	benchStart int64
}

func (me *Fixture) StartRetina(sleepTime time.Duration) {
	me.writeFile(retinaConfFname, retinaConf)
	me.runCmd("../bin/retina", "-c", retinaConfFname)
	if sleepTime > 0 {
		time.Sleep(sleepTime)
	}
}

func (me *Fixture) StartBackend(workers int, sleepTime time.Duration) *Backend {
	me.lock.Lock()
	defer me.lock.Unlock()

	logFile := fmt.Sprintf("/tmp/backend_log_%d.txt", len(me.Backends))
	msgFile := fmt.Sprintf("/tmp/backend_msg_%d.txt", len(me.Backends))

	r := me.runCmd("../bin/backend", "-u", "ws://localhost:9391/",
		"-w", strconv.Itoa(workers),
		"-l", logFile,
		"-m", msgFile)

	b := &Backend{LogFile: logFile, MsgFile: msgFile, Runner: r}
	me.Backends = append(me.Backends, b)
	me.TmpFiles = append(me.TmpFiles, logFile, msgFile)

	if sleepTime > 0 {
		time.Sleep(sleepTime)
	}
	return b
}

func (me *Fixture) RunEchoClient(workers int, runTime time.Duration) {
	me.runClient(workers, runTime, func() string {
		s := RandHex(10)
		resp, err := HTTPReq("POST", "http://localhost:9390/api/echo", "", nil, bytes.NewBufferString(s))
		me.C.Check(err, IsNil)
		me.C.Check(string(resp), Equals, s)
		return s
	})
}

func (me *Fixture) RunAddClient(workers int, runTime time.Duration) {
	me.runClient(workers, runTime, func() string {
		a := mrand.Intn(50000)
		b := mrand.Intn(50000)
		c := a + b
		s := fmt.Sprintf("%d,%d", a, b)
		resp, err := HTTPReq("POST", "http://localhost:9390/api/add", "", nil, bytes.NewBufferString(s))
		respInt, _ := strconv.Atoi(string(resp))
		me.C.Check(err, IsNil)
		me.C.Check(respInt, Equals, c)
		return s
	})
}

func (me *Fixture) runClient(workers int, runTime time.Duration, fx func() string) {
	for i := 0; i < workers; i++ {
		me.ClientWG.Add(1)
		go func() {
			defer me.ClientWG.Done()
			end := time.Now().Add(runTime)
			for time.Now().Before(end) {
				s := fx()
				me.addMsg(s)
			}
		}()
	}
}

func (me *Fixture) addMsg(msg string) {
	me.lock.Lock()
	defer me.lock.Unlock()
	me.OutMsgs = append(me.OutMsgs, msg)
}

func (me *Fixture) WaitForClients() {
	me.ClientWG.Wait()
}

func (me *Fixture) StartTimer() {
	me.lock.Lock()
	defer me.lock.Unlock()
	me.benchStart = time.Now().UnixNano()
}

func (me *Fixture) LogThroughput(msg string) {
	me.lock.Lock()
	defer me.lock.Unlock()
	elapsed := time.Now().UnixNano() - me.benchStart
	reqSec := float64(len(me.OutMsgs)) / (float64(elapsed) / 1e9)
	log.Printf("Throughput: %s - %.1f req/sec\n", msg, reqSec)
}

func (me *Fixture) VerifyMessages() {
	me.lock.Lock()
	defer me.lock.Unlock()

	backendMsgs := make([]string, 0, len(me.OutMsgs))
	for _, backend := range me.Backends {
		file, err := os.Open(backend.MsgFile)
		me.C.Assert(err, IsNil)
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			backendMsgs = append(backendMsgs, scanner.Text())
		}
		me.C.Assert(scanner.Err(), IsNil)
	}

	sort.Strings(me.OutMsgs)
	sort.Strings(backendMsgs)

	me.C.Check(len(backendMsgs) > 0, Equals, true)
	me.C.Check(len(backendMsgs), Equals, len(me.OutMsgs))

	errs := 0
	for x := 0; x < len(me.OutMsgs) && x < len(backendMsgs) && errs < 3; x++ {
		if backendMsgs[x] != me.OutMsgs[x] {
			me.C.Errorf("VerifyMessages: %d: %s != %s", x, backendMsgs[x], me.OutMsgs[x])
			errs++
		}
	}
}

func (me *Fixture) Destroy() {
	for x := len(me.Commands) - 1; x >= 0; x-- {
		me.Commands[x].Stop()
	}

	// for _, fname := range me.TmpFiles {
	// 	os.Remove(fname)
	// }
}

func (me *Fixture) runCmd(name string, params ...string) *CmdRunner {
	cmd := exec.Command(name, params...)
	runner := &CmdRunner{
		C:    me.C,
		Cmd:  cmd,
		Done: make(chan bool),
	}
	go runner.Run()
	me.Commands = append(me.Commands, runner)
	return runner
}

func (me *Fixture) writeFile(fname, contents string) {
	err := ioutil.WriteFile(fname, []byte(contents), os.FileMode(0644))
	me.C.Assert(err, IsNil)
	me.TmpFiles = append(me.TmpFiles, fname)
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

var retinaConfFname = "/tmp/retina_test_conf.json"
var retinaConf = `
{
   "listen" : "0.0.0.0:9390",
   "websockethubs" : {
       "test-services" : {
           "listen"    : ":9391",
           "heartbeat" : 2000
       }
   },
   "vhosts" : {
       "default" : {
           "docroot": "/dev/null",
           "wshub": {
               "/api/" : "test-services"
           }
       }
   }
}
`
