package provider

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/containous/maesh/pkg/k8s"
	"github.com/containous/maesh/pkg/topology"
	"github.com/containous/traefik/v2/pkg/config/dynamic"
)

type TopologyBuilder interface {
	Build(ignored k8s.IgnoreWrapper) (*topology.Topology, error)
}

type TCPPortFinder interface {
	Find(svc k8s.ServiceWithPort) (int32, bool)
}

type Provider struct {
	acl         bool
	minHTTPPort int32
	maxHTTPPort int32

	topologyBuilder TopologyBuilder
	tcpStateTable   TCPPortFinder
	ignored         k8s.IgnoreWrapper
}

func New(topologyBuilder TopologyBuilder, tcpStateTable TCPPortFinder, ignored k8s.IgnoreWrapper, minHTTPPort, maxHTTPPort int32, acl bool) *Provider {
	return &Provider{
		acl:             acl,
		minHTTPPort:     minHTTPPort,
		maxHTTPPort:     maxHTTPPort,
		topologyBuilder: topologyBuilder,
		tcpStateTable:   tcpStateTable,
		ignored:         ignored,
	}
}

func (p *Provider) BuildConfig() (*dynamic.Configuration, error) {
	cfg := buildDefaultDynamicConfig()

	topology, err := p.topologyBuilder.Build(p.ignored)
	if err != nil {
		return nil, fmt.Errorf("unable to build topology: %w", err)
	}

	for _, svc := range topology.Services {
		trafficType, err := getTrafficTypeAnnotation(svc)
		if err != nil {
			return nil, fmt.Errorf("unable to evaluate traffic-type annotation on service %s/%s: %w", svc.Namespace, svc.Name, err)
		}
		scheme, err := getSchemeAnnotation(svc)
		if err != nil {
			return nil, fmt.Errorf("unable to evaluate scheme annotation on service %s/%s: %w", svc.Namespace, svc.Name, err)
		}

		var middlewares []string
		middlewareKey := getMiddlewareKey(svc)
		middleware, err := buildMiddleware(svc)
		if err != nil {
			return nil, fmt.Errorf("unable to build middlewares for service %s/%s: %w", svc.Namespace, svc.Name, err)
		}
		if middleware != nil {
			cfg.HTTP.Middlewares[middlewareKey] = middleware
			middlewares = append(middlewares, middlewareKey)
		}

		if p.acl {
			for _, tt := range svc.TrafficTargets {
				if err := p.buildServicesAndRoutersForTrafficTargets(cfg, tt, scheme, trafficType, middlewares); err != nil {
					return nil, fmt.Errorf("unable to build routers and services for service %s/%s and traffic-split %s: %w", svc.Namespace, svc.Name, tt.Name, err)
				}
			}
		} else {
			if err := p.buildServicesAndRoutersForService(cfg, svc, scheme, trafficType, middlewares); err != nil {
				return nil, fmt.Errorf("unable to build routers and services for service %s/%s: %w", svc.Namespace, svc.Name, err)
			}
		}

		for _, ts := range svc.TrafficSplits {
			if err := p.buildServiceAndRoutersForTrafficSplits(cfg, ts, scheme, trafficType, middlewares); err != nil {
				return nil, fmt.Errorf("unable to build routers and services for service %s/%s and traffic-split %s: %w", svc.Namespace, svc.Name, ts.Name, err)
			}
		}
	}

	return cfg, nil
}

func (p *Provider) buildServicesAndRoutersForService(cfg *dynamic.Configuration, svc *topology.Service, scheme, trafficType string, middlewares []string) error {
	routerRule := fmt.Sprintf("Host(`%s.%s.maesh`) || Host(`%s`)", svc.Name, svc.Namespace, svc.ClusterIP)

	switch trafficType {
	case k8s.ServiceTypeHTTP:
		for portID, svcPort := range svc.Ports {
			port := svcPort.TargetPort.IntVal

			entrypoint, err := p.buildHTTPEntrypoint(portID)
			if err != nil {
				return fmt.Errorf("unable to build HTTP entrypoint for Service %s/%s and portID %d: %w", svc.Namespace, svc.Name, portID, err)
			}

			key := getServiceRouterKeyFromService(svc, port)
			cfg.HTTP.Services[key] = buildHTTPServiceFromService(svc, scheme, port)
			cfg.HTTP.Routers[key] = buildHTTPRouter(routerRule, entrypoint, middlewares, key)
		}

	case k8s.ServiceTypeTCP:
		rule := buildTCPRouterRule()

		for _, svcPort := range svc.Ports {
			port := svcPort.TargetPort.IntVal

			entrypoint, err := p.buildTCPEntrypoint(svc, port)
			if err != nil {
				return fmt.Errorf("unable to build TCP entrypoint for Service %s/%s and port %d: %w", svc.Namespace, svc.Name, port, err)
			}

			key := getServiceRouterKeyFromService(svc, port)
			cfg.TCP.Services[key] = buildTCPServiceFromService(svc, port)
			cfg.TCP.Routers[key] = buildTCPRouter(rule, entrypoint, key)
		}
	default:
		return fmt.Errorf("unknown traffic-type %q for service %s/%s", trafficType, svc.Namespace, svc.Name)
	}

	return nil
}

func (p *Provider) buildServicesAndRoutersForTrafficTargets(cfg *dynamic.Configuration, tt *topology.ServiceTrafficTarget, scheme, trafficType string, middlewares []string) error {
	switch trafficType {
	case k8s.ServiceTypeHTTP:
		whitelistMiddleware := buildWhitelistMiddleware(tt)
		if whitelistMiddleware == nil {
			return nil
		}
		whitelistMiddlewareKey := getWhitelistMiddlewareKey(tt)
		cfg.HTTP.Middlewares[whitelistMiddlewareKey] = whitelistMiddleware
		middlewares = append(middlewares, whitelistMiddlewareKey)

		rule := buildHTTPRouterRule(tt)

		for portID, port := range tt.Destination.Ports {
			entrypoint, err := p.buildHTTPEntrypoint(portID)
			if err != nil {
				return fmt.Errorf("unable to build HTTP entrypoint for Service %s/%s and portID %d: %w", tt.Service.Namespace, tt.Service.Name, portID, err)
			}

			key := getServiceRouterKeyFromTrafficTarget(tt, port)
			cfg.HTTP.Services[key] = buildHTTPServiceFromTrafficTarget(tt, scheme, port)
			cfg.HTTP.Routers[key] = buildHTTPRouter(rule, entrypoint, middlewares, key)
		}
	case k8s.ServiceTypeTCP:
		if !hasTrafficTargetSpecTCPRoute(tt) {
			return nil
		}

		rule := buildTCPRouterRule()

		for _, port := range tt.Destination.Ports {
			entrypoint, err := p.buildTCPEntrypoint(tt.Service, port)
			if err != nil {
				return fmt.Errorf("unable to build TCP entrypoint for Service %s/%s and port %d: %w", tt.Service.Namespace, tt.Service.Name, port, err)
			}

			key := getServiceRouterKeyFromTrafficTarget(tt, port)
			cfg.TCP.Services[key] = buildTCPServiceFromTrafficTarget(tt, port)
			cfg.TCP.Routers[key] = buildTCPRouter(rule, entrypoint, key)
		}
	default:
		return fmt.Errorf("unknown traffic-type %q for service %s/%s", trafficType, tt.Service.Namespace, tt.Service.Name)
	}

	return nil
}

func (p *Provider) buildServiceAndRoutersForTrafficSplits(cfg *dynamic.Configuration, ts *topology.TrafficSplit, scheme, trafficType string, middlewares []string) error {
	routerRule := fmt.Sprintf("Host(`%s.%s.maesh`) || Host(`%s`)", ts.Service.Name, ts.Service.Namespace, ts.Service.ClusterIP)

	switch trafficType {
	case k8s.ServiceTypeHTTP:
		for portID, svcPort := range ts.Service.Ports {
			port := svcPort.TargetPort.IntVal

			backendSvcs := make([]dynamic.WRRService, len(ts.Backends))
			for i, backend := range ts.Backends {
				key := getServiceRouterKeyFromTrafficSplitBackend(ts, port, backend)
				cfg.HTTP.Services[key] = buildHTTPSplitTrafficBackendService(backend, scheme, port)
				backendSvcs[i] = dynamic.WRRService{
					Name:   key,
					Weight: getIntRef(backend.Weight),
				}
			}

			entrypoint, err := p.buildHTTPEntrypoint(portID)
			if err != nil {
				return fmt.Errorf("unable to build HTTP entrypoint for Service %s/%s and portID %d: %w", ts.Service.Namespace, ts.Service.Name, portID, err)
			}

			key := getServiceRouterKeyFromTrafficSplit(ts, port)
			cfg.HTTP.Services[key] = buildHTTPServiceFromTrafficSplit(backendSvcs)
			cfg.HTTP.Routers[key] = buildHTTPRouter(routerRule, entrypoint, middlewares, key)
		}
	case k8s.ServiceTypeTCP:
		for _, svcPort := range ts.Service.Ports {
			port := svcPort.TargetPort.IntVal

			backendSvcs := make([]dynamic.TCPWRRService, len(ts.Backends))
			for i, backend := range ts.Backends {
				key := getServiceRouterKeyFromTrafficSplitBackend(ts, port, backend)
				cfg.TCP.Services[key] = buildTCPSplitTrafficBackendService(backend, port)
				backendSvcs[i] = dynamic.TCPWRRService{
					Name:   key,
					Weight: getIntRef(backend.Weight),
				}
			}

			entrypoint, err := p.buildTCPEntrypoint(ts.Service, port)
			if err != nil {
				return fmt.Errorf("unable to build TCP entrypoint for Service %s/%s and port %d: %w", ts.Service.Namespace, ts.Service.Name, port, err)
			}

			key := getServiceRouterKeyFromTrafficSplit(ts, port)
			cfg.TCP.Services[key] = buildTCPServiceFromTrafficSplit(backendSvcs)
			cfg.TCP.Routers[key] = buildTCPRouter(routerRule, entrypoint, key)
		}

	default:
		return fmt.Errorf("unknown traffic-type %q for service %s/%s", trafficType, ts.Service.Namespace, ts.Service.Name)
	}

	return nil
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
			PassHostHeader: getBoolRef(true),
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

func buildHTTPRouter(routerRule string, entrypoint string, middlewares []string, svcKey string) *dynamic.Router {
	return &dynamic.Router{
		EntryPoints: []string{entrypoint},
		Middlewares: middlewares,
		Service:     svcKey,
		Rule:        routerRule,
	}
}

func buildTCPRouter(routerRule string, entrypoint string, svcKey string) *dynamic.TCPRouter {
	return &dynamic.TCPRouter{
		EntryPoints: []string{entrypoint},
		Service:     svcKey,
		Rule:        routerRule,
	}
}

func buildHTTPRouterRule(tt *topology.ServiceTrafficTarget) string {
	var orRules []string

	for _, spec := range tt.Specs {
		for _, match := range spec.HTTPMatches {
			var matchParts []string

			if len(match.PathRegex) > 0 {
				pathRegex := match.PathRegex

				if strings.HasPrefix(match.PathRegex, "/") {
					pathRegex = strings.TrimPrefix(match.PathRegex, "/")
				}

				matchParts = append(matchParts, fmt.Sprintf("PathPrefix(`/{path:%s}`)", pathRegex))
			}

			if len(match.Methods) > 0 && match.Methods[0] != "*" {
				methods := strings.Join(match.Methods, "`,`")
				matchParts = append(matchParts, fmt.Sprintf("Method(`%s`)", methods))
			}

			if len(matchParts) > 0 {
				matchCond := strings.Join(matchParts, " && ")
				orRules = append(orRules, fmt.Sprintf("(%s)", matchCond))
			}
		}
	}

	hostRule := fmt.Sprintf("Host(`%s.%s.maesh`) || Host(`%s`)", tt.Service.Name, tt.Service.Namespace, tt.Service.ClusterIP)

	if len(orRules) > 0 {
		matches := strings.Join(orRules, " || ")
		if len(orRules) > 1 {
			matches = fmt.Sprintf("(%s)", matches)
		}

		return fmt.Sprintf("(%s) && %s", hostRule, matches)
	}

	return hostRule
}

func buildTCPRouterRule() string {
	return "HostSNI(`*`)"
}

func buildMiddleware(svc *topology.Service) (*dynamic.Middleware, error) {
	var circuitBreaker *dynamic.CircuitBreaker
	var retry *dynamic.Retry
	var rateLimit *dynamic.RateLimit

	// Build circuit-breaker middleware.
	if circuitBreakerExpression, ok := svc.Annotations[k8s.AnnotationCircuitBreakerExpression]; ok {
		circuitBreaker = &dynamic.CircuitBreaker{Expression: circuitBreakerExpression}
	}

	// Build retry middleware.
	if retryAttempts, ok := svc.Annotations[k8s.AnnotationRetryAttempts]; ok {
		attempts, err := strconv.Atoi(retryAttempts)
		if err != nil {
			return nil, fmt.Errorf("unable to build retry middleware, %q annotation is invalid: %w", k8s.AnnotationRetryAttempts, err)
		}

		retry = &dynamic.Retry{Attempts: attempts}
	}

	// Build rate-limit middleware.
	rateLimitAverage, hasRateLimitAverage := svc.Annotations[k8s.AnnotationRateLimitAverage]
	rateLimitBurst, hasRateLimitBurst := svc.Annotations[k8s.AnnotationRateLimitBurst]
	if hasRateLimitAverage && hasRateLimitBurst {
		average, err := strconv.Atoi(rateLimitAverage)
		if err != nil {
			return nil, fmt.Errorf("unable to build rate-limit middleware, %q annotaiton is invalid: %w", k8s.AnnotationRateLimitAverage, err)
		}

		burst, err := strconv.Atoi(rateLimitBurst)
		if err != nil {
			return nil, fmt.Errorf("unable to build rate-limit middleware, %q annotaiton is invalid: %w", k8s.AnnotationRateLimitBurst, err)
		}

		if burst <= 0 || average <= 0 {
			return nil, errors.New("unable to build rate-limit middleware, burst and average must be greater than 0")
		}

		rateLimit = &dynamic.RateLimit{
			Average: int64(average),
			Burst:   int64(burst),
		}
	}

	if circuitBreaker == nil && retry == nil && rateLimit == nil {
		return nil, nil
	}

	return &dynamic.Middleware{
		CircuitBreaker: circuitBreaker,
		RateLimit:      rateLimit,
		Retry:          retry,
	}, nil
}

func buildWhitelistMiddleware(tt *topology.ServiceTrafficTarget) *dynamic.Middleware {
	var IPs []string
	for _, source := range tt.Sources {
		for _, pod := range source.Pods {
			IPs = append(IPs, pod.IP)
		}
	}

	if len(IPs) == 0 {
		return nil
	}

	return &dynamic.Middleware{
		IPWhiteList: &dynamic.IPWhiteList{
			SourceRange: IPs,
		},
	}
}

func getTrafficTypeAnnotation(svc *topology.Service) (string, error) {
	trafficType, ok := svc.Annotations[k8s.AnnotationServiceType]

	if !ok {
		return k8s.ServiceTypeHTTP, nil
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

func getMiddlewareKey(svc *topology.Service) string {
	return fmt.Sprintf("%s-%s", svc.Namespace, svc.Name)
}

func getServiceRouterKeyFromService(svc *topology.Service, port int32) string {
	return fmt.Sprintf("%s-%s-%d", svc.Namespace, svc.Name, port)
}

func getWhitelistMiddlewareKey(tt *topology.ServiceTrafficTarget) string {
	return fmt.Sprintf("%s-%s-%s-whitelist", tt.Service.Namespace, tt.Service.Name, tt.Name)
}

func getServiceRouterKeyFromTrafficTarget(tt *topology.ServiceTrafficTarget, port int32) string {
	return fmt.Sprintf("%s-%s-%s-%d-traffic-target", tt.Service.Namespace, tt.Service.Name, tt.Name, port)
}

func getServiceRouterKeyFromTrafficSplit(ts *topology.TrafficSplit, port int32) string {
	return fmt.Sprintf("%s-%s-%s-%d-traffic-split", ts.Service.Namespace, ts.Service.Name, ts.Name, port)
}

func getServiceRouterKeyFromTrafficSplitBackend(ts *topology.TrafficSplit, port int32, backend topology.TrafficSplitBackend) string {
	return fmt.Sprintf("%s-%s-%s-%d-%s-traffic-split-backend", ts.Service.Namespace, ts.Service.Name, ts.Name, port, backend.Service.Name)
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
			},
			Middlewares: map[string]*dynamic.Middleware{},
		},
		TCP: &dynamic.TCPConfiguration{
			Routers:  map[string]*dynamic.TCPRouter{},
			Services: map[string]*dynamic.TCPService{},
		},
	}
}

func getBoolRef(v bool) *bool {
	return &v
}

func getIntRef(v int) *int {
	return &v
}
