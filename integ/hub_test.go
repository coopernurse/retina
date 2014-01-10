package integ

import (
	. "launchpad.net/gocheck"
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
	f.StartBackend(50, 20*time.Millisecond)
	f.StartTimer()
	f.RunEchoClient(5, time.Second)
	f.RunAddClient(5, time.Second)
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
