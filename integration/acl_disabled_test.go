package integration

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/containous/maesh/integration/try"

	"github.com/go-check/check"
	checker "github.com/vdemeester/shakers"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ACLDisabledSuite
type ACLDisabledSuite struct{ BaseSuite }

func (s *ACLDisabledSuite) SetUpSuite(c *check.C) {
	requiredImages := []string{
		"containous/maesh:latest",
		"containous/whoami:v1.0.1",
		"coredns/coredns:1.6.3",
		"traefik:v2.1.6",
	}
	s.startk3s(c, requiredImages)
	s.startAndWaitForCoreDNS(c)
	s.createResources(c, "resources/tcp-state-table/")
	s.createResources(c, "resources/smi/crds/")
}

func (s *ACLDisabledSuite) TearDownSuite(c *check.C) {
	s.stopK3s()
}

func (s *ACLDisabledSuite) TestHTTPService(c *check.C) {
	s.createResources(c, "resources/acl/disabled/http")
	defer s.deleteResources(c, "resources/acl/disabled/http", true)

	cmd := s.startMaeshBinaryCmd(c, false, false)
	err := cmd.Start()

	c.Assert(err, checker.IsNil)
	defer s.stopMaeshBinary(c, cmd.Process)

	s.waitForReadiness(c, "ns", "client", 10*time.Second)
	s.waitForReadiness(c, "ns", "server", 20*time.Second)

	args := []string{
		"get", "all", "-o", "wide",
		"-n", maeshNamespace,
	}
	output, err := s.try.WaitCommandExecuteReturn("kubectl", args, 10*time.Second)
	c.Log(output)
	c.Log(err)

	s.printRawData(c, 10*time.Second)

	s.execFromPod(c, "ns", "client", []string{"dig", "server.ns.maesh"}, 10*time.Second)

	s.execFromPod(c, "ns", "client", []string{"curl", "server.ns.maesh:8080"}, 10*time.Second)
}

func (s *ACLDisabledSuite) getClientPod(c *check.C, ns, name string) *v1.Pod {
	pod, err := s.client.GetKubernetesClient().CoreV1().Pods(ns).Get(name, metav1.GetOptions{})
	c.Assert(err, checker.IsNil)
	c.Assert(pod, checker.NotNil)

	return pod
}

func (s *ACLDisabledSuite) waitForReadiness(c *check.C, ns, name string, timeout time.Duration) {
	args := []string{
		"wait", "--for=condition=Ready",
		fmt.Sprintf("pod/%s", name),
		"-n", ns,
	}
	output, err := s.try.WaitCommandExecuteReturn("kubectl", args, timeout)
	c.Log(output)
	c.Assert(err, checker.IsNil)
}

func (s *ACLDisabledSuite) execFromPod(c *check.C, ns, name string, cmdArgs []string, timeout time.Duration) {
	args := []string{
		"exec", "-it",
		fmt.Sprintf("pod/%s", name),
		"-n", ns,
		"--",
	}

	for _, arg := range cmdArgs {
		args = append(args, arg)
	}

	output, err := s.try.WaitCommandExecuteReturn("kubectl", args, timeout)
	c.Log(output)
	c.Assert(err, checker.IsNil)
}

func (s *ACLDisabledSuite) printRawData(c *check.C, timeout time.Duration) {
	var output string

	url := fmt.Sprintf("http://127.0.0.1:%d/api/configuration/current", maeshAPIPort)
	err := try.GetRequest(url, timeout, func(res *http.Response) error {
		body, err := ioutil.ReadAll(res.Body)
		if err != nil {
			return fmt.Errorf("failed to read response body: %s", err)
		}
		if err = res.Body.Close(); err != nil {
			return err
		}

		output = string(body)

		return nil
	})
	c.Log(output)
	c.Assert(err, checker.IsNil)
}
