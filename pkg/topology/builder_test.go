package topology_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	mk8s "github.com/containous/maesh/pkg/k8s"
	"github.com/containous/maesh/pkg/topology"
	access "github.com/deislabs/smi-sdk-go/pkg/apis/access/v1alpha1"
	spec "github.com/deislabs/smi-sdk-go/pkg/apis/specs/v1alpha1"
	split "github.com/deislabs/smi-sdk-go/pkg/apis/split/v1alpha2"
	accessclient "github.com/deislabs/smi-sdk-go/pkg/gen/client/access/clientset/versioned"
	accessfake "github.com/deislabs/smi-sdk-go/pkg/gen/client/access/clientset/versioned/fake"
	accessInformer "github.com/deislabs/smi-sdk-go/pkg/gen/client/access/informers/externalversions"
	specsclient "github.com/deislabs/smi-sdk-go/pkg/gen/client/specs/clientset/versioned"
	specfake "github.com/deislabs/smi-sdk-go/pkg/gen/client/specs/clientset/versioned/fake"
	specsInformer "github.com/deislabs/smi-sdk-go/pkg/gen/client/specs/informers/externalversions"
	splitclient "github.com/deislabs/smi-sdk-go/pkg/gen/client/split/clientset/versioned"
	splitfake "github.com/deislabs/smi-sdk-go/pkg/gen/client/split/clientset/versioned/fake"
	splitInformer "github.com/deislabs/smi-sdk-go/pkg/gen/client/split/informers/externalversions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/informers"
	k8s "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// TestTopologyBuilder_BuildIgnoresNamespaces makes sure namespace to ignore are ignored by the TopologyBuilder.
func TestTopologyBuilder_BuildIgnoresNamespaces(t *testing.T) {
	selectorAppA := map[string]string{"app": "app-a"}
	selectorAppB := map[string]string{"app": "app-b"}
	annotations := map[string]string{
		"maesh.containo.us/traffic-type":      "http",
		"maesh.containo.us/ratelimit-average": "100",
		"maesh.containo.us/ratelimit-burst":   "200",
	}

	saA := createServiceAccount("ignored-ns", "service-account-a")
	podA := createPod("ignored-ns", "app-a", saA, selectorAppA, "10.10.1.1")

	saB := createServiceAccount("ignored-ns", "service-account-b")
	svcB := createService("ignored-ns", "svc-b", annotations, []int32{8080}, selectorAppB, "10.10.1.16")
	podB := createPod("ignored-ns", "app-b", saB, svcB.Spec.Selector, "10.10.2.1")

	svcC := createService("ignored-ns", "svc-c", annotations, []int32{9091}, selectorAppA, "10.10.1.17")
	svcD := createService("ignored-ns", "svc-d", annotations, []int32{9092}, selectorAppA, "10.10.1.18")

	apiMatch := createHTTPMatch("api", []string{"GET", "POST"}, "/api")
	metricMatch := createHTTPMatch("metric", []string{"GET"}, "/metric")
	rtGrp := createHTTPRouteGroup("ignored-ns", "http-rt-grp", []spec.HTTPMatch{apiMatch, metricMatch})

	tt := createTrafficTarget("ignored-ns", "tt", saB, "8080", []*corev1.ServiceAccount{saA}, rtGrp, []string{})
	ts := createTrafficSplit("ignored-ns", "ts", svcB, svcC, 80, svcD, 20)

	k8sClient := fake.NewSimpleClientset(saA, podA, saB, svcB, podB, svcC, svcD)
	smiAccessClient := accessfake.NewSimpleClientset(tt)
	smiSplitClient := splitfake.NewSimpleClientset(ts)
	smiSpecClient := specfake.NewSimpleClientset(rtGrp)

	builder, err := createBuilder(k8sClient, smiAccessClient, smiSpecClient, smiSplitClient)
	require.NoError(t, err)

	ignored := mk8s.NewIgnored()
	ignored.AddIgnoredNamespace("ignored-ns")

	got, err := builder.Build(ignored)
	require.NoError(t, err)

	want := &topology.Topology{
		Services:        make(map[topology.NameNamespace]*topology.Service),
		Pods:            make(map[topology.NameNamespace]*topology.Pod),
		TrafficTargets:  make(map[topology.NameNamespace]*access.TrafficTarget),
		TrafficSplits:   make(map[topology.NameNamespace]*split.TrafficSplit),
		HTTPRouteGroups: make(map[topology.NameNamespace]*spec.HTTPRouteGroup),
		TCPRoutes:       make(map[topology.NameNamespace]*spec.TCPRoute),
	}

	assert.Equal(t, want, got)
}

// TestTopologyBuilder_BuildWithTrafficTargetAndTrafficSplit makes sure the topology can be built with TrafficTarget
// and TrafficSplit.
func TestTopologyBuilder_BuildWithTrafficTargetAndTrafficSplit(t *testing.T) {
	selectorAppA := map[string]string{"app": "app-a"}
	selectorAppB := map[string]string{"app": "app-b"}
	annotations := map[string]string{
		"maesh.containo.us/traffic-type":      "http",
		"maesh.containo.us/ratelimit-average": "100",
		"maesh.containo.us/ratelimit-burst":   "200",
	}

	saA := createServiceAccount("my-ns", "service-account-a")
	podA := createPod("my-ns", "app-a", saA, selectorAppA, "10.10.1.1")

	saB := createServiceAccount("my-ns", "service-account-b")
	svcB := createService("my-ns", "svc-b", annotations, []int32{8080}, selectorAppB, "10.10.1.16")
	podB := createPod("my-ns", "app-b", saB, svcB.Spec.Selector, "10.10.2.1")

	svcC := createService("my-ns", "svc-c", annotations, []int32{9091}, selectorAppA, "10.10.1.17")
	svcD := createService("my-ns", "svc-d", annotations, []int32{9092}, selectorAppA, "10.10.1.18")

	apiMatch := createHTTPMatch("api", []string{"GET", "POST"}, "/api")
	metricMatch := createHTTPMatch("metric", []string{"GET"}, "/metric")
	rtGrp := createHTTPRouteGroup("my-ns", "http-rt-grp", []spec.HTTPMatch{apiMatch, metricMatch})

	ttMatch := []string{apiMatch.Name}
	tt := createTrafficTarget("my-ns", "tt", saB, "8080", []*corev1.ServiceAccount{saA}, rtGrp, ttMatch)
	ts := createTrafficSplit("my-ns", "ts", svcB, svcC, 80, svcD, 20)

	k8sClient := fake.NewSimpleClientset(saA, podA, saB, svcB, podB, svcC, svcD)
	smiAccessClient := accessfake.NewSimpleClientset(tt)
	smiSplitClient := splitfake.NewSimpleClientset(ts)
	smiSpecClient := specfake.NewSimpleClientset(rtGrp)

	builder, err := createBuilder(k8sClient, smiAccessClient, smiSpecClient, smiSplitClient)
	require.NoError(t, err)

	ignored := mk8s.NewIgnored()
	got, err := builder.Build(ignored)
	require.NoError(t, err)

	wantPodA := podToTopologyPod(podA)
	wantPodB1 := podToTopologyPod(podB)

	wantServiceB := serviceToTopologyService(svcB)
	wantServiceC := serviceToTopologyService(svcC)
	wantServiceD := serviceToTopologyService(svcD)

	wantServiceBTrafficTarget := &topology.ServiceTrafficTarget{
		Service: wantServiceB,
		Name:    tt.Name,
		Sources: []topology.ServiceTrafficTargetSource{
			{
				ServiceAccount: saA.Name,
				Namespace:      saA.Namespace,
				Pods:           []*topology.Pod{wantPodA},
			},
		},
		Destination: topology.ServiceTrafficTargetDestination{
			ServiceAccount: saB.Name,
			Namespace:      saB.Namespace,
			Ports:          []int32{8080},
			Pods:           []*topology.Pod{wantPodB1},
		},
		Specs: []topology.TrafficSpec{
			{
				HTTPRouteGroup: rtGrp,
				HTTPMatches:    []*spec.HTTPMatch{&apiMatch},
			},
		},
	}
	wantTrafficSplit := &topology.TrafficSplit{
		Name:      ts.Name,
		Namespace: ts.Namespace,
		Service:   wantServiceB,
		Backends: []topology.TrafficSplitBackend{
			{
				Weight:  80,
				Service: wantServiceC,
			},
			{
				Weight:  20,
				Service: wantServiceD,
			},
		},
	}

	wantPodA.Outgoing = []*topology.ServiceTrafficTarget{wantServiceBTrafficTarget}
	wantPodB1.Incoming = []*topology.ServiceTrafficTarget{wantServiceBTrafficTarget}
	wantServiceB.TrafficTargets = []*topology.ServiceTrafficTarget{wantServiceBTrafficTarget}
	wantServiceB.TrafficSplits = []*topology.TrafficSplit{wantTrafficSplit}

	want := &topology.Topology{
		Services: map[topology.NameNamespace]*topology.Service{
			nn(svcB.Name, svcB.Namespace): wantServiceB,
			nn(svcC.Name, svcC.Namespace): wantServiceC,
			nn(svcD.Name, svcD.Namespace): wantServiceD,
		},
		Pods: map[topology.NameNamespace]*topology.Pod{
			nn(podA.Name, podA.Namespace): wantPodA,
			nn(podB.Name, podB.Namespace): wantPodB1,
		},
		TrafficTargets: map[topology.NameNamespace]*access.TrafficTarget{
			nn(tt.Name, tt.Namespace): tt,
		},
		TrafficSplits: map[topology.NameNamespace]*split.TrafficSplit{
			nn(ts.Name, ts.Namespace): ts,
		},
		HTTPRouteGroups: map[topology.NameNamespace]*spec.HTTPRouteGroup{
			nn(rtGrp.Name, rtGrp.Namespace): rtGrp,
		},
		TCPRoutes: make(map[topology.NameNamespace]*spec.TCPRoute),
	}

	assert.Equal(t, want, got)
}

// TestTopologyBuilder_BuildWithTrafficTargetSpecEmptyMatch makes sure that when TrafficTarget.Spec.Matches is empty,
// the output matches list contains all the matches defined in the HTTPRouteGroup (as defined by the
//spec https://github.com/servicemeshinterface/smi-spec/blob/master/traffic-access-control.md#traffictarget-v1alpha1)
func TestTopologyBuilder_BuildWithTrafficTargetSpecEmptyMatch(t *testing.T) {
	selectorAppA := map[string]string{"app": "app-a"}
	selectorAppB := map[string]string{"app": "app-b"}
	annotations := map[string]string{}

	saA := createServiceAccount("my-ns", "service-account-a")
	podA := createPod("my-ns", "app-a", saA, selectorAppA, "10.10.1.1")

	saB := createServiceAccount("my-ns", "service-account-b")
	svcB := createService("my-ns", "svc-b", annotations, []int32{8080}, selectorAppB, "10.10.1.16")
	podB := createPod("my-ns", "app-b", saB, svcB.Spec.Selector, "10.10.2.1")

	apiMatch := createHTTPMatch("api", []string{"GET", "POST"}, "/api")
	metricMatch := createHTTPMatch("metric", []string{"GET"}, "/metric")
	rtGrp := createHTTPRouteGroup("my-ns", "http-rt-grp", []spec.HTTPMatch{apiMatch, metricMatch})

	tt := createTrafficTarget("my-ns", "tt", saB, "8080", []*corev1.ServiceAccount{saA}, rtGrp, []string{})

	k8sClient := fake.NewSimpleClientset(saA, podA, saB, svcB, podB)
	smiAccessClient := accessfake.NewSimpleClientset(tt)
	smiSplitClient := splitfake.NewSimpleClientset()
	smiSpecClient := specfake.NewSimpleClientset(rtGrp)

	builder, err := createBuilder(k8sClient, smiAccessClient, smiSpecClient, smiSplitClient)
	require.NoError(t, err)

	ignored := mk8s.NewIgnored()
	got, err := builder.Build(ignored)
	require.NoError(t, err)

	wantPodA := podToTopologyPod(podA)
	wantPodB1 := podToTopologyPod(podB)

	wantServiceB := serviceToTopologyService(svcB)

	wantServiceBTrafficTarget := &topology.ServiceTrafficTarget{
		Service: wantServiceB,
		Name:    tt.Name,
		Sources: []topology.ServiceTrafficTargetSource{
			{
				ServiceAccount: saA.Name,
				Namespace:      saA.Namespace,
				Pods:           []*topology.Pod{wantPodA},
			},
		},
		Destination: topology.ServiceTrafficTargetDestination{
			ServiceAccount: saB.Name,
			Namespace:      saB.Namespace,
			Ports:          []int32{8080},
			Pods:           []*topology.Pod{wantPodB1},
		},
		Specs: []topology.TrafficSpec{
			{
				HTTPRouteGroup: rtGrp,
				HTTPMatches:    []*spec.HTTPMatch{&apiMatch, &metricMatch},
			},
		},
	}

	wantPodA.Outgoing = []*topology.ServiceTrafficTarget{wantServiceBTrafficTarget}
	wantPodB1.Incoming = []*topology.ServiceTrafficTarget{wantServiceBTrafficTarget}
	wantServiceB.TrafficTargets = []*topology.ServiceTrafficTarget{wantServiceBTrafficTarget}

	want := &topology.Topology{
		Services: map[topology.NameNamespace]*topology.Service{
			nn(svcB.Name, svcB.Namespace): wantServiceB,
		},
		Pods: map[topology.NameNamespace]*topology.Pod{
			nn(podA.Name, podA.Namespace): wantPodA,
			nn(podB.Name, podB.Namespace): wantPodB1,
		},
		TrafficTargets: map[topology.NameNamespace]*access.TrafficTarget{
			nn(tt.Name, tt.Namespace): tt,
		},
		TrafficSplits: make(map[topology.NameNamespace]*split.TrafficSplit),
		HTTPRouteGroups: map[topology.NameNamespace]*spec.HTTPRouteGroup{
			nn(rtGrp.Name, rtGrp.Namespace): rtGrp,
		},
		TCPRoutes: make(map[topology.NameNamespace]*spec.TCPRoute),
	}

	assert.Equal(t, want, got)
}

// TestTopologyBuilder_BuildWithTrafficTargetEmptyDestinationPort makes sure that when a TrafficTarget.Destination.Port
// is empty, the output contains all the ports defined by the destination service (as defined by the
// spec https://github.com/servicemeshinterface/smi-spec/blob/master/traffic-access-control.md#traffictarget-v1alpha1)
func TestTopologyBuilder_BuildWithTrafficTargetEmptyDestinationPort(t *testing.T) {
	selectorAppA := map[string]string{"app": "app-a"}
	selectorAppB := map[string]string{"app": "app-b"}
	annotations := map[string]string{
		"maesh.containo.us/traffic-type":      "http",
		"maesh.containo.us/ratelimit-average": "100",
		"maesh.containo.us/ratelimit-burst":   "200",
	}

	saA := createServiceAccount("my-ns", "service-account-a")
	podA := createPod("my-ns", "app-a", saA, selectorAppA, "10.10.1.1")

	saB := createServiceAccount("my-ns", "service-account-b")
	svcB := createService("my-ns", "svc-b", annotations, []int32{8080, 9090}, selectorAppB, "10.10.1.16")
	podB := createPod("my-ns", "app-b", saB, svcB.Spec.Selector, "10.10.2.1")

	tt := createTrafficTarget("my-ns", "tt", saB, "", []*corev1.ServiceAccount{saA}, nil, []string{})

	k8sClient := fake.NewSimpleClientset(saA, podA, saB, svcB, podB)
	smiAccessClient := accessfake.NewSimpleClientset(tt)
	smiSplitClient := splitfake.NewSimpleClientset()
	smiSpecClient := specfake.NewSimpleClientset()

	builder, err := createBuilder(k8sClient, smiAccessClient, smiSpecClient, smiSplitClient)
	require.NoError(t, err)

	ignored := mk8s.NewIgnored()
	got, err := builder.Build(ignored)
	require.NoError(t, err)

	wantPodA := podToTopologyPod(podA)
	wantPodB1 := podToTopologyPod(podB)

	wantServiceB := serviceToTopologyService(svcB)

	wantServiceBTrafficTarget := &topology.ServiceTrafficTarget{
		Service: wantServiceB,
		Name:    tt.Name,
		Sources: []topology.ServiceTrafficTargetSource{
			{
				ServiceAccount: saA.Name,
				Namespace:      saA.Namespace,
				Pods:           []*topology.Pod{wantPodA},
			},
		},
		Destination: topology.ServiceTrafficTargetDestination{
			ServiceAccount: saB.Name,
			Namespace:      saB.Namespace,
			Ports:          []int32{8080, 9090},
			Pods:           []*topology.Pod{wantPodB1},
		},
	}

	wantPodA.Outgoing = []*topology.ServiceTrafficTarget{wantServiceBTrafficTarget}
	wantPodB1.Incoming = []*topology.ServiceTrafficTarget{wantServiceBTrafficTarget}
	wantServiceB.TrafficTargets = []*topology.ServiceTrafficTarget{wantServiceBTrafficTarget}

	want := &topology.Topology{
		Services: map[topology.NameNamespace]*topology.Service{
			nn(svcB.Name, svcB.Namespace): wantServiceB,
		},
		Pods: map[topology.NameNamespace]*topology.Pod{
			nn(podA.Name, podA.Namespace): wantPodA,
			nn(podB.Name, podB.Namespace): wantPodB1,
		},
		TrafficTargets: map[topology.NameNamespace]*access.TrafficTarget{
			nn(tt.Name, tt.Namespace): tt,
		},
		TrafficSplits:   make(map[topology.NameNamespace]*split.TrafficSplit),
		HTTPRouteGroups: make(map[topology.NameNamespace]*spec.HTTPRouteGroup),
		TCPRoutes:       make(map[topology.NameNamespace]*spec.TCPRoute),
	}

	assert.Equal(t, want, got)
}

// TestTopologyBuilder_BuildTrafficTargetMultipleSourcesAndDestinations makes sure we can build a topology with
// a TrafficTarget defines with multiple service accounts as sources.
func TestTopologyBuilder_BuildTrafficTargetMultipleSourcesAndDestinations(t *testing.T) {
	selectorAppA := map[string]string{"app": "app-a"}
	selectorAppB := map[string]string{"app": "app-b"}
	selectorAppC := map[string]string{"app": "app-c"}
	annotations := map[string]string{}

	saA := createServiceAccount("my-ns", "service-account-a")
	podA := createPod("my-ns", "app-a", saA, selectorAppA, "10.10.1.1")

	saB := createServiceAccount("my-ns", "service-account-b")
	podB := createPod("my-ns", "app-b", saB, selectorAppB, "10.10.2.1")

	saC := createServiceAccount("my-ns", "service-account-c")
	svcC := createService("my-ns", "svc-c", annotations, []int32{8080}, selectorAppC, "10.10.1.16")
	podC1 := createPod("my-ns", "app-c-1", saC, svcC.Spec.Selector, "10.10.3.1")
	podC2 := createPod("my-ns", "app-c-2", saC, svcC.Spec.Selector, "10.10.3.2")

	tt := createTrafficTarget("my-ns", "tt", saC, "8080", []*corev1.ServiceAccount{saA, saB}, nil, []string{})

	k8sClient := fake.NewSimpleClientset(saA, podA, saB, podB, saC, svcC, podC1, podC2)
	smiAccessClient := accessfake.NewSimpleClientset(tt)
	smiSplitClient := splitfake.NewSimpleClientset()
	smiSpecClient := specfake.NewSimpleClientset()

	builder, err := createBuilder(k8sClient, smiAccessClient, smiSpecClient, smiSplitClient)
	require.NoError(t, err)

	ignored := mk8s.NewIgnored()
	got, err := builder.Build(ignored)
	require.NoError(t, err)

	wantPodA := podToTopologyPod(podA)
	wantPodB := podToTopologyPod(podB)
	wantPodC1 := podToTopologyPod(podC1)
	wantPodC2 := podToTopologyPod(podC2)

	wantServiceC := serviceToTopologyService(svcC)

	wantServiceCTrafficTarget := &topology.ServiceTrafficTarget{
		Service: wantServiceC,
		Name:    tt.Name,
		Sources: []topology.ServiceTrafficTargetSource{
			{
				ServiceAccount: saA.Name,
				Namespace:      saA.Namespace,
				Pods:           []*topology.Pod{wantPodA},
			},
			{
				ServiceAccount: saB.Name,
				Namespace:      saB.Namespace,
				Pods:           []*topology.Pod{wantPodB},
			},
		},
		Destination: topology.ServiceTrafficTargetDestination{
			ServiceAccount: saC.Name,
			Namespace:      saC.Namespace,
			Ports:          []int32{8080},
			Pods:           []*topology.Pod{wantPodC1, wantPodC2},
		},
	}

	wantPodA.Outgoing = []*topology.ServiceTrafficTarget{wantServiceCTrafficTarget}
	wantPodB.Outgoing = []*topology.ServiceTrafficTarget{wantServiceCTrafficTarget}
	wantPodC1.Incoming = []*topology.ServiceTrafficTarget{wantServiceCTrafficTarget}
	wantPodC2.Incoming = []*topology.ServiceTrafficTarget{wantServiceCTrafficTarget}

	wantServiceC.TrafficTargets = []*topology.ServiceTrafficTarget{wantServiceCTrafficTarget}

	want := &topology.Topology{
		Services: map[topology.NameNamespace]*topology.Service{
			nn(svcC.Name, svcC.Namespace): wantServiceC,
		},
		Pods: map[topology.NameNamespace]*topology.Pod{
			nn(podA.Name, podA.Namespace):   wantPodA,
			nn(podB.Name, podB.Namespace):   wantPodB,
			nn(podC1.Name, podC1.Namespace): wantPodC1,
			nn(podC2.Name, podC2.Namespace): wantPodC2,
		},
		TrafficTargets: map[topology.NameNamespace]*access.TrafficTarget{
			nn(tt.Name, tt.Namespace): tt,
		},
		TrafficSplits:   make(map[topology.NameNamespace]*split.TrafficSplit),
		HTTPRouteGroups: make(map[topology.NameNamespace]*spec.HTTPRouteGroup),
		TCPRoutes:       make(map[topology.NameNamespace]*spec.TCPRoute),
	}

	assert.Equal(t, want, got)
}

// createBuilder initialize the different k8s factories, start them, initialize listers and create
// a new topology.Builder.
func createBuilder(
	k8sClient k8s.Interface,
	smiAccessClient accessclient.Interface,
	smiSpecClient specsclient.Interface,
	smiSplitClient splitclient.Interface) (*topology.Builder, error) {

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	k8sFactory := informers.NewSharedInformerFactoryWithOptions(k8sClient, mk8s.ResyncPeriod)

	svcLister := k8sFactory.Core().V1().Services().Lister()
	podLister := k8sFactory.Core().V1().Pods().Lister()
	epLister := k8sFactory.Core().V1().Endpoints().Lister()

	accessFactory := accessInformer.NewSharedInformerFactoryWithOptions(smiAccessClient, mk8s.ResyncPeriod)
	splitFactory := splitInformer.NewSharedInformerFactoryWithOptions(smiSplitClient, mk8s.ResyncPeriod)
	specsFactory := specsInformer.NewSharedInformerFactoryWithOptions(smiSpecClient, mk8s.ResyncPeriod)

	trafficTargetLister := accessFactory.Access().V1alpha1().TrafficTargets().Lister()
	trafficSplitLister := splitFactory.Split().V1alpha2().TrafficSplits().Lister()
	httpRouteGroupLister := specsFactory.Specs().V1alpha1().HTTPRouteGroups().Lister()
	tcpRouteLister := specsFactory.Specs().V1alpha1().TCPRoutes().Lister()

	k8sFactory.Start(ctx.Done())
	accessFactory.Start(ctx.Done())
	splitFactory.Start(ctx.Done())
	specsFactory.Start(ctx.Done())

	for t, ok := range k8sFactory.WaitForCacheSync(ctx.Done()) {
		if !ok {
			return nil, fmt.Errorf("timed out while waiting for cache sync: %s", t.String())
		}
	}
	for t, ok := range accessFactory.WaitForCacheSync(ctx.Done()) {
		if !ok {
			return nil, fmt.Errorf("timed out while waiting for cache sync: %s", t.String())
		}
	}
	for t, ok := range splitFactory.WaitForCacheSync(ctx.Done()) {
		if !ok {
			return nil, fmt.Errorf("timed out while waiting for cache sync: %s", t.String())
		}
	}
	for t, ok := range specsFactory.WaitForCacheSync(ctx.Done()) {
		if !ok {
			return nil, fmt.Errorf("timed out while waiting for cache sync: %s", t.String())
		}
	}

	return topology.NewBuilder(
		svcLister,
		epLister,
		podLister,
		trafficTargetLister,
		trafficSplitLister,
		httpRouteGroupLister,
		tcpRouteLister), nil
}

func podToTopologyPod(pod *corev1.Pod) *topology.Pod {
	return &topology.Pod{
		Name:           pod.Name,
		Namespace:      pod.Namespace,
		ServiceAccount: pod.Spec.ServiceAccountName,
		Owner:          pod.OwnerReferences,
		IP:             pod.Status.PodIP,
	}
}

func serviceToTopologyService(svc *corev1.Service) *topology.Service {
	return &topology.Service{
		Name:        svc.Name,
		Namespace:   svc.Namespace,
		Selector:    svc.Spec.Selector,
		Annotations: svc.Annotations,
		Ports:       svc.Spec.Ports,
		ClusterIP:   svc.Spec.ClusterIP,
	}
}

func nn(name, ns string) topology.NameNamespace {
	return topology.NameNamespace{
		Name:      name,
		Namespace: ns,
	}
}

func createTrafficSplit(ns, name string, svc *corev1.Service, backend1 *corev1.Service, weight1 int, backend2 *corev1.Service, weight2 int) *split.TrafficSplit {
	return &split.TrafficSplit{
		TypeMeta: metav1.TypeMeta{
			Kind:       "TrafficSplit",
			APIVersion: "split.smi-spec.io/v1alpha2",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
		},
		Spec: split.TrafficSplitSpec{
			Service: svc.Name,
			Backends: []split.TrafficSplitBackend{
				{
					Service: backend1.Name,
					Weight:  weight1,
				},
				{
					Service: backend2.Name,
					Weight:  weight2,
				},
			},
		},
	}
}

func createTrafficTarget(ns, name string, destSa *corev1.ServiceAccount, destPort string, srcsSa []*corev1.ServiceAccount, rtGrp *spec.HTTPRouteGroup, rtGrpMatches []string) *access.TrafficTarget {
	sources := make([]access.IdentityBindingSubject, len(srcsSa))
	for i, sa := range srcsSa {
		sources[i] = access.IdentityBindingSubject{
			Kind:      "ServiceAccount",
			Name:      sa.Name,
			Namespace: sa.Namespace,
		}
	}

	var specs []access.TrafficTargetSpec

	if rtGrp != nil {
		specs = append(specs, access.TrafficTargetSpec{
			Kind:    "HTTPRouteGroup",
			Name:    rtGrp.Name,
			Matches: rtGrpMatches,
		})
	}

	return &access.TrafficTarget{
		TypeMeta: metav1.TypeMeta{
			Kind:       "TrafficTarget",
			APIVersion: "access.smi-spec.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
		},
		Destination: access.IdentityBindingSubject{
			Kind:      "ServiceAccount",
			Name:      destSa.Name,
			Namespace: destSa.Namespace,
			Port:      destPort,
		},
		Sources: sources,
		Specs:   specs,
	}
}

func createHTTPRouteGroup(ns, name string, matches []spec.HTTPMatch) *spec.HTTPRouteGroup {
	return &spec.HTTPRouteGroup{
		TypeMeta: metav1.TypeMeta{
			Kind:       "HTTPRouteGroup",
			APIVersion: "specs.smi-spec.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
		},
		Matches: matches,
	}
}

func createHTTPMatch(name string, methods []string, pathPrefix string) spec.HTTPMatch {
	return spec.HTTPMatch{
		Name:      name,
		Methods:   methods,
		PathRegex: pathPrefix,
	}
}

func createService(
	ns, name string,
	annotations map[string]string,
	targetPorts []int32,
	selector map[string]string,
	clusterIP string) *corev1.Service {

	ports := make([]corev1.ServicePort, len(targetPorts))
	for i, p := range targetPorts {
		ports[i] = corev1.ServicePort{
			Name:       "",
			Protocol:   "TCP",
			Port:       p + 1,
			TargetPort: intstr.FromInt(int(p)),
		}
	}

	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   ns,
			Name:        name,
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Ports:     ports,
			Selector:  selector,
			ClusterIP: clusterIP,
			Type:      "ClusterIP",
		},
	}
}

func createEndpoints(svc *corev1.Service, pods []*corev1.Pod) *corev1.Endpoints {
	ports := make([]corev1.EndpointPort, len(svc.Spec.Ports))
	for i, port := range svc.Spec.Ports {
		ports[i] = corev1.EndpointPort{
			Name:     port.Name,
			Port:     port.TargetPort.IntVal,
			Protocol: port.Protocol,
		}
	}

	addresses := make([]corev1.EndpointAddress, len(pods))
	for i, pod := range pods {
		addresses[i] = corev1.EndpointAddress{
			IP: pod.Status.PodIP,
		}
	}

	return &corev1.Endpoints{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Endpoints",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      svc.Name,
			Namespace: svc.Namespace,
		},
		Subsets: []corev1.EndpointSubset{
			{
				Addresses: addresses,
				Ports:     ports,
			},
		},
	}
}

func createPod(ns, name string,
	sa *corev1.ServiceAccount,
	selector map[string]string,
	podIP string) *corev1.Pod {

	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    selector,
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: sa.Name,
		},
		Status: corev1.PodStatus{
			PodIP: podIP,
		},
	}
}

func createServiceAccount(ns, name string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ServiceAccount",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
		},
	}
}
