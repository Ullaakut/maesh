package provider

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/containous/maesh/pkg/k8s"
	"github.com/containous/maesh/pkg/topology"
	"github.com/containous/traefik/v2/pkg/config/dynamic"
)

type MiddlewareBuilder interface {
	Build(svc *topology.Service) (*dynamic.Middleware, error)
}

type DefaultMiddlewareBuilder struct{}

func (b *DefaultMiddlewareBuilder) Build(svc *topology.Service) (*dynamic.Middleware, error) {
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

// buildWhitelistMiddleware builds an IPWhiteList middleware which blocks every requests except those originating
// from an authorized Pod. Authorized Pods are all the Pods under the Service with a ServiceAccount listed in the
// TrafficTarget (all the Pods listed in the ServiceTrafficTarget.Sources). This middleware doesn't work if there's a
// proxy between the authorized Pod and this Maesh proxy.
func buildWhitelistMiddleware(tt *topology.ServiceTrafficTarget) *dynamic.Middleware {
	var IPs []string
	for _, source := range tt.Sources {
		for _, pod := range source.Pods {
			IPs = append(IPs, pod.IP)
		}
	}

	return &dynamic.Middleware{
		IPWhiteList: &dynamic.IPWhiteList{
			SourceRange: IPs,
		},
	}
}

// buildWhitelistMiddlewareIndirect builds an IPWhiteList middleware like buildWhitelistMiddleware except it's intended
// to be used when there is at least one proxy between the authorized Pod and this Maesh proxy.
func buildWhitelistMiddlewareIndirect(tt *topology.ServiceTrafficTarget, maeshProxyIPs []string) *dynamic.Middleware {
	whitelist := buildWhitelistMiddleware(tt)
	whitelist.IPWhiteList.IPStrategy = &dynamic.IPStrategy{
		ExcludedIPs: maeshProxyIPs,
	}

	return whitelist
}
