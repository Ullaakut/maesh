package integration

import (
	"fmt"

	"github.com/containous/traefik/v2/pkg/config/dynamic"
	"github.com/go-check/check"
	checker "github.com/vdemeester/shakers"
	corev1 "k8s.io/api/core/v1"
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
	s.createResources(c, "resources/tcp-state-table/")
	s.createResources(c, "resources/smi/crds/")
}

func (s *ACLDisabledSuite) TearDownSuite(c *check.C) {
	s.stopK3s()
}

func (s *ACLDisabledSuite) TestHTTPService(c *check.C) {
	s.createResources(c, "resources/acl/disabled/http")
	defer s.deleteResources(c, "resources/acl/disabled/http", true)
	defer s.deleteShadowServices(c)

	s.waitForPods(c, []string{"server"})

	cmd := s.startMaeshBinaryCmd(c, false, false)
	err := cmd.Start()

	c.Assert(err, checker.IsNil)
	defer s.stopMaeshBinary(c, cmd.Process)

	config := s.testConfigurationWithReturn(c, "resources/acl/disabled/http.json")

	serverSvc := s.getService(c, "server")
	serverPod := s.getPod(c, "server")

	s.checkBlockAllMiddleware(c, config)
	s.checkHTTPReadinessService(c, config)
	s.checkHTTPServiceLoadBalancer(c, config, serverSvc, []*corev1.Pod{serverPod})
}

func (s *ACLDisabledSuite) TestTCPService(c *check.C) {
	s.createResources(c, "resources/acl/disabled/tcp")
	defer s.deleteResources(c, "resources/acl/disabled/tcp", true)
	defer s.deleteShadowServices(c)

	s.waitForPods(c, []string{"server"})

	cmd := s.startMaeshBinaryCmd(c, false, false)
	err := cmd.Start()

	c.Assert(err, checker.IsNil)
	defer s.stopMaeshBinary(c, cmd.Process)

	config := s.testConfigurationWithReturn(c, "resources/acl/disabled/tcp.json")

	serverSvc := s.getService(c, "server")
	serverPod := s.getPod(c, "server")

	s.checkHTTPReadinessService(c, config)
	s.checkTCPServiceLoadBalancer(c, config, serverSvc, []*corev1.Pod{serverPod})
}

func (s *ACLDisabledSuite) TestSplitTraffic(c *check.C) {
	s.createResources(c, "resources/acl/disabled/traffic-split")
	defer s.deleteResources(c, "resources/acl/disabled/traffic-split", true)
	defer s.deleteShadowServices(c)

	s.waitForPods(c, []string{"server-v1", "server-v2"})

	cmd := s.startMaeshBinaryCmd(c, false, false)
	err := cmd.Start()

	c.Assert(err, checker.IsNil)
	defer s.stopMaeshBinary(c, cmd.Process)

	config := s.testConfigurationWithReturn(c, "resources/acl/disabled/traffic-split.json")

	s.checkBlockAllMiddleware(c, config)

	serverV1Svc := s.getService(c, "server-v1")
	serverV1Pod := s.getPod(c, "server-v1")

	s.checkHTTPServiceLoadBalancer(c, config, serverV1Svc, []*corev1.Pod{serverV1Pod})

	serverV2Svc := s.getService(c, "server-v2")
	serverV2Pod := s.getPod(c, "server-v2")

	s.checkHTTPServiceLoadBalancer(c, config, serverV2Svc, []*corev1.Pod{serverV2Pod})
}

func (s *ACLDisabledSuite) checkBlockAllMiddleware(c *check.C, config *dynamic.Configuration) {
	middleware := config.HTTP.Middlewares["block-all-middleware"]
	c.Assert(middleware, checker.NotNil)

	c.Assert(middleware.IPWhiteList.SourceRange, checker.HasLen, 1)
	c.Assert(middleware.IPWhiteList.SourceRange[0], checker.Equals, "255.255.255.255")
}

func (s *ACLDisabledSuite) checkHTTPReadinessService(c *check.C, config *dynamic.Configuration) {
	service := config.HTTP.Services["readiness"]
	c.Assert(service, checker.NotNil)

	c.Assert(service.LoadBalancer.Servers, checker.HasLen, 1)
	c.Assert(service.LoadBalancer.Servers[0].URL, checker.Equals, "http://127.0.0.1:8080")
}

func (s *ACLDisabledSuite) checkHTTPServiceLoadBalancer(c *check.C, config *dynamic.Configuration, svc *corev1.Service, pods []*corev1.Pod) {
	for _, port := range svc.Spec.Ports {
		svcKey := fmt.Sprintf("%s-%s-%d", svc.Namespace, svc.Name, port.Port)

		service := config.HTTP.Services[svcKey]
		c.Assert(service, checker.NotNil)

		c.Assert(service.LoadBalancer.Servers, checker.HasLen, len(pods))

		for _, pod := range pods {
			wantURL := fmt.Sprintf("http://%s:%d", pod.Status.PodIP, port.TargetPort.IntVal)

			var found bool

			for _, server := range service.LoadBalancer.Servers {
				if wantURL == server.URL {
					found = true
					break
				}
			}

			c.Assert(found, checker.True)
		}
	}
}

func (s *ACLDisabledSuite) checkTCPServiceLoadBalancer(c *check.C, config *dynamic.Configuration, svc *corev1.Service, pods []*corev1.Pod) {
	for _, port := range svc.Spec.Ports {
		svcKey := fmt.Sprintf("%s-%s-%d", svc.Namespace, svc.Name, port.Port)

		service := config.TCP.Services[svcKey]
		c.Assert(service, checker.NotNil)

		c.Assert(service.LoadBalancer.Servers, checker.HasLen, len(pods))

		for _, pod := range pods {
			wantURL := fmt.Sprintf("%s:%d", pod.Status.PodIP, port.TargetPort.IntVal)

			var found bool

			for _, server := range service.LoadBalancer.Servers {
				if wantURL == server.Address {
					found = true
					break
				}
			}

			c.Assert(found, checker.True)
		}
	}
}
