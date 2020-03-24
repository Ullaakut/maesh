package provider_test

import (
	"fmt"
	"testing"

	spec "github.com/deislabs/smi-sdk-go/pkg/apis/specs/v1alpha1"

	"github.com/containous/maesh/pkg/k8s"
	mk8s "github.com/containous/maesh/pkg/k8s"
	"github.com/containous/maesh/pkg/provider"
	"github.com/containous/maesh/pkg/topology"
	"github.com/containous/traefik/v2/pkg/config/dynamic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type TopologyBuilderMock func() (*topology.Topology, error)

func (m TopologyBuilderMock) Build(_ mk8s.IgnoreWrapper) (*topology.Topology, error) {
	return m()
}

type tcpStateTableMock func() (int32, bool)

func (t tcpStateTableMock) Find(_ k8s.ServiceWithPort) (int32, bool) {
	return t()
}

func TestProvider_BuildConfigWithACLDisabled(t *testing.T) {
	annotations := map[string]string{}
	svcbAnnotations := map[string]string{
		"maesh.containo.us/scheme":                     "https",
		"maesh.containo.us/retry-attempts":             "2",
		"maesh.containo.us/ratelimit-average":          "100",
		"maesh.containo.us/ratelimit-burst":            "110",
		"maesh.containo.us/circuit-breaker-expression": "LatencyAtQuantileMS(50.0) > 100",
	}
	svcfAnnotations := map[string]string{
		"maesh.containo.us/traffic-type": "tcp",
	}
	saB := "sa-b"

	podB1 := createPod("my-ns", "pod-b1", saB, "10.10.2.1")
	podB2 := createPod("my-ns", "pod-b2", saB, "10.10.2.2")
	svcB := createSvc("my-ns", "svc-b", svcbAnnotations, []int32{8080}, "10.10.14.1", []*topology.Pod{podB1, podB2})
	svcD := createSvc("my-ns", "svc-d", annotations, []int32{8080}, "10.10.15.1", []*topology.Pod{})
	svcE := createSvc("my-ns", "svc-e", annotations, []int32{8080}, "10.10.16.1", []*topology.Pod{})
	svcF := createSvc("my-ns", "svc-f", svcfAnnotations, []int32{8080}, "10.10.17.1", []*topology.Pod{podB1, podB2})

	ts := &topology.TrafficSplit{
		Name:      "ts",
		Namespace: "my-ns",
		Service:   svcB,
		Backends: []topology.TrafficSplitBackend{
			{
				Weight:  80,
				Service: svcD,
			},
			{
				Weight:  20,
				Service: svcE,
			},
		},
	}

	svcB.TrafficSplits = []*topology.TrafficSplit{ts}

	top := &topology.Topology{
		Services: map[topology.NameNamespace]*topology.Service{
			nn(svcB.Name, svcB.Namespace): svcB,
			nn(svcD.Name, svcD.Namespace): svcD,
			nn(svcE.Name, svcE.Namespace): svcE,
			nn(svcF.Name, svcF.Namespace): svcF,
		},
		Pods: map[topology.NameNamespace]*topology.Pod{
			nn(podB1.Name, podB1.Namespace): podB1,
			nn(podB2.Name, podB2.Namespace): podB2,
		},
	}
	builder := func() (*topology.Topology, error) {
		return top, nil
	}

	tcpStatetable := func() (int32, bool) {
		return 5000, true
	}
	ignored := mk8s.NewIgnored()
	provider := provider.New(TopologyBuilderMock(builder), tcpStateTableMock(tcpStatetable), ignored, 10000, 10001, false, "http")

	got, err := provider.BuildConfig()
	require.NoError(t, err)

	want := &dynamic.Configuration{
		HTTP: &dynamic.HTTPConfiguration{
			Routers: map[string]*dynamic.Router{
				"readiness": readinessRtr,
				"my-ns-svc-b-8080": {
					Rule:        "Host(`svc-b.my-ns.maesh`) || Host(`10.10.14.1`)",
					EntryPoints: []string{"http-10000"},
					Service:     "my-ns-svc-b-8080",
					Middlewares: []string{"my-ns-svc-b"},
				},
				"my-ns-svc-b-ts-8080-traffic-split": {
					Rule:        "Host(`svc-b.my-ns.maesh`) || Host(`10.10.14.1`)",
					EntryPoints: []string{"http-10000"},
					Service:     "my-ns-svc-b-ts-8080-traffic-split",
					Middlewares: []string{"my-ns-svc-b"},
				},
				"my-ns-svc-d-8080": {
					Rule:        "Host(`svc-d.my-ns.maesh`) || Host(`10.10.15.1`)",
					EntryPoints: []string{"http-10000"},
					Service:     "my-ns-svc-d-8080",
				},
				"my-ns-svc-e-8080": {
					Rule:        "Host(`svc-e.my-ns.maesh`) || Host(`10.10.16.1`)",
					EntryPoints: []string{"http-10000"},
					Service:     "my-ns-svc-e-8080",
				},
			},
			Services: map[string]*dynamic.Service{
				"readiness": readinessSvc,
				"my-ns-svc-b-8080": {
					LoadBalancer: &dynamic.ServersLoadBalancer{
						Servers: []dynamic.Server{
							{URL: "https://10.10.2.1:8080"},
							{URL: "https://10.10.2.2:8080"},
						},
						PassHostHeader: getBoolRef(true),
					},
				},
				"my-ns-svc-b-ts-8080-traffic-split": {
					Weighted: &dynamic.WeightedRoundRobin{
						Services: []dynamic.WRRService{
							{
								Name:   "my-ns-svc-b-ts-8080-svc-d-traffic-split-backend",
								Weight: getIntRef(80),
							},
							{
								Name:   "my-ns-svc-b-ts-8080-svc-e-traffic-split-backend",
								Weight: getIntRef(20),
							},
						},
					},
				},
				"my-ns-svc-b-ts-8080-svc-d-traffic-split-backend": {
					LoadBalancer: &dynamic.ServersLoadBalancer{
						Servers: []dynamic.Server{
							{URL: "https://svc-d.my-ns.maesh:8080"},
						},
						PassHostHeader: getBoolRef(true),
					},
				},
				"my-ns-svc-b-ts-8080-svc-e-traffic-split-backend": {
					LoadBalancer: &dynamic.ServersLoadBalancer{
						Servers: []dynamic.Server{
							{URL: "https://svc-e.my-ns.maesh:8080"},
						},
						PassHostHeader: getBoolRef(true),
					},
				},
				"my-ns-svc-d-8080": {
					LoadBalancer: &dynamic.ServersLoadBalancer{
						PassHostHeader: getBoolRef(true),
					},
				},
				"my-ns-svc-e-8080": {
					LoadBalancer: &dynamic.ServersLoadBalancer{
						PassHostHeader: getBoolRef(true),
					},
				},
			},
			Middlewares: map[string]*dynamic.Middleware{
				"my-ns-svc-b": {
					Retry: &dynamic.Retry{Attempts: 2},
					RateLimit: &dynamic.RateLimit{
						Average: 100,
						Burst:   110,
					},
					CircuitBreaker: &dynamic.CircuitBreaker{
						Expression: "LatencyAtQuantileMS(50.0) > 100",
					},
				},
			},
		},
		TCP: &dynamic.TCPConfiguration{
			Routers: map[string]*dynamic.TCPRouter{
				"my-ns-svc-f-8080": {
					EntryPoints: []string{"tcp-5000"},
					Service:     "my-ns-svc-f-8080",
					Rule:        "HostSNI(`*`)",
				},
			},
			Services: map[string]*dynamic.TCPService{
				"my-ns-svc-f-8080": {
					LoadBalancer: &dynamic.TCPServersLoadBalancer{
						Servers: []dynamic.TCPServer{
							{
								Address: "10.10.2.1:8080",
							},
							{
								Address: "10.10.2.2:8080",
							},
						},
					},
				},
			},
		},
	}

	assert.Equal(t, want, got)
}

func TestProvider_BuildConfigTCP(t *testing.T) {
	saA := "sa-a"
	saB := "sa-b"

	podA := createPod("my-ns", "pod-a", saA, "10.10.1.1")
	podB := createPod("my-ns", "pod-b", saB, "10.10.1.2")
	svcB := createSvc("my-ns", "svc-b", map[string]string{}, []int32{8080}, "10.10.13.1", []*topology.Pod{podB})
	svcC := createSvc("my-ns", "svc-c", map[string]string{}, []int32{8080}, "10.10.13.2", []*topology.Pod{})
	svcD := createSvc("my-ns", "svc-d", map[string]string{}, []int32{8080}, "10.10.13.3", []*topology.Pod{})

	tt := &topology.ServiceTrafficTarget{
		Service: svcB,
		Name:    "tt",
		Sources: []topology.ServiceTrafficTargetSource{
			{
				ServiceAccount: saA,
				Namespace:      "my-ns",
				Pods:           []*topology.Pod{podA},
			},
		},
		Destination: topology.ServiceTrafficTargetDestination{
			ServiceAccount: saB,
			Namespace:      "my-ns",
			Ports:          []int32{8080},
			Pods:           []*topology.Pod{podB},
		},
		Specs: []topology.TrafficSpec{
			{
				TCPRoute: createTCPRoute("my-ns", "my-tcp-route"),
			},
		},
	}

	ts := &topology.TrafficSplit{
		Name:      "ts",
		Namespace: "my-ns",
		Service:   svcB,
		Backends: []topology.TrafficSplitBackend{
			{
				Weight:  80,
				Service: svcC,
			},
			{
				Weight:  20,
				Service: svcD,
			},
		},
	}

	podA.Outgoing = []*topology.ServiceTrafficTarget{tt}
	podB.Incoming = []*topology.ServiceTrafficTarget{tt}
	svcB.TrafficTargets = []*topology.ServiceTrafficTarget{tt}
	svcB.TrafficSplits = []*topology.TrafficSplit{ts}

	top := &topology.Topology{
		Services: map[topology.NameNamespace]*topology.Service{
			nn(svcB.Name, svcB.Namespace): svcB,
			nn(svcC.Name, svcC.Namespace): svcC,
			nn(svcD.Name, svcD.Namespace): svcD,
		},
		Pods: map[topology.NameNamespace]*topology.Pod{
			nn(podA.Name, podA.Namespace): podA,
			nn(podB.Name, podB.Namespace): podB,
		},
	}
	builder := func() (*topology.Topology, error) {
		return top, nil
	}

	tcpStatetable := func() (int32, bool) {
		return 5000, true
	}

	ignored := mk8s.NewIgnored()
	provider := provider.New(TopologyBuilderMock(builder), tcpStateTableMock(tcpStatetable), ignored, 10000, 10001, true, "tcp")

	got, err := provider.BuildConfig()
	require.NoError(t, err)

	want := &dynamic.Configuration{
		HTTP: &dynamic.HTTPConfiguration{
			Routers: map[string]*dynamic.Router{
				"readiness": readinessRtr,
			},
			Services: map[string]*dynamic.Service{
				"readiness": readinessSvc,
			},
			Middlewares: map[string]*dynamic.Middleware{},
		},
		TCP: &dynamic.TCPConfiguration{
			Routers: map[string]*dynamic.TCPRouter{
				"my-ns-svc-b-tt-8080-traffic-target": {
					EntryPoints: []string{"tcp-5000"},
					Service:     "my-ns-svc-b-tt-8080-traffic-target",
					Rule:        "HostSNI(`*`)",
				},
				"my-ns-svc-b-ts-8080-traffic-split": {
					EntryPoints: []string{"tcp-5000"},
					Service:     "my-ns-svc-b-ts-8080-traffic-split",
					Rule:        "HostSNI(`*`)",
				},
			},
			Services: map[string]*dynamic.TCPService{
				"my-ns-svc-b-tt-8080-traffic-target": {
					LoadBalancer: &dynamic.TCPServersLoadBalancer{
						Servers: []dynamic.TCPServer{
							{
								Address: "10.10.1.2:8080",
							},
						},
					},
				},
				"my-ns-svc-b-ts-8080-traffic-split": {
					Weighted: &dynamic.TCPWeightedRoundRobin{
						Services: []dynamic.TCPWRRService{
							{
								Name:   "my-ns-svc-b-ts-8080-svc-c-traffic-split-backend",
								Weight: getIntRef(80),
							},
							{
								Name:   "my-ns-svc-b-ts-8080-svc-d-traffic-split-backend",
								Weight: getIntRef(20),
							},
						},
					},
				},
				"my-ns-svc-b-ts-8080-svc-c-traffic-split-backend": {
					LoadBalancer: &dynamic.TCPServersLoadBalancer{
						Servers: []dynamic.TCPServer{
							{
								Address: "svc-c.my-ns.maesh:8080",
							},
						},
					},
				},
				"my-ns-svc-b-ts-8080-svc-d-traffic-split-backend": {
					LoadBalancer: &dynamic.TCPServersLoadBalancer{
						Servers: []dynamic.TCPServer{
							{
								Address: "svc-d.my-ns.maesh:8080",
							},
						},
					},
				},
			},
		},
	}

	assert.Equal(t, want, got)
}

func TestProvider_BuildConfig(t *testing.T) {
	annotations := map[string]string{}
	svcbAnnotations := map[string]string{
		"maesh.containo.us/retry-attempts":             "2",
		"maesh.containo.us/ratelimit-average":          "100",
		"maesh.containo.us/ratelimit-burst":            "110",
		"maesh.containo.us/circuit-breaker-expression": "LatencyAtQuantileMS(50.0) > 100",
	}
	saA := "sa-a"
	saB := "sa-b"
	saC := "sa-c"

	podA := createPod("my-ns", "pod-a", saA, "10.10.1.1")
	svcA := createSvc("my-ns", "svc-a", annotations, []int32{9090}, "10.10.13.1", []*topology.Pod{podA})
	podC := createPod("my-ns", "pod-c", saC, "10.10.3.1")
	podB1 := createPod("my-ns", "pod-b1", saB, "10.10.2.1")
	podB2 := createPod("my-ns", "pod-b2", saB, "10.10.2.2")
	svcB := createSvc("my-ns", "svc-b", svcbAnnotations, []int32{8080}, "10.10.14.1", []*topology.Pod{podB1, podB2})
	svcD := createSvc("my-ns", "svc-d", annotations, []int32{8080}, "10.10.15.1", []*topology.Pod{})
	svcE := createSvc("my-ns", "svc-e", annotations, []int32{8080}, "10.10.16.1", []*topology.Pod{})

	apiMatch := createHTTPMatch("api", []string{"GET"}, "/api")
	metricMatch := createHTTPMatch("metric", []string{"POST"}, "/metric")
	rtGrp := createHTTPRouteGroup("my-ns", "rt-grp", []spec.HTTPMatch{apiMatch, metricMatch})

	tt := &topology.ServiceTrafficTarget{
		Service: svcB,
		Name:    "tt",
		Sources: []topology.ServiceTrafficTargetSource{
			{
				ServiceAccount: saA,
				Namespace:      "my-ns",
				Pods:           []*topology.Pod{podA},
			},
			{
				ServiceAccount: saC,
				Namespace:      "my-ns",
				Pods:           []*topology.Pod{podC},
			},
		},
		Destination: topology.ServiceTrafficTargetDestination{
			ServiceAccount: saB,
			Namespace:      "my-ns",
			Ports:          []int32{8080},
			Pods:           []*topology.Pod{podB1, podB2},
		},
		Specs: []topology.TrafficSpec{
			{
				HTTPRouteGroup: rtGrp,
				HTTPMatches: []*spec.HTTPMatch{
					&apiMatch,
					&metricMatch,
				},
			},
		},
	}
	ts := &topology.TrafficSplit{
		Name:      "ts",
		Namespace: "my-ns",
		Service:   svcB,
		Backends: []topology.TrafficSplitBackend{
			{
				Weight:  80,
				Service: svcD,
			},
			{
				Weight:  20,
				Service: svcE,
			},
		},
	}

	podA.Outgoing = []*topology.ServiceTrafficTarget{tt}
	podC.Outgoing = []*topology.ServiceTrafficTarget{tt}
	podB1.Incoming = []*topology.ServiceTrafficTarget{tt}
	podB2.Incoming = []*topology.ServiceTrafficTarget{tt}
	svcB.TrafficTargets = []*topology.ServiceTrafficTarget{tt}
	svcB.TrafficSplits = []*topology.TrafficSplit{ts}

	top := &topology.Topology{
		Services: map[topology.NameNamespace]*topology.Service{
			nn(svcA.Name, svcA.Namespace): svcA,
			nn(svcB.Name, svcB.Namespace): svcB,
			nn(svcD.Name, svcD.Namespace): svcD,
			nn(svcE.Name, svcE.Namespace): svcE,
		},
		Pods: map[topology.NameNamespace]*topology.Pod{
			nn(podA.Name, podA.Namespace):   podA,
			nn(podC.Name, podC.Namespace):   podC,
			nn(podB1.Name, podB1.Namespace): podB1,
			nn(podB2.Name, podB2.Namespace): podB2,
		},
	}
	builder := func() (*topology.Topology, error) {
		return top, nil
	}

	ignored := mk8s.NewIgnored()
	provider := provider.New(TopologyBuilderMock(builder), nil, ignored, 10000, 10001, true, "http")

	got, err := provider.BuildConfig()
	require.NoError(t, err)

	want := &dynamic.Configuration{
		HTTP: &dynamic.HTTPConfiguration{
			Routers: map[string]*dynamic.Router{
				"readiness": readinessRtr,
				"my-ns-svc-b-tt-8080-traffic-target": {
					Rule:        "(Host(`svc-b.my-ns.maesh`) || Host(`10.10.14.1`)) && ((PathPrefix(`/{path:api}`) && Method(`GET`)) || (PathPrefix(`/{path:metric}`) && Method(`POST`)))",
					EntryPoints: []string{"http-10000"},
					Service:     "my-ns-svc-b-tt-8080-traffic-target",
					Middlewares: []string{"my-ns-svc-b", "my-ns-svc-b-tt-whitelist"},
				},
				"my-ns-svc-b-ts-8080-traffic-split": {
					Rule:        "Host(`svc-b.my-ns.maesh`) || Host(`10.10.14.1`)",
					EntryPoints: []string{"http-10000"},
					Service:     "my-ns-svc-b-ts-8080-traffic-split",
					Middlewares: []string{"my-ns-svc-b"},
				},
			},
			Services: map[string]*dynamic.Service{
				"readiness": readinessSvc,
				"my-ns-svc-b-tt-8080-traffic-target": {
					LoadBalancer: &dynamic.ServersLoadBalancer{
						Servers: []dynamic.Server{
							{URL: "http://10.10.2.1:8080"},
							{URL: "http://10.10.2.2:8080"},
						},
						PassHostHeader: getBoolRef(true),
					},
				},
				"my-ns-svc-b-ts-8080-traffic-split": {
					Weighted: &dynamic.WeightedRoundRobin{
						Services: []dynamic.WRRService{
							{
								Name:   "my-ns-svc-b-ts-8080-svc-d-traffic-split-backend",
								Weight: getIntRef(80),
							},
							{
								Name:   "my-ns-svc-b-ts-8080-svc-e-traffic-split-backend",
								Weight: getIntRef(20),
							},
						},
					},
				},
				"my-ns-svc-b-ts-8080-svc-d-traffic-split-backend": {
					LoadBalancer: &dynamic.ServersLoadBalancer{
						Servers: []dynamic.Server{
							{URL: "http://svc-d.my-ns.maesh:8080"},
						},
						PassHostHeader: getBoolRef(true),
					},
				},
				"my-ns-svc-b-ts-8080-svc-e-traffic-split-backend": {
					LoadBalancer: &dynamic.ServersLoadBalancer{
						Servers: []dynamic.Server{
							{URL: "http://svc-e.my-ns.maesh:8080"},
						},
						PassHostHeader: getBoolRef(true),
					},
				},
			},
			Middlewares: map[string]*dynamic.Middleware{
				"my-ns-svc-b": {
					Retry: &dynamic.Retry{Attempts: 2},
					RateLimit: &dynamic.RateLimit{
						Average: 100,
						Burst:   110,
					},
					CircuitBreaker: &dynamic.CircuitBreaker{
						Expression: "LatencyAtQuantileMS(50.0) > 100",
					},
				},
				"my-ns-svc-b-tt-whitelist": {
					IPWhiteList: &dynamic.IPWhiteList{
						SourceRange: []string{
							"10.10.1.1",
							"10.10.3.1",
						},
					},
				},
			},
		},
		TCP: &dynamic.TCPConfiguration{
			Routers:  map[string]*dynamic.TCPRouter{},
			Services: map[string]*dynamic.TCPService{},
		},
	}

	assert.Equal(t, want, got)
}

func nn(name, ns string) topology.NameNamespace {
	return topology.NameNamespace{
		Name:      name,
		Namespace: ns,
	}
}

func createPod(ns, name, sa, ip string) *topology.Pod {
	return &topology.Pod{
		Name:           name,
		Namespace:      ns,
		ServiceAccount: sa,
		IP:             ip,
	}
}

func createSvc(ns, name string, annotations map[string]string, ports []int32, ip string, pods []*topology.Pod) *topology.Service {
	svcPorts := make([]v1.ServicePort, len(ports))
	subsetPorts := make([]v1.EndpointPort, len(ports))
	for i, port := range ports {
		portName := fmt.Sprintf("port-%d", port)
		svcPorts[i] = v1.ServicePort{
			Name:       portName,
			Protocol:   "TCP",
			Port:       port,
			TargetPort: intstr.FromInt(int(port)),
		}

		subsetPorts[i] = v1.EndpointPort{
			Name:     portName,
			Port:     port,
			Protocol: "TCP",
		}
	}

	subsetAddress := make([]v1.EndpointAddress, len(pods))
	for i, pod := range pods {
		subsetAddress[i] = v1.EndpointAddress{IP: pod.IP}
	}

	ep := &v1.Endpoints{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Endpoints",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Subsets: []v1.EndpointSubset{
			{
				Addresses: subsetAddress,
				Ports:     subsetPorts,
			},
		},
	}

	return &topology.Service{
		Name:        name,
		Namespace:   ns,
		Annotations: annotations,
		Ports:       svcPorts,
		ClusterIP:   ip,
		Endpoints:   ep,
	}
}

func createHTTPMatch(name string, methods []string, pathRegex string) spec.HTTPMatch {
	return spec.HTTPMatch{
		Name:      name,
		Methods:   methods,
		PathRegex: pathRegex,
	}
}

func createHTTPRouteGroup(ns, name string, matches []spec.HTTPMatch) *spec.HTTPRouteGroup {
	return &spec.HTTPRouteGroup{
		TypeMeta: metav1.TypeMeta{
			Kind:       "HTTPRouteGroup",
			APIVersion: "specs.smi-spec.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
		},
		Matches: matches,
	}
}

func createTCPRoute(ns, name string) *spec.TCPRoute {
	return &spec.TCPRoute{
		TypeMeta: metav1.TypeMeta{
			Kind:       "TCPRoute",
			APIVersion: "specs.smi-spec.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
	}
}

func getBoolRef(v bool) *bool {
	return &v
}

func getIntRef(v int) *int {
	return &v
}

var readinessRtr = &dynamic.Router{
	Rule:        "Path(`/ping`)",
	EntryPoints: []string{"readiness"},
	Service:     "readiness",
}

var readinessSvc = &dynamic.Service{
	LoadBalancer: &dynamic.ServersLoadBalancer{
		PassHostHeader: getBoolRef(true),
		Servers: []dynamic.Server{
			{
				URL: "http://127.0.0.1:8080",
			},
		},
	},
}
