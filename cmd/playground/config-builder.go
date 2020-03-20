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

/*
Services:
  [whoami/whoami-server]:
    Name: whoami-server
    Namespace: whoami
    ClusterIP: 10.43.231.88
    Annotations:
      - kubectl.kubernetes.io/last-applied-configuration: {"apiVersion":"v1","kind"...
    Selector:
      - app: whoami-server
    Ports:
      - 8080->80
    TrafficTarget:
      [traffic-target-test] 0xc00011ec60
        Name: traffic-target-test
        Service: 0xc00011db20
        Sources:
          [whoami/whoami-client]
            ServiceAccount: whoami-client
            Namespace: whoami
            Pods:
              - whoami/whoami-client
        Destination:
          ServiceAccount: whoami-server
          Namespace: whoami
          Pods: whoami
            - whoami/whoami-server-74788cbb6d-qssxt
            - whoami/whoami-server-74788cbb6d-8gkgn
Pods:
  [whoami/whoami-client]
    Namespace: whoami
    Name: whoami-client
    IP: 10.42.0.7
    Outgoing:
      - 0xc00011ec60 (name=whoami-server, service=traffic-target-test)
  [whoami/whoami-server-74788cbb6d-qssxt]
    Namespace: whoami
    Name: whoami-server-74788cbb6d-qssxt
    IP: 10.42.0.5
    Incoming:
      - 0xc00011ec60 (name=whoami-server, service=traffic-target-test)
  [whoami/whoami-server-74788cbb6d-8gkgn]
    Namespace: whoami
    Name: whoami-server-74788cbb6d-8gkgn
    IP: 10.42.0.6
    Incoming:
      - 0xc00011ec60 (name=whoami-server, service=traffic-target-test)
*/

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
	tcpStateTable TCPPortFinder
	defaultMode   string
	minHTTPPort   int32
	maxHTTPPort   int32
}

func New(logger logrus.FieldLogger, defaultMode string, tcpStateTable TCPPortFinder, minPortHTTP, maxPortHTTP int32) *ConfigBuilder {
	if tcpStateTable == nil {
		tcpStateTable = TCPPortFinderMock{}
	}
	return &ConfigBuilder{logger: logger, defaultMode: defaultMode, tcpStateTable: tcpStateTable, minHTTPPort: minPortHTTP, maxHTTPPort: maxPortHTTP}
}

// Add the match of the HTTPRouteGroup
func (c *ConfigBuilder) BuildConfig(topology *Topology) *dynamic.Configuration {
	config := base.CreateBaseConfigWithReadiness()

	for _, service := range topology.Services {
		serviceMode := c.getServiceMode(service.Annotations[k8s.AnnotationServiceType])

		var middlewaresKey string
		if serviceMode != k8s.ServiceTypeTCP {
			middlewaresKey = fmt.Sprintf("%s-%s", service.Name, service.Namespace)
			config.HTTP.Middlewares[middlewaresKey] = c.buildHTTPMiddlewares(service.Annotations)
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

					config.HTTP.Services[key] = buildHTTPService(tt.Destination.Pods, port)

					whitelistKey := tt.Name + "-" + service.Namespace + "-" + key + "-whitelist"
					config.HTTP.Middlewares[whitelistKey] = createWhitelistMiddleware(sourceIPs)
					middlewares := []string{whitelistKey}

					if middlewaresKey != "" {
						middlewares = append(middlewares, middlewaresKey)
					}

					config.HTTP.Routers[key] = buildHTTPRouter(key, service, entrypointPort, middlewares, tt.Specs)
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

					config.TCP.Services[key] = buildTCPService(tt.Destination.Pods, port)
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

func buildHTTPRouter(key string, service *Service, entrypointPort int32, middlewares []string, specs []TrafficSpec) *dynamic.Router {
	return &dynamic.Router{
		Rule:        buildRuleSnippetFromServiceAndMatch(service.Name, service.Namespace, service.ClusterIP, specs),
		EntryPoints: []string{fmt.Sprintf("http-%d", entrypointPort)},
		Service:     key,
		Middlewares: middlewares,
	}
}

func buildHTTPService(destinationPods []*Pod, port int32) *dynamic.Service {
	var servers []dynamic.Server
	for _, pod := range destinationPods {
		server := dynamic.Server{
			URL: fmt.Sprintf("%s://%s", "http", net.JoinHostPort(pod.IP, strconv.Itoa(int(port)))),
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

func buildTCPRouter(key string, entrypointPort int32) *dynamic.TCPRouter {
	return &dynamic.TCPRouter{
		Rule:        "HostSNI(`*`)",
		EntryPoints: []string{fmt.Sprintf("tcp-%d", entrypointPort)},
		Service:     key,
	}
}

func buildTCPService(destinationPods []*Pod, port int32) *dynamic.TCPService {
	var servers []dynamic.TCPServer
	for _, pod := range destinationPods {
		server := dynamic.TCPServer{
			Address: net.JoinHostPort(pod.IP, strconv.FormatInt(int64(port), 10)),
		}
		servers = append(servers, server)
	}

	return &dynamic.TCPService{
		LoadBalancer: &dynamic.TCPServersLoadBalancer{
			Servers: servers,
		},
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
