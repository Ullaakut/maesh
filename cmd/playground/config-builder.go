package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/containous/maesh/pkg/k8s"
	"github.com/containous/maesh/pkg/providers/base"
	"github.com/containous/traefik/v2/pkg/config/dynamic"
	"github.com/sirupsen/logrus"
)

// TCPPortFinder is capable of retrieving a TCP port mapping for a given service.
type TCPPortFinder interface {
	Find(svc k8s.ServiceWithPort) (int32, bool)
}

type TCPPortFinderMock struct{}

func (t TCPPortFinderMock) Find(_ k8s.ServiceWithPort) (int32, bool) {
	return 10000, true
}

type ConfigBuilder struct {
	logger        logrus.FieldLogger
	namespace     string
	tcpStateTable TCPPortFinder
	defaultMode   string
	minHTTPPort   int32
	maxHTTPPort   int32
}

func New(logger logrus.FieldLogger, defaultMode string, namespace string, tcpStateTable TCPPortFinder, minPortHTTP, maxPortHTTP int32) *ConfigBuilder {
	if tcpStateTable == nil {
		tcpStateTable = TCPPortFinderMock{}
	}
	return &ConfigBuilder{logger: logger, defaultMode: defaultMode, namespace: namespace, tcpStateTable: tcpStateTable, minHTTPPort: minPortHTTP, maxHTTPPort: maxPortHTTP}
}

// Add the match of the HTTPRouteGroup
func (c *ConfigBuilder) BuildConfig(topology *Topology) *dynamic.Configuration {
	config := base.CreateBaseConfigWithReadiness()

	for _, service := range topology.Services {
		serviceMode := c.getServiceMode(service.Annotations[k8s.AnnotationServiceType])

		scheme := base.GetScheme(service.Annotations)
		var middlewaresKey string
		if serviceMode != k8s.ServiceTypeTCP {
			middlewaresKey = fmt.Sprintf("%s-%s", service.Name, service.Namespace)
			if middlewares := c.buildHTTPMiddlewares(service.Annotations); middlewares != nil {
				config.HTTP.Middlewares[middlewaresKey] = middlewares
			}
		}
		for _, tt := range service.TrafficTargets {
			for idPort, port := range tt.Destination.Ports {
				key := buildKey(service.Name, service.Namespace, port, tt.Name, service.Namespace)

				switch serviceMode {
				case k8s.ServiceTypeHTTP:
					sourceIPs := getIpSource(tt.Sources)
					if len(sourceIPs) == 0 || len(tt.Destination.Pods) == 0 {
						continue
					}

					entrypointPort, err := c.getHTTPPort(idPort)
					if err != nil {
						c.logger.Debugf("Mesh HTTP port not found for service %s/%s %d", service.Namespace, service.Name, port)
						continue
					}

					var urls []string
					for _, pod := range tt.Destination.Pods {
						urls = append(urls, fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(pod.IP, strconv.Itoa(int(port)))))
					}
					config.HTTP.Services[key] = buildHTTPLoadBalancer(urls)

					whitelistKey := tt.Name + "-" + service.Namespace + "-" + key + "-whitelist"
					config.HTTP.Middlewares[whitelistKey] = createWhitelistMiddleware(sourceIPs)
					middlewares := []string{whitelistKey}

					if middlewaresKey != "" {
						middlewares = append(middlewares, middlewaresKey)
					}

					rule := buildRuleSnippetFromServiceAndMatch(service.Name, service.Namespace, service.ClusterIP, tt.Specs)
					config.HTTP.Routers[key] = buildHTTPRouter(key, entrypointPort, middlewares, rule)
				case k8s.ServiceTypeTCP:
					if len(tt.Destination.Pods) == 0 {
						continue
					}

					var tcpRouteFound bool
					for _, spec := range tt.Specs {
						if spec.TCPRoute != nil {
							tcpRouteFound = true
							break
						}
					}
					if !tcpRouteFound {
						// there is no TrafficTarget which allow TCP
						continue
					}

					entrypointPort, err := c.getTCPPort(service.Name, service.Namespace, port)
					if err != nil {
						c.logger.Debugf("Mesh TCP port not found for service %s/%s %d", service.Namespace, service.Name, port)
						continue
					}

					var addresses []string
					for _, pod := range tt.Destination.Pods {
						addresses = append(addresses, net.JoinHostPort(pod.IP, strconv.FormatInt(int64(port), 10)))
					}
					config.TCP.Services[key] = buildTCPLoadBalancer(addresses)
					config.TCP.Routers[key] = buildTCPRouter(key, entrypointPort)
				}
			}
		}

		for _, split := range service.TrafficSplits {
			for idPort, port := range service.Ports {
				switch serviceMode {
				case k8s.ServiceTypeHTTP:
					entrypointPort, err := c.getHTTPPort(idPort)
					if err != nil {
						c.logger.Debugf("Mesh HTTP port not found for service %s/%s %d", service.Namespace, service.Name, port.Port)
						continue
					}

					key := buildKey(service.Name, service.Namespace, 0, split.Name, service.Namespace) + "-split"

					var WRRServices []dynamic.WRRService
					for _, backend := range split.Backends {
						//create a service/backend with the url of the service
						sliptKey := fmt.Sprintf("%s-split-%s", key, backend.Service.Name)

						host := fmt.Sprintf("%s.%s.maesh", backend.Service.Name, backend.Service.Namespace)
						address := net.JoinHostPort(host, strconv.FormatInt(int64(port.Port), 10))
						url := fmt.Sprintf("%s://%s", scheme, address)

						config.HTTP.Services[sliptKey] = buildHTTPLoadBalancer([]string{url})
						WRRServices = append(WRRServices, dynamic.WRRService{
							Name:   sliptKey,
							Weight: intToP(int64(backend.Weight)),
						})
					}

					config.HTTP.Services[key] = &dynamic.Service{
						Weighted: &dynamic.WeightedRoundRobin{
							Services: WRRServices,
						},
					}

					rule := fmt.Sprintf("Host(`%s.%s.maesh`) || Host(`%s`)", service.Name, service.Namespace, service.ClusterIP)
					config.HTTP.Routers[key] = buildHTTPRouter(key, entrypointPort, []string{middlewaresKey}, rule)
				case k8s.ServiceTypeTCP:
					entrypointPort, err := c.getTCPPort(service.Name, service.Namespace, port.Port)
					if err != nil {
						c.logger.Debugf("Mesh TCP port not found for service %s/%s %d", service.Namespace, service.Name, port)
						continue
					}
					key := buildKey(service.Name, service.Namespace, 0, split.Name, service.Namespace) + "-split"

					var WRRServices []dynamic.TCPWRRService
					for _, backend := range split.Backends {
						//create a service/backend with the url of the service
						sliptKey := fmt.Sprintf("%s-split-%s", key, backend.Service.Name)

						host := fmt.Sprintf("%s.%s.maesh", backend.Service.Name, backend.Service.Namespace)
						address := net.JoinHostPort(host, strconv.FormatInt(int64(port.Port), 10))

						config.HTTP.Services[sliptKey] = buildHTTPLoadBalancer([]string{address})
						WRRServices = append(WRRServices, dynamic.TCPWRRService{
							Name:   sliptKey,
							Weight: intToP(int64(backend.Weight)),
						})
					}

					config.TCP.Services[key] = &dynamic.TCPService{
						Weighted: &dynamic.TCPWeightedRoundRobin{
							Services: WRRServices,
						},
					}

					config.TCP.Routers[key] = buildTCPRouter(key, entrypointPort)
				}
			}
		}
	}

	return config
}

func buildRuleSnippetFromServiceAndMatch(name, namespace, ip string, specs []TrafficSpec) string {
	var result []string

	for _, spec := range specs {
		for _, match := range spec.HTTPMatches {
			var matchParts []string
			if len(match.PathRegex) > 0 {
				preparedPath := match.PathRegex

				if strings.HasPrefix(match.PathRegex, "/") {
					preparedPath = strings.TrimPrefix(preparedPath, "/")
				}

				matchParts = append(matchParts, fmt.Sprintf("PathPrefix(`/{path:%s}`)", preparedPath))
			}

			if len(match.Methods) > 0 && match.Methods[0] != "*" {
				methods := strings.Join(match.Methods, "`,`")
				matchParts = append(matchParts, fmt.Sprintf("Method(`%s`)", methods))
			}

			if len(matchParts) > 0 {
				matchCond := strings.Join(matchParts, " && ")
				result = append(result, fmt.Sprintf("(%s)", matchCond))
			}
		}
	}
	hostRule := fmt.Sprintf("Host(`%s.%s.maesh`) || Host(`%s`)", name, namespace, ip)

	var matches string
	if len(result) > 0 {
		matches = strings.Join(result, " || ")

		return fmt.Sprintf("(%s) && (%s)", hostRule, matches)
	}

	return hostRule
}

func buildHTTPRouter(key string, entrypointPort int32, middlewares []string, rule string) *dynamic.Router {
	return &dynamic.Router{
		Rule:        rule,
		EntryPoints: []string{fmt.Sprintf("http-%d", entrypointPort)},
		Service:     key,
		Middlewares: middlewares,
	}
}

func buildHTTPLoadBalancer(urls []string) *dynamic.Service {
	var servers []dynamic.Server
	for _, url := range urls {
		server := dynamic.Server{
			URL: url,
		}
		servers = append(servers, server)
	}

	return &dynamic.Service{
		LoadBalancer: &dynamic.ServersLoadBalancer{
			PassHostHeader: base.Bool(true),
			Servers:        servers,
		},
	}
}

func buildTCPLoadBalancer(addresses []string) *dynamic.TCPService {
	var servers []dynamic.TCPServer
	for _, address := range addresses {
		server := dynamic.TCPServer{
			Address: address,
		}
		servers = append(servers, server)
	}

	return &dynamic.TCPService{
		LoadBalancer: &dynamic.TCPServersLoadBalancer{
			Servers: servers,
		},
	}
}

func buildTCPRouter(key string, entrypointPort int32) *dynamic.TCPRouter {
	return &dynamic.TCPRouter{
		Rule:        "HostSNI(`*`)",
		EntryPoints: []string{fmt.Sprintf("tcp-%d", entrypointPort)},
		Service:     key,
	}
}

func (p *ConfigBuilder) getTCPPort(namespace, name string, port int32) (int32, error) {
	meshPort, ok := p.tcpStateTable.Find(k8s.ServiceWithPort{
		Namespace: namespace,
		Name:      name,
		Port:      port,
	})
	if !ok {
		return 0, errors.New("unable to find an available TCP port")
	}

	return meshPort, nil
}

func (p *ConfigBuilder) getHTTPPort(portID int) (int32, error) {
	if p.minHTTPPort+int32(portID) >= p.maxHTTPPort {
		return 0, errors.New("unable to find an available HTTP port")
	}

	return p.minHTTPPort + int32(portID), nil
}

func (c *ConfigBuilder) getServiceMode(mode string) string {
	if mode == "" {
		return c.defaultMode
	}

	return mode
}

func getIpSource(sources map[NameNamespace]ServiceTrafficTargetSource) []string {
	var sourceIPs []string
	for _, source := range sources {
		for _, podSource := range source.Pods {
			sourceIPs = append(sourceIPs, podSource.IP)
		}
	}

	return sourceIPs
}

func (c *ConfigBuilder) buildHTTPMiddlewares(annotations map[string]string) *dynamic.Middleware {
	circuitBreaker := buildCircuitBreakerMiddleware(annotations)
	retry := c.buildRetryMiddleware(annotations)
	rateLimit := c.buildRateLimitMiddleware(annotations)

	if circuitBreaker == nil && retry == nil && rateLimit == nil {
		return nil
	}

	return &dynamic.Middleware{
		CircuitBreaker: circuitBreaker,
		RateLimit:      rateLimit,
		Retry:          retry,
	}
}

func buildCircuitBreakerMiddleware(annotations map[string]string) *dynamic.CircuitBreaker {
	if annotations[k8s.AnnotationCircuitBreakerExpression] != "" {
		expression := annotations[k8s.AnnotationCircuitBreakerExpression]
		if expression != "" {
			return &dynamic.CircuitBreaker{
				Expression: expression,
			}
		}
	}

	return nil
}

func (c *ConfigBuilder) buildRetryMiddleware(annotations map[string]string) *dynamic.Retry {
	if annotations[k8s.AnnotationRetryAttempts] != "" {
		retryAttempts, err := strconv.Atoi(annotations[k8s.AnnotationRetryAttempts])
		if err != nil {
			c.logger.Errorf("Could not parse retry annotation: %v", err)
		}

		if retryAttempts > 0 {
			return &dynamic.Retry{
				Attempts: retryAttempts,
			}
		}
	}

	return nil
}

func (c *ConfigBuilder) buildRateLimitMiddleware(annotations map[string]string) *dynamic.RateLimit {
	if annotations[k8s.AnnotationRateLimitAverage] != "" || annotations[k8s.AnnotationRateLimitBurst] != "" {
		rlAverage, err := strconv.Atoi(annotations[k8s.AnnotationRateLimitAverage])
		if err != nil {
			c.logger.Errorf("Could not parse rateLimit average annotation: %v", err)
		}

		rlBurst, err := strconv.Atoi(annotations[k8s.AnnotationRateLimitBurst])
		if err != nil {
			c.logger.Errorf("Could not parse rateLimit burst annotation: %v", err)
		}

		if rlAverage > 0 && rlBurst > 1 {
			return &dynamic.RateLimit{
				Average: int64(rlAverage),
				Burst:   int64(rlBurst),
			}
		}
	}

	return nil
}

func createWhitelistMiddleware(sourceIPs []string) *dynamic.Middleware {
	// Create middleware.
	return &dynamic.Middleware{
		IPWhiteList: &dynamic.IPWhiteList{
			SourceRange: sourceIPs,
		},
	}
}

func buildKey(serviceName, namespace string, port int32, ttName, ttNamespace string) string {
	// Use the hash of the servicename.namespace.port.traffictargetname.traffictargetnamespace as the key
	// So that we can update services based on their name
	// and not have to worry about duplicates on merges.
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s.%s.%d.%s.%s", serviceName, namespace, port, ttName, ttNamespace)))
	dst := make([]byte, hex.EncodedLen(len(sum)))
	hex.Encode(dst, sum[:])
	fullHash := string(dst)

	return fmt.Sprintf("%.10s-%.10s-%d-%.10s-%.10s-%.16s", serviceName, namespace, port, ttName, ttNamespace, fullHash)
}

func intToP(v int64) *int {
	i := int(v)
	return &i
}
