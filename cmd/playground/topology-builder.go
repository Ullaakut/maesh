package main

import (
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

func NewTopologyBuilder(k8sClient k8s.Interface, smiAccessClient accessclient.Interface, smiSpecClient specsclient.Interface, smiSplitClient splitclient.Interface) *TopologyBuilder {
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

	return &TopologyBuilder{
		svcLister:            svcLister,
		epLister:             epLister,
		podLister:            podLister,
		trafficTargetLister:  trafficTargetLister,
		trafficSplitLister:   trafficSplitLister,
		httpRouteGroupLister: httpRouteGroupLister,
	}
}

//
func (b *TopologyBuilder) Build() (*Topology, error) {
	var topology Topology

	var pods []*v1.Pod
	var trafficTargets []*v1alpha1.TrafficTarget
	var err error

	if pods, err = b.podLister.List(labels.Everything()); err != nil {
		return nil, fmt.Errorf("unable to list Pods: %w", err)
	}

	if trafficTargets, err = b.trafficTargetLister.List(labels.Everything()); err != nil {
		return nil, fmt.Errorf("unable to list TrafficTargets: %w", err)
	}

	// Group pods by service account.
	podsBySa := make(map[NameNamespace][]*v1.Pod)
	for _, pod := range pods {
		saKey := NameNamespace{pod.Spec.ServiceAccountName, pod.Namespace}

		podsBySa[saKey] = append(podsBySa[saKey], pod)
	}

	// Create Services and serviceTrafficTargets.
	for _, trafficTarget := range trafficTargets {
		destSaKey := NameNamespace{trafficTarget.Destination.Name, trafficTarget.Destination.Namespace}
		destK8sPods := podsBySa[destSaKey]

		// Group pods by service.
		podsBySvc := make(map[*v1.Service][]*v1.Pod)
		for _, pod := range destK8sPods {
			services, err := b.svcLister.GetPodServices(pod)
			if err != nil {
				return nil, fmt.Errorf("unable to get pod %q services: %w", pod.Name, err)
			}

			for _, service := range services {
				podsBySvc[service] = append(podsBySvc[service], pod)
			}
		}

		// Build traffic target sources.
		sources := make(map[NameNamespace]ServiceTrafficTargetSource)
		for _, source := range trafficTarget.Sources {
			srcSaKey := NameNamespace{source.Name, source.Namespace}
			srcK8sPods := podsBySa[srcSaKey]

			srcPods := make([]*Pod, len(srcK8sPods))
			for _, pod := range srcK8sPods {
				srcPods = append(srcPods, getOrCreatePod(&topology, pod))
			}

			sources[srcSaKey] = ServiceTrafficTargetSource{
				ServiceAccount: source.Name,
				Namespace:      source.Namespace,
				Pods:           srcPods,
			}
		}

		for service, pods := range podsBySvc {
			svc := getOrCreateService(&topology, service)

			// Find out who are the destination pods.
			destPods := make([]*Pod, len(pods))
			for _, pod := range pods {
				destPods = append(destPods, getOrCreatePod(&topology, pod))
			}

			// Find out which port can be used on the destination service.
			var destPorts []int32
			if trafficTarget.Destination.Port != "" {
				port, err := strconv.ParseInt(trafficTarget.Destination.Port, 10, 32)
				if err != nil {
					return nil, fmt.Errorf("destination port of TrafficTarget %q is not a valid port: %w", trafficTarget.Name, err)
				}
				destPorts = []int32{int32(port)}
			} else {
				for _, port := range service.Spec.Ports {
					destPorts = append(destPorts, port.TargetPort.IntVal)
				}
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

	return &topology, nil
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
