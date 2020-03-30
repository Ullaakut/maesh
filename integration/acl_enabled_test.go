package integration

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/go-check/check"
	checker "github.com/vdemeester/shakers"
)

// ACLEnabledSuite
type ACLEnabledSuite struct{ BaseSuite }

func (s *ACLEnabledSuite) SetUpSuite(c *check.C) {
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
	err := s.installHelmMaesh(c, false, false, true)
	c.Assert(err, checker.IsNil)
	s.waitForMaeshControllerStarted(c)
}

func (s *ACLEnabledSuite) TearDownSuite(c *check.C) {
	s.stopK3s()
}

func (s *ACLEnabledSuite) TestHTTPTrafficTarget(c *check.C) {
	s.createResources(c, "resources/acl/enabled/http-traffic-target")
	defer s.deleteResources(c, "resources/acl/enabled/http-traffic-target", true)

	s.waitForPods(c, []string{"client-a", "client-b", "server"})

	// Make sure client-a can hit the server on /api.
	curlFromClientA := []string{
		"exec", "-i", "pod/client-a", "-n", "ns", "--",
		"curl", "--fail", "-sw", "'%{http_code}'", "server.ns.maesh:8080/api",
	}
	output, err := s.try.WaitCommandExecuteReturn("kubectl", curlFromClientA, 20*time.Second)
	c.Assert(err, checker.IsNil)
	c.Assert(output, checker.Contains, "'200'")

	// Make sure client-b can't hit the server.
	curlFromClientB := []string{
		"exec", "-i", "pod/client-b", "-n", "ns", "--",
		"curl", "-sw", "'%{http_code}'", "server.ns.maesh:8080/api",
	}
	output, err = s.try.WaitCommandExecuteReturn("kubectl", curlFromClientB, 20*time.Second)
	c.Assert(err, checker.IsNil)
	c.Assert(output, checker.Contains, "Forbidden'403'")

	// Make sure we can't hit anything but /api.
	curlFromClientA = []string{
		"exec", "-i", "pod/client-a", "-n", "ns", "--",
		"curl", "-sw", "'%{http_code}'", "server.ns.maesh:8080",
	}
	output, err = s.try.WaitCommandExecuteReturn("kubectl", curlFromClientA, 20*time.Second)
	c.Assert(err, checker.IsNil)
	c.Assert(output, checker.Contains, "Forbidden'403'")
}

func (s *ACLEnabledSuite) TestTCPTrafficTarget(c *check.C) {
	s.createResources(c, "resources/acl/enabled/tcp-traffic-target")
	defer s.deleteResources(c, "resources/acl/enabled/tcp-traffic-target", true)

	s.waitForPods(c, []string{"client", "server"})

	// Make sure client can hit the server.
	curlFromClientA := []string{
		"exec", "-i", "pod/client", "-n", "ns", "--",
		"ash", "-c", "echo 'WHO' | nc -q 0 server.ns.maesh 8080 | grep 'Hostname: server'",
	}
	_, err := s.try.WaitCommandExecuteReturn("kubectl", curlFromClientA, 20*time.Second)
	c.Assert(err, checker.IsNil)
}

func (s *ACLEnabledSuite) printRawData(c *check.C, timeout time.Duration) {
	proxies, err := s.client.GetKubernetesClient().CoreV1().Pods(maeshNamespace).List(metav1.ListOptions{
		LabelSelector: "component=maesh-mesh",
	})
	c.Assert(err, checker.IsNil)
	c.Assert(len(proxies.Items), checker.GreaterThan, 0)

	proxyIP := proxies.Items[0].Status.PodIP
	fmt.Println("proxyIP!", proxyIP)

	args := []string{
		"exec", "-it",
		"pod/client",
		"-n", "ns",
		"--",
		"curl",
		fmt.Sprintf("http://%s:8080/api/rawdata", proxyIP),
	}

	output, err := s.try.WaitCommandExecuteReturn("kubectl", args, timeout)
	c.Log(output)
	c.Assert(err, checker.IsNil)
}

func (s *ACLEnabledSuite) TestTrafficSplit(c *check.C) {
	s.createResources(c, "resources/acl/enabled/traffic-split")
	defer s.deleteResources(c, "resources/acl/enabled/traffic-split", true)

	s.waitForPods(c, []string{"client-a", "client-b", "server-v1", "server-v2"})

	// Make sure client-a can hit the server.
	err := s.try.Retry(func() error {
		var fstCall, secCall string
		var err error

		fstCall, err = s.kubectlExec("ns", "pod/client-a", "curl", "--fail", "server.ns.maesh:8080")
		if err != nil {
			return err
		}

		secCall, err = s.kubectlExec("ns", "pod/client-a", "curl", "--fail", "server.ns.maesh:8080")
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
	}, 30*time.Second)
	c.Assert(err, checker.IsNil)

	// Make sure client-b can't hit the server.
	var output string
	output, err = s.kubectlExec("ns", "client-b", "curl", "-sw", "'%{http_code}'", "server.ns.maesh:8080")
	c.Assert(err, checker.IsNil)
	c.Assert(output, checker.Contains, "Forbidden'403'")
}

func (s *ACLEnabledSuite) waitForPods(c *check.C, pods []string) {
	for _, pod := range pods {
		err := s.try.WaitPodIPAssigned(pod, "ns", 30*time.Second)
		c.Assert(err, checker.IsNil)
	}
}
