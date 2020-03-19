package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/deislabs/smi-sdk-go/pkg/apis/access/v1alpha1"
	accessclient "github.com/deislabs/smi-sdk-go/pkg/gen/client/access/clientset/versioned"
	accessinformer "github.com/deislabs/smi-sdk-go/pkg/gen/client/access/informers/externalversions"
	access "github.com/deislabs/smi-sdk-go/pkg/gen/client/access/listers/access/v1alpha1"
	specsclient "github.com/deislabs/smi-sdk-go/pkg/gen/client/specs/clientset/versioned"
	specsinformer "github.com/deislabs/smi-sdk-go/pkg/gen/client/specs/informers/externalversions"
	spec "github.com/deislabs/smi-sdk-go/pkg/gen/client/specs/listers/specs/v1alpha1"
	splitclient "github.com/deislabs/smi-sdk-go/pkg/gen/client/split/clientset/versioned"
	splitinformer "github.com/deislabs/smi-sdk-go/pkg/gen/client/split/informers/externalversions"
	split "github.com/deislabs/smi-sdk-go/pkg/gen/client/split/listers/split/v1alpha2"
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
	trafficTargetLister  access.TrafficTargetLister
	trafficSplitLister   split.TrafficSplitLister
	httpRouteGroupLister spec.HTTPRouteGroupLister
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
	}, nil
}

func (b *TopologyBuilder) Build() (*Topology, error) {
	topology := NewTopology()

	// Group pods by service account.
	pods, err := b.podLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("unable to list Pods: %w", err)
	}
	podsBySa := b.groupPodsByServiceAccount(pods)

	// For each TrafficTarget:
	// - Create a new "Service" for each kubernetes destination pods services. Destination pods are the all the pods
	//   under the current service which have the service account mention in TrafficTarget.Destination.
	// - Create a new "Pod" for each pod found while traversing the destination pods and source pods[2]
	// - Create a new "ServiceTrafficTarget" for each new "Service" and link them together.
	// - And Finally, we link "Pod"s with the "ServiceTrafficTarget". In the Pod.Outgoing for source pods and in
	// Pod.Incoming for destination pods.
	trafficTargets, err := b.trafficTargetLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("unable to list TrafficTargets: %w", err)
	}
	for _, trafficTarget := range trafficTargets {
		destSaKey := NameNamespace{trafficTarget.Destination.Name, trafficTarget.Destination.Namespace}

		// Group destination pods by service.
		podsBySvc, err := b.groupPodsByService(podsBySa[destSaKey])
		if err != nil {
			return nil, fmt.Errorf("unable to group pods by service: %w", err)
		}

		// Build traffic target sources.
		sources := b.buildTrafficTargetSources(topology, trafficTarget, podsBySa)

		for service, pods := range podsBySvc {
			svc := getOrCreateService(topology, service)

			// Find out who are the destination pods.
			destPods := make([]*Pod, len(pods))
			for i, pod := range pods {
				destPods[i] = getOrCreatePod(topology, pod)
			}

			// Find out which port can be used on the destination service.
			destPorts, err := b.getTrafficTargetDestinationPorts(service, trafficTarget)
			if err != nil {
				return nil, fmt.Errorf("unable to find TrafficTarget %q destination ports for service %s: %w", trafficTarget.Name, service.Name, err)
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
				Specs:       nil,
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

	return topology, nil
}

func (b *TopologyBuilder) groupPodsByService(pods []*v1.Pod) (map[*v1.Service][]*v1.Pod, error) {
	podsBySvc := make(map[*v1.Service][]*v1.Pod)

	for _, pod := range pods {
		services, err := b.svcLister.GetPodServices(pod)
		if err != nil {
			return nil, fmt.Errorf("unable to get pod %q services: %w", pod.Name, err)
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

func (b *TopologyBuilder) buildTrafficTargetSources(t *Topology, tt *v1alpha1.TrafficTarget, podsBySa map[NameNamespace][]*v1.Pod) map[NameNamespace]ServiceTrafficTargetSource {
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

func (b *TopologyBuilder) getTrafficTargetDestinationPorts(svc *v1.Service, tt *v1alpha1.TrafficTarget) ([]int32, error) {
	var destPorts []int32

	if tt.Destination.Port != "" {
		port, err := strconv.ParseInt(tt.Destination.Port, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("destination port of TrafficTarget %q is not a valid port: %w", tt.Name, err)
		}
		destPorts = []int32{int32(port)}
	} else {
		for _, port := range svc.Spec.Ports {
			destPorts = append(destPorts, port.TargetPort.IntVal)
		}
	}

	return destPorts, nil
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

func getOrCreateService(topology *Topology, svc *v1.Service) *Service {
	svcKey := NameNamespace{svc.Name, svc.Namespace}

	// Create the service if it doesn't exist yet.
	if _, ok := topology.Services[svcKey]; !ok {
		topology.Services[svcKey] = &Service{
			Name:           svc.Name,
			Namespace:      svc.Namespace,
			Selector:       svc.Spec.Selector,
			Annotations:    svc.Annotations,
			Ports:          svc.Spec.Ports,
			ClusterIP:      svc.Spec.ClusterIP,
			TrafficTargets: make(map[string]*ServiceTrafficTarget),
			TrafficSplits:  make(map[string]*TrafficSplit),
		}
	}

	return topology.Services[svcKey]
}
