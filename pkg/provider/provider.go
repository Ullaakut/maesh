package provider

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/containous/maesh/pkg/k8s"
	"github.com/containous/maesh/pkg/topology"
	"github.com/containous/traefik/v2/pkg/config/dynamic"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	v1 "k8s.io/client-go/listers/core/v1"
)

// TopologyBuilder is capable of building Topologies.
type TopologyBuilder interface {
	Build(ignored k8s.IgnoreWrapper) (*topology.Topology, error)
}

// TCPPortFinder is capable for finding a TCP port mapping.
type TCPPortFinder interface {
	Find(svc k8s.ServiceWithPort) (int32, bool)
}

// When multiple Traefik Routers listen to the same entrypoint and have the same Rule, the chosen router will be the one
// with the highest priority. There are few cases where this priority is crucial when building the dynamic configuration:
// - When a TrafficSplit is set on a k8s service, this will create 2 Traefik Routers. One for accessing the k8s service
//   endpoints and one for accessing the services endpoints mentionned in the TrafficSplit. They both have the same Rule
//   but we should always prioritize the TrafficSplit. Therefore, TrafficSplit Routers should always have a higher priority.
// - When a TrafficTarget Destination targets pods of a k8s service and a TrafficSplit is set on this service. This
//   will create 2 Traefik Routers. One for the TrafficSplit and one for the TrafficTarget. We should always prioritize
//   TrafficSplits Routers and so, TrafficSplit Routers should always have a higher priority than TrafficTarget Routers.
const (
	priorityService               = 1
	priorityTrafficTargetDirect   = 2
	priorityTrafficTargetIndirect = 3
	priorityTrafficSplit          = 4
)

// Provider holds the configuration for generating dynamic configuration from a kubernetes cluster state.
type Provider struct {
	acl                bool
	minHTTPPort        int32
	maxHTTPPort        int32
	defaultTrafficType string
	maeshNamespace     string

	podLister         v1.PodLister
	topologyBuilder   TopologyBuilder
	tcpStateTable     TCPPortFinder
	middlewareBuilder MiddlewareBuilder
	ignored           k8s.IgnoreWrapper

	logger logrus.FieldLogger
}

// New creates a new Provider.
func New(podLister v1.PodLister, topologyBuilder TopologyBuilder, tcpStateTable TCPPortFinder, ignored k8s.IgnoreWrapper, minHTTPPort, maxHTTPPort int32, acl bool, defaultTrafficType, maeshNamespace string, logger logrus.FieldLogger) *Provider {
	return &Provider{
		acl:                acl,
		minHTTPPort:        minHTTPPort,
		maxHTTPPort:        maxHTTPPort,
		defaultTrafficType: defaultTrafficType,
		maeshNamespace:     maeshNamespace,
		podLister:          podLister,
		middlewareBuilder:  &AnnotationBasedMiddlewareBuilder{},
		topologyBuilder:    topologyBuilder,
		tcpStateTable:      tcpStateTable,
		ignored:            ignored,
		logger:             logger,
	}
}

// BuildConfig builds a dynamic configuration.
func (p *Provider) BuildConfig() (*dynamic.Configuration, error) {
	cfg := buildDefaultDynamicConfig()

	t, err := p.topologyBuilder.Build(p.ignored)
	if err != nil {
		return nil, fmt.Errorf("unable to build topology: %w", err)
	}

	maeshProxyIPs, err := p.getMaeshProxyIPs()
	if err != nil {
		return nil, fmt.Errorf("unable to find Maesh proxies: %w", err)
	}

	for _, svc := range t.Services {
		if err := p.buildConfigForService(cfg, svc, maeshProxyIPs); err != nil {
			p.logger.Errorf("Unable to build config for service %s/%s: %w", svc.Namespace, svc.Name, err)
		}
	}

	return cfg, nil
}

func (p *Provider) buildConfigForService(cfg *dynamic.Configuration, svc *topology.Service, maeshProxyIPs []string) error {
	trafficType, err := p.getTrafficTypeAnnotation(svc)
	if err != nil {
		return fmt.Errorf("unable to evaluate traffic-type annotation: %w", err)
	}

	scheme, err := getSchemeAnnotation(svc)
	if err != nil {
		return fmt.Errorf("unable to evaluate scheme annotation: %w", err)
	}

	var middlewares []string

	if trafficType == k8s.ServiceTypeHTTP {
		middleware, err := p.middlewareBuilder.Build(svc)
		if err != nil {
			return fmt.Errorf("unable to build middlewares: %w", err)
		}

		if middleware != nil {
			middlewareKey := getMiddlewareKey(svc)
			cfg.HTTP.Middlewares[middlewareKey] = middleware

			middlewares = append(middlewares, middlewareKey)
		}
	}

	if p.acl {
		for _, tt := range svc.TrafficTargets {
			if err := p.buildServicesAndRoutersForTrafficTargets(cfg, tt, scheme, trafficType, middlewares, maeshProxyIPs); err != nil {
				p.logger.Errorf("Unable to build routers and services for service %s/%s for traffic-target %s: %v", svc.Namespace, svc.Name, tt.Name, err)
				continue
			}
		}

		if trafficType == k8s.ServiceTypeHTTP {
			p.buildBlockAllRouters(cfg, svc)
		}
	} else {
		err := p.buildServicesAndRoutersForService(cfg, svc, scheme, trafficType, middlewares)
		if err != nil {
			return fmt.Errorf("unable to build routers and services: %w", err)
		}
	}

	for _, ts := range svc.TrafficSplits {
		if err := p.buildServiceAndRoutersForTrafficSplits(cfg, ts, scheme, trafficType, middlewares); err != nil {
			p.logger.Errorf("Unable to build routers and services for service %s/%s and traffic-split %s: %v", svc.Namespace, svc.Name, ts.Name, err)
			continue
		}
	}

	return nil
}

func (p *Provider) buildServicesAndRoutersForService(cfg *dynamic.Configuration, svc *topology.Service, scheme, trafficType string, middlewares []string) error {
	switch trafficType {
	case k8s.ServiceTypeHTTP:
		httpRule := fmt.Sprintf("Host(`%s.%s.maesh`) || Host(`%s`)", svc.Name, svc.Namespace, svc.ClusterIP)

		for portID, svcPort := range svc.Ports {
			entrypoint, err := p.buildHTTPEntrypoint(portID)
			if err != nil {
				p.logger.Errorf("Unable to build HTTP entrypoint for Service %s/%s and portID %d: %v", svc.Namespace, svc.Name, portID, err)
				continue
			}

			key := getServiceRouterKeyFromService(svc, svcPort.Port)
			cfg.HTTP.Services[key] = buildHTTPServiceFromService(svc, scheme, svcPort.TargetPort.IntVal)
			cfg.HTTP.Routers[key] = buildHTTPRouter(httpRule, entrypoint, middlewares, key, priorityService)
		}

	case k8s.ServiceTypeTCP:
		rule := buildTCPRouterRule()

		for _, svcPort := range svc.Ports {
			entrypoint, err := p.buildTCPEntrypoint(svc, svcPort.Port)
			if err != nil {
				p.logger.Errorf("Unable to build TCP entrypoint for Service %s/%s and port %d: %v", svc.Namespace, svc.Name, svcPort.Port, err)
				continue
			}

			key := getServiceRouterKeyFromService(svc, svcPort.Port)
			cfg.TCP.Services[key] = buildTCPServiceFromService(svc, svcPort.TargetPort.IntVal)
			cfg.TCP.Routers[key] = buildTCPRouter(rule, entrypoint, key)
		}
	default:
		return fmt.Errorf("unknown traffic-type %q", trafficType)
	}

	return nil
}

func (p *Provider) buildServicesAndRoutersForTrafficTargets(cfg *dynamic.Configuration, tt *topology.ServiceTrafficTarget, scheme, trafficType string, middlewares []string, maeshProxyIPs []string) error {
	switch trafficType {
	case k8s.ServiceTypeHTTP:
		whitelistMiddleware := buildWhitelistMiddlewareFromTrafficTarget(tt)
		whitelistMiddlewareDirectKey := getWhitelistMiddlewareDirectKeyFromTrafficTarget(tt)
		cfg.HTTP.Middlewares[whitelistMiddlewareDirectKey] = whitelistMiddleware

		rule := buildHTTPRuleFromTrafficTarget(tt)

		for portID, svcPort := range tt.Destination.Ports {
			entrypoint, err := p.buildHTTPEntrypoint(portID)
			if err != nil {
				p.logger.Errorf("Unable to build HTTP entrypoint for Service %s/%s and portID %d: %v", tt.Service.Namespace, tt.Service.Name, portID, err)
				continue
			}

			key := getServiceRouterKeyFromTrafficTarget(tt, svcPort.Port)
			rtrMiddlewares := addToSliceCopy(middlewares, whitelistMiddlewareDirectKey)
			cfg.HTTP.Services[key] = buildHTTPServiceFromTrafficTarget(tt, scheme, svcPort.TargetPort.IntVal)
			cfg.HTTP.Routers[key] = buildHTTPRouter(rule, entrypoint, rtrMiddlewares, key, priorityTrafficTargetDirect)

			// If the ServiceTrafficTarget is a backend of at least one TrafficSplit we need an additional router with
			// a whitelist middleware which whitelists based on the X-Forwarded-For header instead of on the RemoteAddr value.
			if len(tt.Service.BackendOf) > 0 {
				whitelistMiddlewareIndirect := buildWhitelistMiddlewareIndirectFromTrafficTarget(tt)
				whitelistMiddlewareIndirectKey := getWhitelistMiddlewareIndirectKeyFromTrafficTarget(tt)
				cfg.HTTP.Middlewares[whitelistMiddlewareIndirectKey] = whitelistMiddlewareIndirect

				indirectKey := getRouterKeyFromTrafficTargetIndirect(tt, svcPort.Port)
				rule = buildHTTPRuleFromTrafficTargetIndirect(tt)
				rtrMiddlewares = addToSliceCopy(middlewares, whitelistMiddlewareIndirectKey)
				cfg.HTTP.Routers[indirectKey] = buildHTTPRouter(rule, entrypoint, rtrMiddlewares, key, priorityTrafficTargetIndirect)
			}
		}
	case k8s.ServiceTypeTCP:
		if !hasTrafficTargetSpecTCPRoute(tt) {
			return nil
		}

		rule := buildTCPRouterRule()

		for _, svcPort := range tt.Destination.Ports {
			entrypoint, err := p.buildTCPEntrypoint(tt.Service, svcPort.Port)
			if err != nil {
				p.logger.Errorf("Unable to build TCP entrypoint for Service %s/%s and port %d: %v", tt.Service.Namespace, tt.Service.Name, svcPort.Port, err)
				continue
			}

			key := getServiceRouterKeyFromTrafficTarget(tt, svcPort.Port)
			cfg.TCP.Services[key] = buildTCPServiceFromTrafficTarget(tt, svcPort.TargetPort.IntVal)
			cfg.TCP.Routers[key] = buildTCPRouter(rule, entrypoint, key)
		}
	default:
		return fmt.Errorf("unknown traffic-type %q", trafficType)
	}

	return nil
}

func (p *Provider) buildServiceAndRoutersForTrafficSplits(cfg *dynamic.Configuration, ts *topology.TrafficSplit, scheme, trafficType string, middlewares []string) error {
	switch trafficType {
	case k8s.ServiceTypeHTTP:
		rule := buildHTTPRuleFromService(ts.Service)

		rtrMiddlewares := middlewares
		if p.acl {
			whitelistMiddleware := buildWhitelistMiddlewareFromTrafficSplit(ts)
			whitelistMiddlewareDirectKey := getWhitelistMiddlewareDirectKeyFromTrafficSplit(ts)
			cfg.HTTP.Middlewares[whitelistMiddlewareDirectKey] = whitelistMiddleware

			rtrMiddlewares = addToSliceCopy(middlewares, whitelistMiddlewareDirectKey)
		}

		for portID, svcPort := range ts.Service.Ports {
			backendSvcs := make([]dynamic.WRRService, len(ts.Backends))

			for i, backend := range ts.Backends {
				key := getServiceRouterKeyFromTrafficSplitBackend(ts, svcPort.Port, backend)
				// This is unclear in SMI's specification if port mapping should be enforced between the Service and the
				// TrafficSplit backends. https://github.com/servicemeshinterface/smi-spec/blob/master/traffic-split.md#ports
				cfg.HTTP.Services[key] = buildHTTPSplitTrafficBackendService(backend, scheme, svcPort.Port)
				backendSvcs[i] = dynamic.WRRService{
					Name:   key,
					Weight: getIntRef(backend.Weight),
				}
			}

			entrypoint, err := p.buildHTTPEntrypoint(portID)
			if err != nil {
				p.logger.Errorf("Unable to build HTTP entrypoint for Service %s/%s and portID %d: %v", ts.Service.Namespace, ts.Service.Name, portID, err)
				continue
			}

			key := getServiceRouterKeyFromTrafficSplit(ts, svcPort.Port)
			cfg.HTTP.Services[key] = buildHTTPServiceFromTrafficSplit(backendSvcs)
			cfg.HTTP.Routers[key] = buildHTTPRouter(rule, entrypoint, rtrMiddlewares, key, priorityTrafficSplit)

			// If the ServiceTrafficSplit is a backend of at least one TrafficSplit we need an additional router with
			// a whitelist middleware which whitelists based on the X-Forwarded-For header instead of on the RemoteAddr value.
			if len(ts.Service.BackendOf) > 0 && p.acl {
				whitelistMiddlewareIndirect := buildWhitelistMiddlewareIndirectFromTrafficSplit(ts)
				whitelistMiddlewareIndirectKey := getWhitelistMiddlewareIndirectKeyTrafficSplit(ts)
				cfg.HTTP.Middlewares[whitelistMiddlewareIndirectKey] = whitelistMiddlewareIndirect

				indirectKey := getRouterKeyFromTrafficSplitIndirect(ts, svcPort.Port)
				rule = buildHTTPRuleFromTrafficSplitIndirect(ts)
				rtrMiddlewaresindirect := addToSliceCopy(middlewares, whitelistMiddlewareIndirectKey)
				cfg.HTTP.Routers[indirectKey] = buildHTTPRouter(rule, entrypoint, rtrMiddlewaresindirect, key, priorityTrafficTargetIndirect)
			}
		}
	case k8s.ServiceTypeTCP:
		tcpRule := buildTCPRouterRule()

		for _, svcPort := range ts.Service.Ports {
			backendSvcs := make([]dynamic.TCPWRRService, len(ts.Backends))

			for i, backend := range ts.Backends {
				key := getServiceRouterKeyFromTrafficSplitBackend(ts, svcPort.Port, backend)
				cfg.TCP.Services[key] = buildTCPSplitTrafficBackendService(backend, svcPort.TargetPort.IntVal)
				backendSvcs[i] = dynamic.TCPWRRService{
					Name:   key,
					Weight: getIntRef(backend.Weight),
				}
			}

			entrypoint, err := p.buildTCPEntrypoint(ts.Service, svcPort.Port)
			if err != nil {
				p.logger.Errorf("Unable to build TCP entrypoint for Service %s/%s and port %d: %v", ts.Service.Namespace, ts.Service.Name, svcPort.Port, err)
				continue
			}

			key := getServiceRouterKeyFromTrafficSplit(ts, svcPort.Port)
			cfg.TCP.Services[key] = buildTCPServiceFromTrafficSplit(backendSvcs)
			cfg.TCP.Routers[key] = buildTCPRouter(tcpRule, entrypoint, key)
		}

	default:
		return fmt.Errorf("unknown traffic-type %q", trafficType)
	}

	return nil
}

func (p *Provider) getMaeshProxyIPs() ([]string, error) {
	req, err := labels.NewRequirement("component", selection.Equals, []string{"maesh-mesh"})
	if err != nil {
		return []string{}, err
	}

	pods, err := p.podLister.Pods(p.maeshNamespace).List(labels.Everything().Add(*req))
	if err != nil {
		return []string{}, fmt.Errorf("unable to get Maesh Proxy pods: %w", err)
	}

	proxyIPs := make([]string, len(pods))

	for i, pod := range pods {
		proxyIPs[i] = pod.Status.PodIP
	}

	return proxyIPs, nil
}

func (p *Provider) buildBlockAllRouters(cfg *dynamic.Configuration, svc *topology.Service) {
	rule := buildHTTPRuleFromService(svc)

	for portID, svcPort := range svc.Ports {
		entrypoint, err := p.buildHTTPEntrypoint(portID)
		if err != nil {
			p.logger.Errorf("unable to build HTTP entrypoint for Service %s/%s and portID %d: %w", svc.Namespace, svc.Name, portID, err)
			continue
		}

		key := getServiceRouterKeyFromService(svc, svcPort.Port)
		cfg.HTTP.Routers[key] = &dynamic.Router{
			EntryPoints: []string{entrypoint},
			Middlewares: []string{blockAllMiddlewareKey},
			Service:     blockAllServiceKey,
			Rule:        rule,
			Priority:    priorityService,
		}
	}
}

func (p Provider) buildHTTPEntrypoint(portID int) (string, error) {
	port := p.minHTTPPort + int32(portID)
	if port >= p.maxHTTPPort {
		return "", errors.New("too many HTTP entrypoint")
	}

	return fmt.Sprintf("http-%d", port), nil
}

func (p Provider) buildTCPEntrypoint(svc *topology.Service, port int32) (string, error) {
	meshPort, ok := p.tcpStateTable.Find(k8s.ServiceWithPort{
		Namespace: svc.Namespace,
		Name:      svc.Name,
		Port:      port,
	})

	if !ok {
		return "", errors.New("port not found")
	}

	return fmt.Sprintf("tcp-%d", meshPort), nil
}

func (p *Provider) getTrafficTypeAnnotation(svc *topology.Service) (string, error) {
	trafficType, ok := svc.Annotations[k8s.AnnotationServiceType]

	if !ok {
		return p.defaultTrafficType, nil
	}

	if trafficType != k8s.ServiceTypeHTTP && trafficType != k8s.ServiceTypeTCP {
		return "", fmt.Errorf("traffic-type annotation references an unknown traffic type %q", trafficType)
	}

	return trafficType, nil
}

func getSchemeAnnotation(svc *topology.Service) (string, error) {
	scheme, ok := svc.Annotations[k8s.AnnotationScheme]

	if !ok {
		return k8s.SchemeHTTP, nil
	}

	if scheme != k8s.SchemeHTTP && scheme != k8s.SchemeH2c && scheme != k8s.SchemeHTTPS {
		return "", fmt.Errorf("scheme annotation references an unknown scheme %q", scheme)
	}

	return scheme, nil
}

func buildHTTPServiceFromService(svc *topology.Service, scheme string, port int32) *dynamic.Service {
	var servers []dynamic.Server

	if len(svc.Endpoints.Subsets) > 0 {
		for _, subnet := range svc.Endpoints.Subsets {
			for _, address := range subnet.Addresses {
				url := net.JoinHostPort(address.IP, strconv.Itoa(int(port)))

				servers = append(servers, dynamic.Server{
					URL: fmt.Sprintf("%s://%s", scheme, url),
				})
			}
		}
	}

	return &dynamic.Service{
		LoadBalancer: &dynamic.ServersLoadBalancer{
			Servers:        servers,
			PassHostHeader: getBoolRef(true),
		},
	}
}

func buildTCPServiceFromService(svc *topology.Service, port int32) *dynamic.TCPService {
	var servers []dynamic.TCPServer

	if len(svc.Endpoints.Subsets) > 0 {
		for _, subnet := range svc.Endpoints.Subsets {
			for _, address := range subnet.Addresses {
				servers = append(servers, dynamic.TCPServer{
					Address: net.JoinHostPort(address.IP, strconv.Itoa(int(port))),
				})
			}
		}
	}

	return &dynamic.TCPService{
		LoadBalancer: &dynamic.TCPServersLoadBalancer{
			Servers: servers,
		},
	}
}

func buildHTTPServiceFromTrafficTarget(tt *topology.ServiceTrafficTarget, scheme string, port int32) *dynamic.Service {
	servers := make([]dynamic.Server, len(tt.Destination.Pods))

	for i, pod := range tt.Destination.Pods {
		url := net.JoinHostPort(pod.IP, strconv.Itoa(int(port)))

		servers[i].URL = fmt.Sprintf("%s://%s", scheme, url)
	}

	return &dynamic.Service{
		LoadBalancer: &dynamic.ServersLoadBalancer{
			Servers:        servers,
			PassHostHeader: getBoolRef(true),
		},
	}
}

func buildTCPServiceFromTrafficTarget(tt *topology.ServiceTrafficTarget, port int32) *dynamic.TCPService {
	servers := make([]dynamic.TCPServer, len(tt.Destination.Pods))

	for i, pod := range tt.Destination.Pods {
		servers[i].Address = net.JoinHostPort(pod.IP, strconv.Itoa(int(port)))
	}

	return &dynamic.TCPService{
		LoadBalancer: &dynamic.TCPServersLoadBalancer{
			Servers: servers,
		},
	}
}

func buildHTTPServiceFromTrafficSplit(backendSvc []dynamic.WRRService) *dynamic.Service {
	return &dynamic.Service{
		Weighted: &dynamic.WeightedRoundRobin{
			Services: backendSvc,
		},
	}
}

func buildTCPServiceFromTrafficSplit(backendSvc []dynamic.TCPWRRService) *dynamic.TCPService {
	return &dynamic.TCPService{
		Weighted: &dynamic.TCPWeightedRoundRobin{
			Services: backendSvc,
		},
	}
}

func buildHTTPSplitTrafficBackendService(backend topology.TrafficSplitBackend, scheme string, port int32) *dynamic.Service {
	server := dynamic.Server{
		URL: fmt.Sprintf("%s://%s.%s.maesh:%d", scheme, backend.Service.Name, backend.Service.Namespace, port),
	}

	return &dynamic.Service{
		LoadBalancer: &dynamic.ServersLoadBalancer{
			Servers:        []dynamic.Server{server},
			PassHostHeader: getBoolRef(false),
		},
	}
}

func buildTCPSplitTrafficBackendService(backend topology.TrafficSplitBackend, port int32) *dynamic.TCPService {
	server := dynamic.TCPServer{
		Address: fmt.Sprintf("%s.%s.maesh:%d", backend.Service.Name, backend.Service.Namespace, port),
	}

	return &dynamic.TCPService{
		LoadBalancer: &dynamic.TCPServersLoadBalancer{
			Servers: []dynamic.TCPServer{server},
		},
	}
}

func buildHTTPRouter(routerRule string, entrypoint string, middlewares []string, svcKey string, priority int) *dynamic.Router {
	return &dynamic.Router{
		EntryPoints: []string{entrypoint},
		Middlewares: middlewares,
		Service:     svcKey,
		Rule:        routerRule,
		Priority:    getRulePriority(routerRule, priority),
	}
}

func buildTCPRouter(routerRule string, entrypoint string, svcKey string) *dynamic.TCPRouter {
	return &dynamic.TCPRouter{
		EntryPoints: []string{entrypoint},
		Service:     svcKey,
		Rule:        routerRule,
	}
}

func hasTrafficTargetSpecTCPRoute(tt *topology.ServiceTrafficTarget) bool {
	for _, spec := range tt.Specs {
		if spec.TCPRoute != nil {
			return true
		}
	}

	return false
}

func buildDefaultDynamicConfig() *dynamic.Configuration {
	return &dynamic.Configuration{
		HTTP: &dynamic.HTTPConfiguration{
			Routers: map[string]*dynamic.Router{
				"readiness": {
					Rule:        "Path(`/ping`)",
					EntryPoints: []string{"readiness"},
					Service:     "readiness",
				},
			},
			Services: map[string]*dynamic.Service{
				"readiness": {
					LoadBalancer: &dynamic.ServersLoadBalancer{
						PassHostHeader: getBoolRef(true),
						Servers: []dynamic.Server{
							{
								URL: "http://127.0.0.1:8080",
							},
						},
					},
				},
				blockAllServiceKey: {
					LoadBalancer: &dynamic.ServersLoadBalancer{},
				},
			},
			Middlewares: map[string]*dynamic.Middleware{
				blockAllMiddlewareKey: {
					IPWhiteList: &dynamic.IPWhiteList{
						SourceRange: []string{"255.255.255.255"},
					},
				},
			},
		},
		TCP: &dynamic.TCPConfiguration{
			Routers:  map[string]*dynamic.TCPRouter{},
			Services: map[string]*dynamic.TCPService{},
		},
	}
}

func addToSliceCopy(items []string, item string) []string {
	cpy := make([]string, len(items)+1)
	copy(cpy, items)
	cpy[len(items)] = item

	return cpy
}

func getBoolRef(v bool) *bool {
	return &v
}

func getIntRef(v int) *int {
	return &v
}
