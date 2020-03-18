package main

import (
	"fmt"
	"log"
	"time"

	"github.com/containous/traefik/v2/pkg/config/dynamic"
	"k8s.io/apimachinery/pkg/labels"

	corev1 "k8s.io/api/core/v1"

	"github.com/davecgh/go-spew/spew"
	accessClient "github.com/deislabs/smi-sdk-go/pkg/gen/client/access/clientset/versioned"
	accessInformer "github.com/deislabs/smi-sdk-go/pkg/gen/client/access/informers/externalversions"
	access "github.com/deislabs/smi-sdk-go/pkg/gen/client/access/listers/access/v1alpha1"
	specsClient "github.com/deislabs/smi-sdk-go/pkg/gen/client/specs/clientset/versioned"
	specsInformer "github.com/deislabs/smi-sdk-go/pkg/gen/client/specs/informers/externalversions"
	spec "github.com/deislabs/smi-sdk-go/pkg/gen/client/specs/listers/specs/v1alpha1"
	splitClient "github.com/deislabs/smi-sdk-go/pkg/gen/client/split/clientset/versioned"
	splitInformer "github.com/deislabs/smi-sdk-go/pkg/gen/client/split/informers/externalversions"
	split "github.com/deislabs/smi-sdk-go/pkg/gen/client/split/listers/split/v1alpha2"
	"k8s.io/client-go/informers"
	k8s "k8s.io/client-go/kubernetes"
	v1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	kubeConfig := ""
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

	analizer := NewTopologyAnalyser(client, smiAccessClient, smiSpecClient, smiSplitClient)

	topology, err := analizer.Analyse()
	if err != nil {
		fmt.Printf("Unable to analyse topology: %v\n", err)
	}

	spew.Dump(topology)
}

type TopologyAnalyser struct {
	svcLister            v1.ServiceLister
	epLister             v1.EndpointsLister
	podLister            v1.PodLister
	trafficTargetLister  access.TrafficTargetLister
	trafficSplitLister   split.TrafficSplitLister
	httpRouteGroupLister spec.HTTPRouteGroupLister
}

func NewTopologyAnalyser(k8sClient k8s.Interface, smiAccessClient accessClient.Interface, smiSpecClient specsClient.Interface, smiSplitClient splitClient.Interface) *TopologyAnalyser {
	k8sInformerFact := informers.NewSharedInformerFactoryWithOptions(k8sClient, 5*time.Minute)
	smiAccessInformerFact := accessInformer.NewSharedInformerFactoryWithOptions(smiAccessClient, 5*time.Minute)
	smiSpecInformerFact := specsInformer.NewSharedInformerFactoryWithOptions(smiSpecClient, 5*time.Minute)
	smiSplitInformerFact := splitInformer.NewSharedInformerFactoryWithOptions(smiSplitClient, 5*time.Minute)

	svcLister := k8sInformerFact.Core().V1().Services().Lister()
	epLister := k8sInformerFact.Core().V1().Endpoints().Lister()
	podLister := k8sInformerFact.Core().V1().Pods().Lister()
	trafficTargetLister := smiAccessInformerFact.Access().V1alpha1().TrafficTargets().Lister()
	httpRouteGroupLister := smiSpecInformerFact.Specs().V1alpha1().HTTPRouteGroups().Lister()
	trafficSplitLister := smiSplitInformerFact.Split().V1alpha2().TrafficSplits().Lister()

	return &TopologyAnalyser{
		svcLister:            svcLister,
		epLister:             epLister,
		podLister:            podLister,
		trafficTargetLister:  trafficTargetLister,
		trafficSplitLister:   trafficSplitLister,
		httpRouteGroupLister: httpRouteGroupLister,
	}
}

// Filter pods and exclude ignored namespaces
func (a *TopologyAnalyser) Analyse() (*dynamic.Configuration, error) {
	cfg := dynamic.Configuration{}

	pods, err := a.podLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("unable to list Pods: %w", err)
	}

	services, err := a.svcLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("unable to list Pods: %w", err)
	}

	trafficTargets, err := a.trafficTargetLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("unable to list TrafficTargets: %w", err)
	}

	// Group Pods by Namespace and Service Account
	podsSa := make(map[string][]*corev1.Pod)
	for _, pod := range pods {
		key := a.getPodsSaKey(pod.Namespace, pod.Spec.ServiceAccountName)
		podsSa[key] = append(podsSa[key], pod)
	}

	// Group Services by Endpoint address/port
	servicesEp := make(map[string][]*corev1.Service)
	for _, service := range services {
		endpoints, err := a.epLister.Endpoints(service.Namespace).Get(service.Name)
		if err != nil {
			return nil, fmt.Errorf("unable to get service %q endpoints: %w", service.Name, err)
		}

		for _, subnet := range endpoints.Subsets {
			for _, port := range subnet.Ports {
				for _, address := range subnet.Addresses {
					key := fmt.Sprintf("%s-%d", address.IP, port.Port)
					servicesEp[key] = append(servicesEp[key], service)
				}
			}
		}
	}

	for _, target := range trafficTargets {
		var sourcesPods []*corev1.Pod

		// The current specification doesn't define any other valid source kind.
		if target.Destination.Kind != "ServiceAccount" {
			continue
		}
		key := a.getPodsSaKey(target.Destination.Namespace, target.Destination.Name)
		destPods := podsSa[key]

		for _, source := range target.Sources {
			// The current specification doesn't define any other valid source kind.
			if source.Kind != "ServiceAccount" {
				continue
			}

			key = a.getPodsSaKey(source.Namespace, source.Name)
			sourcesPods = append(sourcesPods, podsSa[key]...)
		}

		for _, pod := range destPods {

		}
		// router
		// services
	}

	return &cfg, nil
}

func (a *TopologyAnalyser) GetPods(serviceAccount string) *corev1.Pod {
}

func (a *TopologyAnalyser) getPodsSaKey(namespace, sa string) string {
	return fmt.Sprintf("%s-%s", namespace, sa)
}
