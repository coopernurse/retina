package integ

import (
	. "launchpad.net/gocheck"
	"log"
	"math/rand"
	"testing"
	"time"
)

// Hook up gocheck into the "go test" runner.
func TestHubSuite(t *testing.T) { TestingT(t) }

type S struct{}

var _ = Suite(&S{})

var f *Fixture

func (s *S) SetUpSuite(c *C) {
	rand.Seed(time.Now().UnixNano())
}

func (s *S) TestOneBackend(c *C) {
	f = NewFixture(c)
	defer f.Destroy()
	f.StartRetina(20 * time.Millisecond)
	f.StartBackend(5, 20*time.Millisecond)
	f.StartTimer()
	f.RunEchoClient(5, time.Second)
	f.RunAddClient(50, time.Second)
	f.WaitForClients()
	f.LogThroughput("TestOneBackend")
	f.VerifyMessages()
}

func (s *S) TestFiveBackends(c *C) {
	f = NewFixture(c)
	defer f.Destroy()
	f.StartRetina(20 * time.Millisecond)
	f.StartBackend(1, 0)
	f.StartBackend(20, 0)
	f.StartBackend(30, 0)
	f.StartBackend(40, 0)
	f.StartBackend(50, 20*time.Millisecond)
	f.StartTimer()
	f.RunEchoClient(15, time.Second)
	f.RunAddClient(5, time.Second)
	f.WaitForClients()
	f.LogThroughput("TestFiveBackends")
	f.VerifyMessages()
}

func (s *S) TestRandomBackendFailure(c *C) {
	f = NewFixture(c)
	defer f.Destroy()
	f.StartRetina(20 * time.Millisecond)
	b := f.StartBackend(5, 20*time.Millisecond)
	f.StartTimer()
	f.RunEchoClient(15, 5*time.Second)
	end := time.Now().Add(4 * time.Second)
	for time.Now().Before(end) {
		b.Runner.Stop()
		time.Sleep(time.Duration(rand.Intn(1000)+500) * time.Millisecond)
		b = f.StartBackend(5, 200*time.Millisecond)
		log.Println("TestRandomBackendFailure: Backend restarted")
	}
	f.StartBackend(5, 0)
	f.WaitForClients()
	f.LogThroughput("TestRandomBackendFailure")
	f.VerifyMessages()
}
