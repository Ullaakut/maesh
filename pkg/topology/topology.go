package topology

import (
	"fmt"
	"strings"

	access "github.com/deislabs/smi-sdk-go/pkg/apis/access/v1alpha1"
	specs "github.com/deislabs/smi-sdk-go/pkg/apis/specs/v1alpha1"
	split "github.com/deislabs/smi-sdk-go/pkg/apis/split/v1alpha2"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type NameNamespace struct {
	Name      string
	Namespace string
}

type Topology struct {
	Services map[NameNamespace]*Service
	Pods     map[NameNamespace]*Pod

	TrafficTargets  map[NameNamespace]*access.TrafficTarget
	TrafficSplits   map[NameNamespace]*split.TrafficSplit
	HTTPRouteGroups map[NameNamespace]*specs.HTTPRouteGroup
	TCPRoutes       map[NameNamespace]*specs.TCPRoute
}

func NewTopology() *Topology {
	return &Topology{
		Services:        make(map[NameNamespace]*Service),
		Pods:            make(map[NameNamespace]*Pod),
		TrafficTargets:  make(map[NameNamespace]*access.TrafficTarget),
		TrafficSplits:   make(map[NameNamespace]*split.TrafficSplit),
		HTTPRouteGroups: make(map[NameNamespace]*specs.HTTPRouteGroup),
		TCPRoutes:       make(map[NameNamespace]*specs.TCPRoute),
	}
}

type Service struct {
	Name        string
	Namespace   string
	Selector    map[string]string
	Annotations map[string]string
	Ports       []corev1.ServicePort
	ClusterIP   string

	TrafficTargets []*ServiceTrafficTarget
	TrafficSplits  []*TrafficSplit
}

type ServiceTrafficTarget struct {
	Service *Service
	Name    string

	Sources     []ServiceTrafficTargetSource
	Destination ServiceTrafficTargetDestination
	Specs       []TrafficSpec
}

type ServiceTrafficTargetSource struct {
	ServiceAccount string
	Namespace      string
	Pods           []*Pod
}

type ServiceTrafficTargetDestination struct {
	ServiceAccount string
	Namespace      string
	Ports          []int32
	Pods           []*Pod
}

type TrafficSpec struct {
	HTTPRouteGroup *specs.HTTPRouteGroup
	TCPRoute       *specs.TCPRoute

	HTTPMatches []*specs.HTTPMatch
}

type Pod struct {
	Name           string
	Namespace      string
	ServiceAccount string
	Owner          []v1.OwnerReference
	IP             string

	Outgoing []*ServiceTrafficTarget
	Incoming []*ServiceTrafficTarget
}

type TrafficSplit struct {
	Name      string
	Namespace string

	Service  *Service
	Backends []TrafficSplitBackend
}

type TrafficSplitBackend struct {
	Weight  int
	Service *Service
}

func (t *Topology) Dump() string {
	var s string

	s += leftPadf(0, "Services:")
	for n, service := range t.Services {
		s += leftPadf(1, "[%s/%s]:", n.Namespace, n.Name)
		s += leftPadf(2, "Name: %s", service.Name)
		s += leftPadf(2, "Namespace: %s", service.Namespace)
		s += leftPadf(2, "ClusterIP: %s", service.ClusterIP)

		if len(service.Annotations) > 0 {
			s += leftPadf(2, "Annotations:")
			for k, v := range service.Annotations {
				shortval := v
				if len(v) > 25 {
					shortval = v[:25]
				}
				if len(shortval) < len(v) {
					shortval += "..."
				}
				s += leftPadf(3, "- %s: %s", k, shortval)
			}
		}

		if len(service.Selector) > 0 {
			s += leftPadf(2, "Selector:")
			for k, v := range service.Selector {
				s += leftPadf(3, "- %s: %s", k, v)
			}
		}

		if len(service.Ports) > 0 {
			s += leftPadf(2, "Ports:")
			for _, v := range service.Ports {
				s += leftPadf(3, "- %d->%d", v.Port, v.TargetPort.IntVal)
			}
		}

		if len(service.TrafficTargets) > 0 {
			s += leftPadf(2, "TrafficTargets:")
			for _, tt := range service.TrafficTargets {
				s += leftPadf(3, "- [%s]", tt.Name)
				s += leftPadf(4, "Name: %s", tt.Name)
				s += leftPadf(4, "Service: %s/%s", tt.Service.Namespace, tt.Service.Name)

				if len(tt.Sources) > 0 {
					s += leftPadf(4, "Sources:")
					for _, source := range tt.Sources {
						s += leftPadf(5, "[%s/%s]", source.Namespace, source.ServiceAccount)
						s += leftPadf(6, "ServiceAccount: %s", source.ServiceAccount)
						s += leftPadf(6, "Namespace: %s", source.Namespace)
						if len(source.Pods) > 0 {
							s += leftPadf(6, "Pods:")
							for _, pod := range source.Pods {
								s += leftPadf(7, "- %s/%s", pod.Namespace, pod.Name)
							}
						}
					}
				}

				s += leftPadf(4, "Destination:")
				s += leftPadf(5, "ServiceAccount: %s", tt.Destination.ServiceAccount)
				s += leftPadf(5, "Namespace: %s", tt.Destination.Namespace)
				if len(tt.Destination.Pods) > 0 {
					s += leftPadf(5, "Pods: %s", tt.Destination.Namespace)
					for _, pod := range tt.Destination.Pods {
						s += leftPadf(6, "- %s/%s", pod.Namespace, pod.Name)
					}
				}

				if len(tt.Specs) > 0 {
					s += leftPadf(4, "Specs:")
					for _, spec := range tt.Specs {
						if spec.HTTPRouteGroup != nil {
							s += leftPadf(5, "[%s:%s-%s]", spec.HTTPRouteGroup.Kind, spec.HTTPRouteGroup.Namespace, spec.HTTPRouteGroup.Name)
							for _, match := range spec.HTTPRouteGroup.Matches {
								s += leftPadf(6, "- name=%s pathRegex=%s methods=%s", match.Name, match.PathRegex, strings.Join(match.Methods, ","))
							}
						}

						if spec.TCPRoute != nil {
							s += leftPadf(5, "[%s:%s-%s]", spec.TCPRoute.Kind, spec.TCPRoute.Namespace, spec.TCPRoute.Name)
						}
					}
				}

			}
		}

		if len(service.TrafficSplits) > 0 {
			s += leftPadf(2, "TrafficSplits:")
			for _, ts := range service.TrafficSplits {
				s += leftPadf(3, "[%s/%s]", ts.Namespace, ts.Name)
				s += leftPadf(4, "Service: %p", ts.Service)

				if len(ts.Backends) > 0 {
					s += leftPadf(4, "Backends:")
					for _, backend := range ts.Backends {
						s += leftPadf(4, "- weight=%d service=%s/%s ", backend.Weight, backend.Service.Namespace, backend.Service.Name)
					}
				}

			}
		}
	}

	if len(t.Pods) > 0 {
		s += leftPadf(0, "Pods:")
		for k, pod := range t.Pods {
			s += leftPadf(1, "[%s/%s]", k.Namespace, k.Name)
			s += leftPadf(2, "Namespace: %s", pod.Namespace)
			s += leftPadf(2, "Name: %s", pod.Name)
			s += leftPadf(2, "IP: %s", pod.IP)

			if len(pod.Outgoing) > 0 {
				s += leftPadf(2, "Outgoing:")
				for _, out := range pod.Outgoing {
					s += leftPadf(3, "- service=%s traffic-target=%s", out.Service.Name, out.Name)
				}
			}

			if len(pod.Incoming) > 0 {
				s += leftPadf(2, "Incoming:")
				for _, in := range pod.Incoming {
					s += leftPadf(3, "- service=%s traffic-target=%s", in.Service.Name, in.Name)
				}
			}
		}
	}

	return s
}

func leftPadf(indent int, format string, args ...interface{}) string {
	var prefix string

	for i := 0; i < indent; i++ {
		prefix += "  "
	}

	return fmt.Sprintf(prefix+format+"\n", args...)
}
