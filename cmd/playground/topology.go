package main

import (
	"fmt"

	"github.com/deislabs/smi-sdk-go/pkg/apis/specs/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type NameNamespace struct {
	Name      string
	Namespace string
}

type Topology struct {
	Services     map[NameNamespace]*Service
	Pods         map[NameNamespace]*Pod
	TrafficSpecs map[NameNamespace]*TrafficSpec
}

func NewTopology() *Topology {
	return &Topology{
		Services:     make(map[NameNamespace]*Service),
		Pods:         make(map[NameNamespace]*Pod),
		TrafficSpecs: make(map[NameNamespace]*TrafficSpec),
	}

}

type Service struct {
	Name        string
	Namespace   string
	Selector    map[string]string
	Annotations map[string]string
	Ports       []corev1.ServicePort
	ClusterIP   string

	TrafficTargets map[string]*ServiceTrafficTarget
	TrafficSplits  map[string]*TrafficSplit
}

type ServiceTrafficTarget struct {
	Service *Service
	Name    string

	Sources     map[NameNamespace]ServiceTrafficTargetSource
	Destination ServiceTrafficTargetDestination
	Specs       []*TrafficSpec
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
	Name      string
	Namespace string

	HTTPRouteGroup *v1alpha1.HTTPRouteGroup
	TCPRoute       *v1alpha1.TCPRoute
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

	Backends []TrafficSplitBackend
	Specs    []*TrafficSpec
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
				shortval := v[:25]
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
			s += leftPadf(2, "TrafficTarget:")
			for sa, tt := range service.TrafficTargets {
				s += leftPadf(3, "[%s] %p", sa, tt)
				s += leftPadf(4, "Name: %s", tt.Name)
				s += leftPadf(4, "Service: %p", tt.Service)

				if len(tt.Sources) > 0 {
					s += leftPadf(4, "Sources:")
					for ssa, source := range tt.Sources {
						s += leftPadf(5, "[%s/%s]", ssa.Namespace, ssa.Name)
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

			}
		}
	}

	s += leftPadf(0, "Pods:")
	for k, pod := range t.Pods {
		s += leftPadf(1, "[%s/%s]", k.Namespace, k.Name)
		s += leftPadf(2, "Namespace: %s", pod.Namespace)
		s += leftPadf(2, "Name: %s", pod.Name)
		s += leftPadf(2, "IP: %s", pod.IP)

		if len(pod.Outgoing) > 0 {
			s += leftPadf(2, "Outgoing:")
			for _, out := range pod.Outgoing {
				s += leftPadf(3, "- %p (name=%s, service=%s)", out, out.Service.Name, out.Name)
			}
		}

		if len(pod.Incoming) > 0 {
			s += leftPadf(2, "Incoming:")
			for _, in := range pod.Incoming {
				s += leftPadf(3, "- %p (name=%s, service=%s)", in, in.Service.Name, in.Name)
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
