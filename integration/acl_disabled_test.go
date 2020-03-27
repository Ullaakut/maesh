package integration

import (
	"regexp"
	"time"

	"github.com/go-check/check"
	checker "github.com/vdemeester/shakers"
)

// ACLDisabledSuite
type ACLDisabledSuite struct{ BaseSuite }

func (s *ACLDisabledSuite) SetUpSuite(c *check.C) {
	requiredImages := []string{
		"containous/maesh:latest",
		"containous/whoami:v1.0.1",
		"containous/whoamitcp",
		"coredns/coredns:1.6.3",
		"traefik:v2.1.6",
	}
	s.startk3s(c, requiredImages)
	s.startAndWaitForCoreDNS(c)
	err := s.installHelmMaesh(c, false, false)
	c.Assert(err, checker.IsNil)
	s.waitForMaeshControllerStarted(c)
}

func (s *ACLDisabledSuite) TearDownSuite(c *check.C) {
	s.stopK3s()
}

func (s *ACLDisabledSuite) TestHTTPService(c *check.C) {
	s.createResources(c, "resources/acl/disabled/http")
	defer s.deleteResources(c, "resources/acl/disabled/http", true)

	err := s.try.WaitPodIPAssigned("client", "ns", 10*time.Second)
	c.Assert(err, checker.IsNil)

	err = s.try.WaitPodIPAssigned("server", "ns", 10*time.Second)
	c.Assert(err, checker.IsNil)

	args := []string{
		"exec", "-it", "pod/client", "-n", "ns", "--",
		"curl", "server.ns.maesh:8080",
	}

	err = s.try.WaitCommandExecute("kubectl", args, "", 10*time.Second)
	c.Assert(err, checker.IsNil)
}

func (s *ACLDisabledSuite) TestTCPService(c *check.C) {
	s.createResources(c, "resources/acl/disabled/tcp")
	defer s.deleteResources(c, "resources/acl/disabled/tcp", true)

	err := s.try.WaitPodIPAssigned("client", "ns", 10*time.Second)
	c.Assert(err, checker.IsNil)

	err = s.try.WaitPodIPAssigned("server", "ns", 10*time.Second)
	c.Assert(err, checker.IsNil)

	args := []string{
		"exec", "-it", "pod/client", "-n", "ns", "--",
		"ash", "-c", "echo 'WHO' | nc -q 0 server.ns.maesh 8080",
	}

	err = s.try.WaitCommandExecute("kubectl", args, "", 10*time.Second)
	c.Assert(err, checker.IsNil)
}

func (s *ACLDisabledSuite) TestTrafficSplit(c *check.C) {
	s.createResources(c, "resources/acl/disabled/traffic-split")
	defer s.deleteResources(c, "resources/acl/disabled/traffic-split", true)

	err := s.try.WaitPodIPAssigned("client", "ns", 10*time.Second)
	c.Assert(err, checker.IsNil)

	err = s.try.WaitPodIPAssigned("server-v1", "ns", 10*time.Second)
	c.Assert(err, checker.IsNil)

	err = s.try.WaitPodIPAssigned("server-v2", "ns", 10*time.Second)
	c.Assert(err, checker.IsNil)

	args := []string{
		"exec", "-i", "pod/client", "-n", "ns", "--",
		"curl", "server.ns.maesh:8080",
	}

	// Call two times the Service which has a TrafficSplit.
	var fstCall, secCall string
	fstCall, err = s.try.WaitCommandExecuteReturn("kubectl", args, 10*time.Second)
	c.Assert(err, checker.IsNil)
	secCall, err = s.try.WaitCommandExecuteReturn("kubectl", args, 10*time.Second)
	c.Assert(err, checker.IsNil)

	// Make sure each call ended up on a different backend.
	backendHostReg := regexp.MustCompile("Host: server-v(1|2)\\.ns\\.maesh:8080")

	matchFstCall := backendHostReg.FindStringSubmatch(fstCall)
	c.Assert(matchFstCall, checker.NotNil)
	matchSecCall := backendHostReg.FindStringSubmatch(secCall)
	c.Assert(matchSecCall, checker.NotNil)

	backendsHit := make(map[string]bool)
	backendsHit[matchFstCall[1]] = true
	backendsHit[matchSecCall[1]] = true

	c.Assert(len(backendsHit), checker.Equals, 2)
}
