package topology

import (
	access "github.com/deislabs/smi-sdk-go/pkg/apis/access/v1alpha1"
	specs "github.com/deislabs/smi-sdk-go/pkg/apis/specs/v1alpha1"
	split "github.com/deislabs/smi-sdk-go/pkg/apis/split/v1alpha2"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NameNamespace is a key for referencing unique resources.
type NameNamespace struct {
	Name      string
	Namespace string
}

// Topology holds the graph. Each Pods and services are nodes of the graph.
type Topology struct {
	Services map[NameNamespace]*Service
	Pods     map[NameNamespace]*Pod

	TrafficTargets  map[NameNamespace]*access.TrafficTarget
	TrafficSplits   map[NameNamespace]*split.TrafficSplit
	HTTPRouteGroups map[NameNamespace]*specs.HTTPRouteGroup
	TCPRoutes       map[NameNamespace]*specs.TCPRoute
}

// NewTopology create a new Topology.
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

// Service is a node of the graph representing a kubernetes service. A service
// is have links with TrafficTargets and TrafficSplits.
type Service struct {
	Name        string
	Namespace   string
	Selector    map[string]string
	Annotations map[string]string
	Ports       []corev1.ServicePort
	ClusterIP   string
	Endpoints   *corev1.Endpoints

	// List of TrafficTargets that are targeting pods which are selected by this service.
	TrafficTargets []*ServiceTrafficTarget
	// List of TrafficSplits that are targeting this service.
	TrafficSplits []*TrafficSplit
	// List of TrafficSplit mentioning this service as a backend.
	BackendOf []*TrafficSplit
}

// ServiceTrafficTarget represents a TrafficTarget applied a on Service. TrafficTargets have a Destination service
// account. This service account can be set on many pods, each of them, potentially accessible via different services.
// A ServiceTrafficTarget is a TrafficTarget for a Service which exposes a Pod which has the TrafficTarget Destination
// service-account.
type ServiceTrafficTarget struct {
	Service *Service
	Name    string

	Sources     []ServiceTrafficTargetSource
	Destination ServiceTrafficTargetDestination
	Specs       []TrafficSpec
}

// ServiceTrafficTargetSource represents a source of a ServiceTrafficTarget. In the SMI specification, a TrafficTarget
// has a list of sources, each of them being a service-account name. ServiceTrafficTargetSource represents this
// service-account, populated with the pods having this service-account.
type ServiceTrafficTargetSource struct {
	ServiceAccount string
	Namespace      string
	Pods           []*Pod
}

// ServiceTrafficTargetDestination represents a destination of a ServiceTrafficTarget. In the SMI specification, a
// TrafficTarget has a destination service-account. ServiceTrafficTargetDestination holds the pods exposed by the
// Service which has this service-account.
type ServiceTrafficTargetDestination struct {
	ServiceAccount string
	Namespace      string
	Ports          []corev1.ServicePort
	Pods           []*Pod
}

// TrafficSpec represents a Spec which can be used for restricting access to a route in a TrafficTarget.
type TrafficSpec struct {
	HTTPRouteGroup *specs.HTTPRouteGroup
	TCPRoute       *specs.TCPRoute

	HTTPMatches []*specs.HTTPMatch
}

// Pod is a node of the graph representing a kubernetes pod.
type Pod struct {
	Name           string
	Namespace      string
	ServiceAccount string
	Owner          []v1.OwnerReference
	IP             string

	Outgoing []*ServiceTrafficTarget
	Incoming []*ServiceTrafficTarget
}

// TrafficSplit represents a TrafficSplit applied on a Service.
type TrafficSplit struct {
	Name      string
	Namespace string

	Service  *Service
	Backends []TrafficSplitBackend
	// List of Pods that are explicitly allowed to reach this service.
	Incoming []*Pod
}

// TrafficSplitBackend is the backend of a TrafficSplit.
type TrafficSplitBackend struct {
	Weight  int
	Service *Service
}
