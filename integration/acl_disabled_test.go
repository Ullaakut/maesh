package integration

import (
	"errors"
	"fmt"
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
		"giantswarm/tiny-tools:3.9",
		"traefik:v2.1.6",
	}
	s.startk3s(c, requiredImages)
	s.startAndWaitForCoreDNS(c)
	err := s.installHelmMaesh(c, false, false, false)
	c.Assert(err, checker.IsNil)
	s.waitForMaeshControllerStarted(c)
}

func (s *ACLDisabledSuite) TearDownSuite(c *check.C) {
	s.stopK3s()
}

func (s *ACLDisabledSuite) TestHTTPService(c *check.C) {
	s.createResources(c, "resources/acl/disabled/http")
	defer s.deleteResources(c, "resources/acl/disabled/http", true)

	s.waitForPods(c, []string{"client", "server"})

	args := []string{
		"exec", "-it", "pod/client", "-n", "ns", "--",
		"curl", "server.ns.maesh:8080",
	}

	err := s.try.WaitCommandExecute("kubectl", args, "", 10*time.Second)
	c.Assert(err, checker.IsNil)
}

func (s *ACLDisabledSuite) TestTCPService(c *check.C) {
	s.createResources(c, "resources/acl/disabled/tcp")
	defer s.deleteResources(c, "resources/acl/disabled/tcp", true)

	s.waitForPods(c, []string{"client", "server"})

	args := []string{
		"exec", "-it", "pod/client", "-n", "ns", "--",
		"ash", "-c", "echo 'WHO' | nc -q 0 server.ns.maesh 8080",
	}

	err := s.try.WaitCommandExecute("kubectl", args, "", 10*time.Second)
	c.Assert(err, checker.IsNil)
}

func (s *ACLDisabledSuite) TestTrafficSplit(c *check.C) {
	s.createResources(c, "resources/acl/disabled/traffic-split")
	defer s.deleteResources(c, "resources/acl/disabled/traffic-split", true)

	s.waitForPods(c, []string{"client", "server-v1", "server-v2"})

	err := s.try.Retry(func() error {
		var fstCall, secCall string
		var err error

		// Call two times the Service which has a TrafficSplit.
		fstCall, err = s.kubectlExec("ns", "pod/client", "curl", "server.ns.maesh:8080")
		if err != nil {
			return err
		}
		secCall, err = s.kubectlExec("ns", "pod/client", "curl", "server.ns.maesh:8080")
		if err != nil {
			return err
		}

		// Make sure each call ended up on a different backend.
		backendHostReg := regexp.MustCompile("Host: server-v([12])\\.ns\\.maesh:8080")
		backendsHit := make(map[string]bool)

		matchFstCall := backendHostReg.FindStringSubmatch(fstCall)
		if matchFstCall == nil {
			return errors.New("unable to find Host header in response")
		}
		backendsHit[matchFstCall[1]] = true

		matchSecCall := backendHostReg.FindStringSubmatch(secCall)
		if matchSecCall == nil {
			return errors.New("unable to find Host header in response")
		}
		backendsHit[matchSecCall[1]] = true

		if len(backendsHit) != 2 {
			return fmt.Errorf("expected to hit 2 different backends, got %d", len(backendsHit))
		}
		return nil
	}, 10*time.Second)
	c.Assert(err, checker.IsNil)
}

func (s *ACLDisabledSuite) waitForPods(c *check.C, pods []string) {
	for _, pod := range pods {
		err := s.try.WaitPodIPAssigned(pod, "ns", 30*time.Second)
		c.Assert(err, checker.IsNil)
	}
}
