package main

import (
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
	TrafficSpecs map[NameNamespace]TrafficSpec
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
