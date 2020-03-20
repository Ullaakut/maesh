package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	access "github.com/deislabs/smi-sdk-go/pkg/apis/access/v1alpha1"
	spec "github.com/deislabs/smi-sdk-go/pkg/apis/specs/v1alpha1"
	split "github.com/deislabs/smi-sdk-go/pkg/apis/split/v1alpha2"
	accessclient "github.com/deislabs/smi-sdk-go/pkg/gen/client/access/clientset/versioned"
	accessinformer "github.com/deislabs/smi-sdk-go/pkg/gen/client/access/informers/externalversions"
	accessLister "github.com/deislabs/smi-sdk-go/pkg/gen/client/access/listers/access/v1alpha1"
	specsclient "github.com/deislabs/smi-sdk-go/pkg/gen/client/specs/clientset/versioned"
	specsinformer "github.com/deislabs/smi-sdk-go/pkg/gen/client/specs/informers/externalversions"
	specLister "github.com/deislabs/smi-sdk-go/pkg/gen/client/specs/listers/specs/v1alpha1"
	splitclient "github.com/deislabs/smi-sdk-go/pkg/gen/client/split/clientset/versioned"
	splitinformer "github.com/deislabs/smi-sdk-go/pkg/gen/client/split/informers/externalversions"
	splitLister "github.com/deislabs/smi-sdk-go/pkg/gen/client/split/listers/split/v1alpha2"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	k8s "k8s.io/client-go/kubernetes"
	listers "k8s.io/client-go/listers/core/v1"
)

type TopologyBuilder struct {
	svcLister            listers.ServiceLister
	epLister             listers.EndpointsLister
	podLister            listers.PodLister
	trafficTargetLister  accessLister.TrafficTargetLister
	trafficSplitLister   splitLister.TrafficSplitLister
	httpRouteGroupLister specLister.HTTPRouteGroupLister
	tcpRoutesLister      specLister.TCPRouteLister
}

func NewTopologyBuilder(ctx context.Context, k8sClient k8s.Interface, smiAccessClient accessclient.Interface, smiSpecClient specsclient.Interface, smiSplitClient splitclient.Interface) (*TopologyBuilder, error) {
	k8sInformerFact := informers.NewSharedInformerFactoryWithOptions(k8sClient, 5*time.Minute)
	smiAccessInformerFact := accessinformer.NewSharedInformerFactoryWithOptions(smiAccessClient, 5*time.Minute)
	smiSpecInformerFact := specsinformer.NewSharedInformerFactoryWithOptions(smiSpecClient, 5*time.Minute)
	smiSplitInformerFact := splitinformer.NewSharedInformerFactoryWithOptions(smiSplitClient, 5*time.Minute)

	svcLister := k8sInformerFact.Core().V1().Services().Lister()
	epLister := k8sInformerFact.Core().V1().Endpoints().Lister()
	podLister := k8sInformerFact.Core().V1().Pods().Lister()
	trafficTargetLister := smiAccessInformerFact.Access().V1alpha1().TrafficTargets().Lister()
	httpRouteGroupLister := smiSpecInformerFact.Specs().V1alpha1().HTTPRouteGroups().Lister()
	tcpRouteLister := smiSpecInformerFact.Specs().V1alpha1().TCPRoutes().Lister()
	trafficSplitLister := smiSplitInformerFact.Split().V1alpha2().TrafficSplits().Lister()

	k8sInformerFact.Start(ctx.Done())
	for _, ok := range k8sInformerFact.WaitForCacheSync(ctx.Done()) {
		if !ok {
			return nil, errors.New("unable to start k8s informers")
		}
	}

	smiAccessInformerFact.Start(ctx.Done())
	for _, ok := range smiAccessInformerFact.WaitForCacheSync(ctx.Done()) {
		if !ok {
			return nil, errors.New("unable to start smi access informers")
		}
	}

	smiSpecInformerFact.Start(ctx.Done())
	for _, ok := range smiSpecInformerFact.WaitForCacheSync(ctx.Done()) {
		if !ok {
			return nil, errors.New("unable to start smi spec informers")
		}
	}

	smiSplitInformerFact.Start(ctx.Done())
	for _, ok := range smiSplitInformerFact.WaitForCacheSync(ctx.Done()) {
		if !ok {
			return nil, errors.New("unable to start smi split informers")
		}
	}

	return &TopologyBuilder{
		svcLister:            svcLister,
		epLister:             epLister,
		podLister:            podLister,
		trafficTargetLister:  trafficTargetLister,
		trafficSplitLister:   trafficSplitLister,
		httpRouteGroupLister: httpRouteGroupLister,
		tcpRoutesLister:      tcpRouteLister,
	}, nil
}

func (b *TopologyBuilder) Build() (*Topology, error) {
	topology := NewTopology()

	services, err := b.svcLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("unable to list Services: %w", err)
	}
	for _, svc := range services {
		svcKey := NameNamespace{svc.Name, svc.Namespace}
		topology.Services[svcKey] = &Service{
			Name:           svc.Name,
			Namespace:      svc.Namespace,
			Selector:       svc.Spec.Selector,
			Annotations:    svc.Annotations,
			Ports:          svc.Spec.Ports,
			ClusterIP:      svc.Spec.ClusterIP,
			TrafficTargets: make(map[string]*ServiceTrafficTarget),
		}
	}

	httpRouteGroups, err := b.httpRouteGroupLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("unable to list HTTPRouteGroups: %w", err)
	}
	for _, httpRouteGroup := range httpRouteGroups {
		key := NameNamespace{httpRouteGroup.Name, httpRouteGroup.Namespace}
		topology.HTTPRouteGroups[key] = httpRouteGroup
	}

	tcpRoutes, err := b.tcpRoutesLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("unable to list TCPRouteGroups")
	}
	for _, tcpRoute := range tcpRoutes {
		key := NameNamespace{tcpRoute.Name, tcpRoute.Namespace}
		topology.TCPRoutes[key] = tcpRoute
	}

	trafficTargets, err := b.trafficTargetLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("unable to list TrafficTargets: %w", err)
	}

	if err := b.buildServicesFromTrafficTargets(topology, trafficTargets); err != nil {
		return nil, err
	}

	trafficSplits, err := b.trafficSplitLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("unable to list TrafficSplits: %w", err)
	}
	if err := b.buildServicesFromTrafficSplits(topology, trafficSplits); err != nil {
		return nil, err
	}

	return topology, nil
}

func (b *TopologyBuilder) buildServicesFromTrafficTargets(topology *Topology, trafficTargets []*access.TrafficTarget) error {
	// Group pods by service account.
	pods, err := b.podLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("unable to list Pods: %w", err)
	}
	podsBySa := b.groupPodsByServiceAccount(pods)

	// For each TrafficTarget:
	// - Create a new "Service" for each kubernetes destination pods services. Destination pods are the all the pods
	//   under the current service which have the service account mention in TrafficTarget.Destination.
	// - Create a new "Pod" for each pod found while traversing the destination pods and source pods[2]
	// - Create a new "ServiceTrafficTarget" for each new "Service" and link them together.
	// - And Finally, we link "Pod"s with the "ServiceTrafficTarget". In the Pod.Outgoing for source pods and in
	// Pod.Incoming for destination pods.
	for _, trafficTarget := range trafficTargets {
		destSaKey := NameNamespace{trafficTarget.Destination.Name, trafficTarget.Destination.Namespace}

		// Group destination pods by service.
		podsBySvc, err := b.groupPodsByService(podsBySa[destSaKey])
		if err != nil {
			return fmt.Errorf("unable to group pods by service: %w", err)
		}

		// Build traffic target sources.
		sources := b.buildTrafficTargetSources(topology, trafficTarget, podsBySa)

		// Build traffic target specs.
		specs, err := b.buildTrafficTargetSpecs(topology, trafficTarget)
		if err != nil {
			return fmt.Errorf("unable to build Specs for TrafficTarget %s/%s: %w", trafficTarget.Namespace, trafficTarget.Name, err)
		}

		for service, pods := range podsBySvc {
			svcKey := NameNamespace{service.Name, service.Namespace}
			svc, ok := topology.Services[svcKey]
			if !ok {
				return fmt.Errorf("unable to find Service %s/%s", service.Namespace, service.Name)
			}

			// Find out who are the destination pods.
			destPods := make([]*Pod, len(pods))
			for i, pod := range pods {
				destPods[i] = getOrCreatePod(topology, pod)
			}

			// Find out which port can be used on the destination service.
			destPorts, err := b.getTrafficTargetDestinationPorts(service, trafficTarget)
			if err != nil {
				return fmt.Errorf("unable to find TrafficTarget %s/%s destination ports for service %s/%s: %w", trafficTarget.Namespace, trafficTarget.Name, service.Namespace, service.Name, err)
			}

			dest := ServiceTrafficTargetDestination{
				ServiceAccount: trafficTarget.Destination.Name,
				Namespace:      trafficTarget.Destination.Namespace,
				Ports:          destPorts,
				Pods:           destPods,
			}

			// Create the ServiceTrafficTarget for the given service.
			svc.TrafficTargets[trafficTarget.Name] = &ServiceTrafficTarget{
				Service:     svc,
				Name:        trafficTarget.Name,
				Sources:     sources,
				Destination: dest,
				Specs:       specs,
			}

			// Add the ServiceTrafficTarget to the source pods.
			for _, source := range sources {
				for _, pod := range source.Pods {
					pod.Outgoing = append(pod.Outgoing, svc.TrafficTargets[trafficTarget.Name])
				}
			}

			// Add the ServiceTrafficTarget to the destination pods
			for _, pod := range dest.Pods {
				pod.Incoming = append(pod.Incoming, svc.TrafficTargets[trafficTarget.Name])
			}
		}
	}

	return nil
}

func (b *TopologyBuilder) buildServicesFromTrafficSplits(topology *Topology, trafficSplits []*split.TrafficSplit) error {
	for _, trafficSplit := range trafficSplits {
		svcKey := NameNamespace{trafficSplit.Spec.Service, trafficSplit.Namespace}
		svc, ok := topology.Services[svcKey]
		if !ok {
			return fmt.Errorf("unable to find Service %s/%s", trafficSplit.Namespace, trafficSplit.Spec.Service)
		}

		backends := make([]TrafficSplitBackend, len(trafficSplit.Spec.Backends))
		for i, backend := range trafficSplit.Spec.Backends {
			backendSvcKey := NameNamespace{backend.Service, trafficSplit.Namespace}
			backendSvc, ok := topology.Services[backendSvcKey]
			if !ok {
				return fmt.Errorf("unable to find Service %s/%s", trafficSplit.Namespace, backend.Service)
			}

			backends[i] = TrafficSplitBackend{
				Weight:  backend.Weight,
				Service: backendSvc,
			}

		}

		svc.TrafficSplits = append(svc.TrafficSplits, &TrafficSplit{
			Name:      trafficSplit.Name,
			Namespace: trafficSplit.Namespace,
			Service:   svc,
			Backends:  backends,
		})
	}

	return nil
}

func (b *TopologyBuilder) groupPodsByService(pods []*v1.Pod) (map[*v1.Service][]*v1.Pod, error) {
	podsBySvc := make(map[*v1.Service][]*v1.Pod)

	for _, pod := range pods {
		services, err := b.svcLister.GetPodServices(pod)
		if err != nil {
			return nil, fmt.Errorf("unable to get pod services for pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}

		for _, service := range services {
			podsBySvc[service] = append(podsBySvc[service], pod)
		}
	}

	return podsBySvc, nil
}

func (b *TopologyBuilder) groupPodsByServiceAccount(pods []*v1.Pod) map[NameNamespace][]*v1.Pod {
	podsBySa := make(map[NameNamespace][]*v1.Pod)

	for _, pod := range pods {
		saKey := NameNamespace{pod.Spec.ServiceAccountName, pod.Namespace}

		podsBySa[saKey] = append(podsBySa[saKey], pod)
	}

	return podsBySa
}

func (b *TopologyBuilder) buildTrafficTargetSources(t *Topology, tt *access.TrafficTarget, podsBySa map[NameNamespace][]*v1.Pod) map[NameNamespace]ServiceTrafficTargetSource {
	sources := make(map[NameNamespace]ServiceTrafficTargetSource)

	for _, source := range tt.Sources {
		srcSaKey := NameNamespace{source.Name, source.Namespace}
		pods := podsBySa[srcSaKey]

		srcPods := make([]*Pod, len(pods))
		for i, pod := range pods {
			srcPods[i] = getOrCreatePod(t, pod)
		}

		sources[srcSaKey] = ServiceTrafficTargetSource{
			ServiceAccount: source.Name,
			Namespace:      source.Namespace,
			Pods:           srcPods,
		}
	}

	return sources
}

func (b *TopologyBuilder) getTrafficTargetDestinationPorts(svc *v1.Service, tt *access.TrafficTarget) ([]int32, error) {
	var destPorts []int32

	if tt.Destination.Port != "" {
		port, err := strconv.ParseInt(tt.Destination.Port, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("destination port of TrafficTarget %s/%s is not a valid port: %w", tt.Namespace, tt.Name, err)
		}
		destPorts = []int32{int32(port)}
	} else {
		for _, port := range svc.Spec.Ports {
			destPorts = append(destPorts, port.TargetPort.IntVal)
		}
	}

	return destPorts, nil
}

func (b *TopologyBuilder) buildTrafficTargetSpecs(topology *Topology, tt *access.TrafficTarget) ([]TrafficSpec, error) {
	var trafficSpecs []TrafficSpec

	for _, s := range tt.Specs {
		var trafficSpec TrafficSpec

		switch s.Kind {
		case "HTTPRouteGroup":
			key := NameNamespace{s.Name, tt.Namespace}
			httpRouteGroup, ok := topology.HTTPRouteGroups[key]
			if !ok {
				return []TrafficSpec{}, fmt.Errorf("unable to get HTTPRouteGroup %s/%s", tt.Namespace, s.Name)
			}

			var httpMatches []*spec.HTTPMatch
			if len(s.Matches) == 0 {
				httpMatches := make([]*spec.HTTPMatch, len(httpRouteGroup.Matches))
				for i, match := range httpRouteGroup.Matches {
					httpMatches[i] = &match
				}
			} else {
				for _, name := range s.Matches {
					var found bool

					for _, match := range httpRouteGroup.Matches {
						found = match.Name == name

						if found {
							httpMatches = append(httpMatches, &match)
							break
						}
					}

					if !found {
						return []TrafficSpec{}, fmt.Errorf("unable to find match %q in HTTPRouteGroup %s/%s", name, tt.Namespace, s.Name)
					}
				}
			}

			trafficSpec = TrafficSpec{
				HTTPRouteGroup: httpRouteGroup,
				HTTPMatches:    httpMatches,
			}
		case "TCPRoute":
			key := NameNamespace{s.Name, tt.Namespace}
			tcpRoute, ok := topology.TCPRoutes[key]
			if !ok {
				return []TrafficSpec{}, fmt.Errorf("unable to get TCPRoute %s/%s", tt.Namespace, s.Name)
			}

			trafficSpec = TrafficSpec{TCPRoute: tcpRoute}
		default:
			return []TrafficSpec{}, fmt.Errorf("unknown spec type: %q", s.Kind)
		}

		trafficSpecs = append(trafficSpecs, trafficSpec)
	}

	return trafficSpecs, nil
}

func getOrCreatePod(topology *Topology, pod *v1.Pod) *Pod {
	podKey := NameNamespace{pod.Name, pod.Namespace}

	if _, ok := topology.Pods[podKey]; !ok {
		topology.Pods[podKey] = &Pod{
			Name:           pod.Name,
			Namespace:      pod.Namespace,
			ServiceAccount: pod.Spec.ServiceAccountName,
			Owner:          pod.OwnerReferences,
			IP:             pod.Status.PodIP,
		}
	}

	return topology.Pods[podKey]
}
