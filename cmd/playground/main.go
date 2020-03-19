package main

import (
	"context"
	"fmt"
	"log"

	accessClient "github.com/deislabs/smi-sdk-go/pkg/gen/client/access/clientset/versioned"
	specsClient "github.com/deislabs/smi-sdk-go/pkg/gen/client/specs/clientset/versioned"
	splitClient "github.com/deislabs/smi-sdk-go/pkg/gen/client/split/clientset/versioned"
	k8s "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	ctx := context.Background()
	kubeConfig := "/home/jspdown/.config/k3d/k3s-default/kubeconfig.yaml"
	url := ""

	config, err := clientcmd.BuildConfigFromFlags(url, kubeConfig)
	if err != nil {
		log.Fatalf("unable to load kubernetes config from %q: %v", kubeConfig, err)
	}

	client, err := k8s.NewForConfig(config)
	if err != nil {
		log.Fatalf("unable to create k8s client: %v", err)
	}

	smiAccessClient, err := accessClient.NewForConfig(config)
	if err != nil {
		log.Fatalf("unable to create SMI access client: %v", err)
	}

	smiSpecClient, err := specsClient.NewForConfig(config)
	if err != nil {
		log.Fatalf("unable to create SMI spec client: %v", err)
	}

	smiSplitClient, err := splitClient.NewForConfig(config)
	if err != nil {
		log.Fatalf("unable to create SMI spec client: %v", err)
	}

	builder, err := NewTopologyBuilder(ctx, client, smiAccessClient, smiSpecClient, smiSplitClient)
	if err != nil {
		fmt.Printf("unable to create topology builder: %v\n", err)
		return
	}
	topology, err := builder.Build()
	if err != nil {
		fmt.Printf("unable to build topology: %v\n", err)
		return
	}

	fmt.Println(topology.Dump())
}

//
//type TopologyAnalyser struct {
//	svcLister            v1.ServiceLister
//	epLister             v1.EndpointsLister
//	podLister            v1.PodLister
//	trafficTargetLister  access.TrafficTargetLister
//	trafficSplitLister   split.TrafficSplitLister
//	httpRouteGroupLister spec.HTTPRouteGroupLister
//}
//
//func NewTopologyAnalyser(k8sClient k8s.Interface, smiAccessClient accessClient.Interface, smiSpecClient specsClient.Interface, smiSplitClient splitClient.Interface) *TopologyAnalyser {
//	k8sInformerFact := informers.NewSharedInformerFactoryWithOptions(k8sClient, 5*time.Minute)
//	smiAccessInformerFact := accessInformer.NewSharedInformerFactoryWithOptions(smiAccessClient, 5*time.Minute)
//	smiSpecInformerFact := specsInformer.NewSharedInformerFactoryWithOptions(smiSpecClient, 5*time.Minute)
//	smiSplitInformerFact := splitInformer.NewSharedInformerFactoryWithOptions(smiSplitClient, 5*time.Minute)
//
//	svcLister := k8sInformerFact.Core().V1().Services().Lister()
//	epLister := k8sInformerFact.Core().V1().Endpoints().Lister()
//	podLister := k8sInformerFact.Core().V1().Pods().Lister()
//	trafficTargetLister := smiAccessInformerFact.Access().V1alpha1().TrafficTargets().Lister()
//	httpRouteGroupLister := smiSpecInformerFact.Specs().V1alpha1().HTTPRouteGroups().Lister()
//	trafficSplitLister := smiSplitInformerFact.Split().V1alpha2().TrafficSplits().Lister()
//
//	return &TopologyAnalyser{
//		svcLister:            svcLister,
//		epLister:             epLister,
//		podLister:            podLister,
//		trafficTargetLister:  trafficTargetLister,
//		trafficSplitLister:   trafficSplitLister,
//		httpRouteGroupLister: httpRouteGroupLister,
//	}
//}

//type NameNamespace struct {
//	Name      string
//	Namespace string
//}
//
//type Topology struct {
//	Services map[NameNamespace]Service
//	Pods     map[NameNamespace]Pod
//}
//
//type Service struct {
//	Name        string
//	Namespace   string
//	Selector    map[string]string
//	Annotations map[string]string
//	Ports       []int32
//}
//
//type Pod struct {
//	Name           string
//	Namespace      string
//	ServiceAccount string
//	IP             string
//
//	Incoming map[NameNamespace]*Service
//	Outgoing map[NameNamespace]*Service
//}
//
//type Split struct{}

//type Topology struct {
//	Services       map[NameNamespace]Service
//	TrafficTargets map[DestinationServiceAccountNamespace]TrafficTarget
//	TrafficSplits  map[ServiceName]TrafficSplit
//}
//
//type Service struct {
//	Name               string
//	Namespace          string
//	ClusterIP          string
//	Ports              []int32
//	Annotations        map[string]string
//	ServiceAccountPods map[DestinationServiceAccount][]Pod
//}
//
//type Pod struct {
//	Name string
//	IP   string
//}
//
//type TrafficTarget struct {
//	Name         string
//	Namespace    string
//	TrafficSpecs []*TrafficSpec
//	Sources      []TrafficTargetSource
//	Destination  TrafficTargetDestination
//}
//
//type TrafficTargetSource struct {
//	Service   string
//	Namespace string
//}
//
//type TrafficTargetDestination struct {
//	Service   string
//	Namespace string
//	Port      int32
//}
//
//// TODO: TrafficSplit and TrafficSpec
//type TrafficSplit struct{}
//type TrafficSpec struct{}
//
//func (a *TopologyAnalyser) Analyse() (*Topology, error) {
//	var topology T
//	pods, err := a.podLister.List(labels.Everything())
//	if err != nil {
//		return nil, fmt.Errorf("unable to list Pods: %w", err)
//	}
//
//	services, err := a.svcLister.List(labels.Everything())
//	if err != nil {
//		return nil, fmt.Errorf("unable to list Pods: %w", err)
//	}
//
//	endpoints, err := a.epLister.List(labels.Everything())
//	if err != nil {
//		return nil, fmt.Errorf("unable to list Pods: %w", err)
//	}
//
//	trafficTargets, err := a.trafficTargetLister.List(labels.Everything())
//	if err != nil {
//		return nil, fmt.Errorf("unable to list TrafficTargets: %w", err)
//	}
//
//	for _, service := range services {
//
//	}
//	return &Topology{}, nil
//}

//
//func (a *TopologyAnalyser) Analyse() (*dynamic.Configuration, error) {
//	cfg := dynamic.Configuration{}
//
//	pods, err := a.podLister.List(labels.Everything())
//	if err != nil {
//		return nil, fmt.Errorf("unable to list Pods: %w", err)
//	}
//
//	services, err := a.svcLister.List(labels.Everything())
//	if err != nil {
//		return nil, fmt.Errorf("unable to list Pods: %w", err)
//	}
//
//	endpoints, err := a.epLister.List(labels.Everything())
//	if err != nil {
//		return nil, fmt.Errorf("unable to list Pods: %w", err)
//	}
//
//	trafficTargets, err := a.trafficTargetLister.List(labels.Everything())
//	if err != nil {
//		return nil, fmt.Errorf("unable to list TrafficTargets: %w", err)
//	}
//
//	// Group endpoints by service
//	endpointsByService := make(map[string][]*corev1.Endpoints)
//	for _, ep := range endpoints {
//		for _, owner := range ep.OwnerReferences {
//			if owner.Kind != "Service" {
//				continue
//			}
//			key := encodeNameNamespaceKey(owner.Name, ep.Namespace)
//			endpointsByService[key] = append(endpointsByService[key], ep)
//		}
//	}
//
//	// Group services by podIP
//	servicesByPodIP := make(map[string][]*corev1.Service)
//	for _, service := range services {
//		eps := endpointsByService[service.Name]
//
//		for _, ep := range eps {
//			for _, subnet := range ep.Subsets {
//				for _, addr := range subnet.Addresses {
//					servicesByPodIP[addr.IP] = append(servicesByPodIP[addr.IP], service)
//				}
//			}
//		}
//	}
//
//	// Group pods by service by service-account
//	podsByServiceBySa := make(map[string]map[string][]*corev1.Pod)
//	for _, pod := range pods {
//		svcs := servicesByPodIP[pod.Status.PodIP]
//		for _, svc := range svcs {
//			bySa, ok := podsByServiceBySa[pod.Spec.ServiceAccountName]
//			if !ok {
//				podsByServiceBySa[pod.Spec.ServiceAccountName] = make(map[string]*corev1.Pod)
//				bySa = podsByServiceBySa[pod.Spec.ServiceAccountName]
//			}
//
//			key := encodeNameNamespaceKey(svc.Name, svc.Namespace)
//			bySa[key] = append(bySa[key], pod)
//		}
//	}
//
//	for _, trafficTarget := range trafficTargets {
//		dsts := podsByServiceBySa[trafficTarget.Destination.Name]
//
//		for _, src := range trafficTarget.Sources {
//			srcs := podsByServiceBySa[src.Name]
//
//			for key, pods := range srcs {
//				svcName, svcNamespace := decodeNameNamespaceKey(key)
//				key := a.buildResourceKey(svcName, svcNamespace)
//				whitelistKey := fmt.Sprintf("%s-%s-%s", trafficTarget.Name, trafficTarget.Namespace)
//
//				var sourceRange []string
//				for _, pod := range pods {
//					sourceRange = append(sourceRange, pod.Status.PodIP)
//				}
//				whitelistMdlKey := ""
//				cfg.HTTP.Middlewares[whitelistMdlKey] = &dynamic.Middleware{
//					IPWhiteList: &dynamic.IPWhiteList{
//						SourceRange: sourceRange,
//					},
//				}
//
//			}
//		}
//	}
//}

//// Filter pods and exclude ignored namespaces
//func (a *TopologyAnalyser) Analyse() (*dynamic.Configuration, error) {
//	cfg := dynamic.Configuration{}
//
//	pods, err := a.podLister.List(labels.Everything())
//	if err != nil {
//		return nil, fmt.Errorf("unable to list Pods: %w", err)
//	}
//
//	services, err := a.svcLister.List(labels.Everything())
//	if err != nil {
//		return nil, fmt.Errorf("unable to list Pods: %w", err)
//	}
//
//	trafficTargets, err := a.trafficTargetLister.List(labels.Everything())
//	if err != nil {
//		return nil, fmt.Errorf("unable to list TrafficTargets: %w", err)
//	}
//
//	// Group Pods by Namespace and Service Account
//	podsSa := make(map[string][]*corev1.Pod)
//	for _, pod := range pods {
//		key := a.getPodsSaKey(pod.Namespace, pod.Spec.ServiceAccountName)
//		podsSa[key] = append(podsSa[key], pod)
//	}
//
//	// Group Services by Endpoint address/port
//	servicesEp := make(map[string][]*corev1.Service)
//	for _, service := range services {
//		endpoints, err := a.epLister.Endpoints(service.Namespace).Get(service.Name)
//		if err != nil {
//			return nil, fmt.Errorf("unable to get service %q endpoints: %w", service.Name, err)
//		}
//
//		for _, subnet := range endpoints.Subsets {
//			for _, port := range subnet.Ports {
//				for _, address := range subnet.Addresses {
//					key := fmt.Sprintf("%s-%d", address.IP, port.Port)
//					servicesEp[key] = append(servicesEp[key], service)
//				}
//			}
//		}
//	}
//	//key := buildKey(service.Name, service.Namespace, sp.Port, groupedTrafficTarget.Name, groupedTrafficTarget.Namespace)
//
//	for _, target := range trafficTargets {
//		// The current specification doesn't define any other valid source kind.
//		if target.Destination.Kind != "ServiceAccount" {
//			continue
//		}
//		key := a.getPodsSaKey(target.Destination.Namespace, target.Destination.Name)
//		destPods := podsSa[key]
//
//		for _, source := range target.Sources {
//			// The current specification doesn't define any other valid source kind.
//			if source.Kind != "ServiceAccount" {
//				continue
//			}
//
//			key = a.getPodsSaKey(source.Namespace, source.Name)
//
//			var sourceIPs []string
//			for _, pod := range podsSa[key] {
//				sourceIPs = append(sourceIPs, pod.Status.PodIP)
//			}
//			whitelistMdlKey := ""
//			cfg.HTTP.Middlewares[whitelistMdlKey] = &dynamic.Middleware{
//				IPWhiteList: &dynamic.IPWhiteList{
//					SourceRange: sourceIPs,
//				},
//			}
//
//			// ep => depends of the service port/namespace/name
//			// rule => depends of the service namespace, name and clusterIP
//			key = a.getTraefikServiceKey()
//			cfg.HTTP.Routers[key] = &dynamic.Router{
//				Rule:        fmt.Sprintf("Host(`%s.%s.maesh`) || Host(`%s`)", svcName, svcNamespace, ip),
//				EntryPoints: ep,
//				Service:     svc,
//				Middlewares: []string{whitelistMdlKey},
//			}
//		}
//
//		// Router(WhiteList middleware => sourcesPods)
//		// Service(LB => destPods)
//
//		for _, pod := range destPods {
//
//		}
//		// router
//		// services
//	}
//
//	return &cfg, nil
//}
//
//func (a *TopologyAnalyser) getPodsSaKey(namespace, sa string) string {
//	return fmt.Sprintf("%s-%s", namespace, sa)
//}
//
//func (a *TopologyAnalyser) buildKey(svcName, svcNs string, port int32, ttName, ttNs string) string {
//	// Use the hash of the servicename.namespace.port.traffictargetname.traffictargetnamespace as the key
//	// So that we can update services based on their name
//	// and not have to worry about duplicates on merges.
//	sum := sha256.Sum256([]byte(fmt.Sprintf("%s.%s.%d.%s.%s", svcName, svcNs, port, ttName, ttNs)))
//	dst := make([]byte, hex.EncodedLen(len(sum)))
//	hex.Encode(dst, sum[:])
//	fullHash := string(dst)
//
//	return fmt.Sprintf("%.10s-%.10s-%d-%.10s-%.10s-%.16s", svcName, svcNs, port, ttName, ttNs, fullHash)
//}
//
//func encodeNameNamespaceKey(name, namespace string) string {
//	return name + "-" + namespace
//}
//
//func decodeNameNamespaceKey(key string) (string, string) {
//	parts := strings.Split(key, "-")
//
//	return parts[0], parts[1]
//}
//

//
//// SUDO code:
//
//FOR EACH trafficTargets AS trafficTarget:
//	destinationServices = GetDestinationServiceFromServiceAccount(trafficTarget.destination)
//
//	FOR EACH destinationServices AS service:
//		FOR
//	Destination = trafficTarget.destination
//	Sources = trafficTarget.sources
