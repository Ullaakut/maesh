package topology

import (
	"fmt"
	"strconv"

	mk8s "github.com/containous/maesh/pkg/k8s"
	access "github.com/deislabs/smi-sdk-go/pkg/apis/access/v1alpha1"
	spec "github.com/deislabs/smi-sdk-go/pkg/apis/specs/v1alpha1"
	accessLister "github.com/deislabs/smi-sdk-go/pkg/gen/client/access/listers/access/v1alpha1"
	specLister "github.com/deislabs/smi-sdk-go/pkg/gen/client/specs/listers/specs/v1alpha1"
	splitLister "github.com/deislabs/smi-sdk-go/pkg/gen/client/split/listers/split/v1alpha2"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	listers "k8s.io/client-go/listers/core/v1"
)

type Builder struct {
	svcLister            listers.ServiceLister
	epLister             listers.EndpointsLister
	podLister            listers.PodLister
	trafficTargetLister  accessLister.TrafficTargetLister
	trafficSplitLister   splitLister.TrafficSplitLister
	httpRouteGroupLister specLister.HTTPRouteGroupLister
	tcpRoutesLister      specLister.TCPRouteLister
}

func NewBuilder(
	svcLister listers.ServiceLister,
	epLister listers.EndpointsLister,
	podLister listers.PodLister,
	trafficTargetLister accessLister.TrafficTargetLister,
	trafficSplitLister splitLister.TrafficSplitLister,
	httpRouteGroupLister specLister.HTTPRouteGroupLister,
	tcpRouteLister specLister.TCPRouteLister) *Builder {

	return &Builder{
		svcLister:            svcLister,
		epLister:             epLister,
		podLister:            podLister,
		trafficTargetLister:  trafficTargetLister,
		trafficSplitLister:   trafficSplitLister,
		httpRouteGroupLister: httpRouteGroupLister,
		tcpRoutesLister:      tcpRouteLister,
	}
}

func (b *Builder) Build(ignored mk8s.IgnoreWrapper) (*Topology, error) {
	topology := NewTopology()

	// Gather resources required for building the graph.
	if err := b.gatherServices(topology, ignored); err != nil {
		return nil, fmt.Errorf("unable to gather Services: %w", err)
	}
	if err := b.gatherTrafficTargets(topology, ignored); err != nil {
		return nil, fmt.Errorf("unable to gather TrafficTargets: %w", err)
	}
	if err := b.gatherTrafficSplits(topology, ignored); err != nil {
		return nil, fmt.Errorf("unable to gather TrafficSplits: %w", err)
	}
	if err := b.gatherHTTPRouteGroups(topology, ignored); err != nil {
		return nil, fmt.Errorf("unable to gather HTTPRouteGroups: %w", err)
	}
	if err := b.gatherTCPRoutes(topology, ignored); err != nil {
		return nil, fmt.Errorf("unable to gather TCPRoutes: %w", err)
	}

	// Build the graph.
	if err := b.evaluateTrafficTargets(topology); err != nil {
		return nil, fmt.Errorf("unable to evaluate TrafficTargets: %w", err)
	}
	if err := b.evaluateTrafficSplits(topology); err != nil {
		return nil, fmt.Errorf("unable to evaluate TrafficSplits: %w", err)
	}

	return topology, nil
}

func (b *Builder) evaluateTrafficTargets(topology *Topology) error {
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
	for _, trafficTarget := range topology.TrafficTargets {
		destSaKey := NameNamespace{trafficTarget.Destination.Name, trafficTarget.Destination.Namespace}

		//
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
			svcTrafficTarget := &ServiceTrafficTarget{
				Service:     svc,
				Name:        trafficTarget.Name,
				Sources:     sources,
				Destination: dest,
				Specs:       specs,
			}
			svc.TrafficTargets = append(svc.TrafficTargets, svcTrafficTarget)

			// Add the ServiceTrafficTarget to the source pods.
			for _, source := range sources {
				for _, pod := range source.Pods {
					pod.Outgoing = append(pod.Outgoing, svcTrafficTarget)
				}
			}

			// Add the ServiceTrafficTarget to the destination pods
			for _, pod := range dest.Pods {
				pod.Incoming = append(pod.Incoming, svcTrafficTarget)
			}
		}
	}

	return nil
}

func (b *Builder) evaluateTrafficSplits(topology *Topology) error {
	for _, trafficSplit := range topology.TrafficSplits {
		svcKey := NameNamespace{trafficSplit.Spec.Service, trafficSplit.Namespace}
		svc, ok := topology.Services[svcKey]
		if !ok {
			return fmt.Errorf("unable to find Service %s/%s", trafficSplit.Namespace, trafficSplit.Spec.Service)
		}

		ts := &TrafficSplit{
			Name:      trafficSplit.Name,
			Namespace: trafficSplit.Namespace,
			Service:   svc,
		}

		backends := make([]TrafficSplitBackend, len(trafficSplit.Spec.Backends))
		for i, backend := range trafficSplit.Spec.Backends {
			backendSvcKey := NameNamespace{backend.Service, trafficSplit.Namespace}
			backendSvc, ok := topology.Services[backendSvcKey]
			if !ok {
				return fmt.Errorf("unable to find Service %s/%s", trafficSplit.Namespace, backend.Service)
			}

			// As required by the SMI specification, backends must expose at least the same ports as the Service on
			// which the TrafficSplit is.
			for _, svcPort := range svc.Ports {
				var portFound bool
				for _, backendPort := range backendSvc.Ports {
					if svcPort.Port == backendPort.Port {
						portFound = true
						break
					}
				}

				if !portFound {
					return fmt.Errorf("port %d must be exposed Service %s/%s in order to be used as a TrafficSplit %s/%s backend",
						svcPort.Port,
						backendSvc.Namespace, backendSvc.Name,
						trafficSplit.Namespace, trafficSplit.Name)
				}
			}

			backends[i] = TrafficSplitBackend{
				Weight:  backend.Weight,
				Service: backendSvc,
			}
			backendSvc.BackendOf = append(backendSvc.BackendOf, ts)

		}
		ts.Backends = backends
		svc.TrafficSplits = append(svc.TrafficSplits, ts)
	}

	return nil
}

func (b *Builder) groupPodsByService(pods []*v1.Pod) (map[*v1.Service][]*v1.Pod, error) {
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

func (b *Builder) groupPodsByServiceAccount(pods []*v1.Pod) map[NameNamespace][]*v1.Pod {
	podsBySa := make(map[NameNamespace][]*v1.Pod)

	for _, pod := range pods {
		saKey := NameNamespace{pod.Spec.ServiceAccountName, pod.Namespace}

		podsBySa[saKey] = append(podsBySa[saKey], pod)
	}

	return podsBySa
}

func (b *Builder) buildTrafficTargetSources(t *Topology, tt *access.TrafficTarget, podsBySa map[NameNamespace][]*v1.Pod) []ServiceTrafficTargetSource {
	sources := make([]ServiceTrafficTargetSource, len(tt.Sources))

	for i, source := range tt.Sources {
		srcSaKey := NameNamespace{source.Name, source.Namespace}
		pods := podsBySa[srcSaKey]

		srcPods := make([]*Pod, len(pods))
		for i, pod := range pods {
			srcPods[i] = getOrCreatePod(t, pod)
		}

		sources[i] = ServiceTrafficTargetSource{
			ServiceAccount: source.Name,
			Namespace:      source.Namespace,
			Pods:           srcPods,
		}
	}

	return sources
}

func (b *Builder) getTrafficTargetDestinationPorts(svc *v1.Service, tt *access.TrafficTarget) ([]v1.ServicePort, error) {
	if tt.Destination.Port == "" {
		return svc.Spec.Ports, nil
	}

	port, err := strconv.ParseInt(tt.Destination.Port, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("destination port of TrafficTarget %s/%s is not a valid port: %w", tt.Namespace, tt.Name, err)
	}

	for _, svcPort := range svc.Spec.Ports {
		if svcPort.TargetPort.IntVal == int32(port) {
			return []v1.ServicePort{svcPort}, nil
		}
	}

	return nil, fmt.Errorf("destination port %d of TrafficTarget %s/%s is not exposed by the service", port, tt.Namespace, tt.Name)
}

func (b *Builder) buildTrafficTargetSpecs(topology *Topology, tt *access.TrafficTarget) ([]TrafficSpec, error) {
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
				httpMatches = make([]*spec.HTTPMatch, len(httpRouteGroup.Matches))
				for i, match := range httpRouteGroup.Matches {
					m := match
					httpMatches[i] = &m
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

func (b *Builder) gatherServices(topology *Topology, ignored mk8s.IgnoreWrapper) error {
	services, err := b.svcLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("unable to list Services: %w", err)
	}
	for _, svc := range services {
		if ignored.IsIgnored(svc.ObjectMeta) {
			continue
		}

		// Error intentionally not handled, a service may not have an endpoints but we still want it listed.
		eps, _ := b.epLister.Endpoints(svc.Namespace).Get(svc.Name)

		svcKey := NameNamespace{svc.Name, svc.Namespace}
		topology.Services[svcKey] = &Service{
			Name:        svc.Name,
			Namespace:   svc.Namespace,
			Selector:    svc.Spec.Selector,
			Annotations: svc.Annotations,
			Ports:       svc.Spec.Ports,
			ClusterIP:   svc.Spec.ClusterIP,
			Endpoints:   eps,
		}
	}

	return nil
}

func (b *Builder) gatherHTTPRouteGroups(topology *Topology, ignored mk8s.IgnoreWrapper) error {
	httpRouteGroups, err := b.httpRouteGroupLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("unable to list HTTPRouteGroups: %w", err)
	}
	for _, httpRouteGroup := range httpRouteGroups {
		if ignored.IsIgnored(httpRouteGroup.ObjectMeta) {
			continue
		}

		key := NameNamespace{httpRouteGroup.Name, httpRouteGroup.Namespace}
		topology.HTTPRouteGroups[key] = httpRouteGroup
	}

	return nil
}

func (b *Builder) gatherTCPRoutes(topology *Topology, ignored mk8s.IgnoreWrapper) error {
	tcpRoutes, err := b.tcpRoutesLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("unable to list TCPRouteGroups")
	}
	for _, tcpRoute := range tcpRoutes {
		if ignored.IsIgnored(tcpRoute.ObjectMeta) {
			continue
		}

		key := NameNamespace{tcpRoute.Name, tcpRoute.Namespace}
		topology.TCPRoutes[key] = tcpRoute
	}

	return nil
}

func (b *Builder) gatherTrafficTargets(topology *Topology, ignored mk8s.IgnoreWrapper) error {
	trafficTargets, err := b.trafficTargetLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("unable to list TrafficTargets: %w", err)
	}
	for _, trafficTarget := range trafficTargets {
		if ignored.IsIgnored(trafficTarget.ObjectMeta) {
			continue
		}

		key := NameNamespace{trafficTarget.Name, trafficTarget.Namespace}
		topology.TrafficTargets[key] = trafficTarget
	}

	return nil
}

func (b *Builder) gatherTrafficSplits(topology *Topology, ignored mk8s.IgnoreWrapper) error {
	trafficSplits, err := b.trafficSplitLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("unable to list TrafficSplits: %w", err)
	}
	for _, trafficSplit := range trafficSplits {
		if ignored.IsIgnored(trafficSplit.ObjectMeta) {
			continue
		}

		key := NameNamespace{trafficSplit.Name, trafficSplit.Namespace}
		topology.TrafficSplits[key] = trafficSplit
	}

	return nil
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
