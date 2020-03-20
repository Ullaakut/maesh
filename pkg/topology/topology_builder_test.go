package topology_test

import (
	"context"
	"testing"

	"github.com/containous/maesh/pkg/k8s"
	"github.com/containous/maesh/pkg/topology"
	access "github.com/deislabs/smi-sdk-go/pkg/apis/access/v1alpha1"
	spec "github.com/deislabs/smi-sdk-go/pkg/apis/specs/v1alpha1"
	split "github.com/deislabs/smi-sdk-go/pkg/apis/split/v1alpha2"
	accessfake "github.com/deislabs/smi-sdk-go/pkg/gen/client/access/clientset/versioned/fake"
	specfake "github.com/deislabs/smi-sdk-go/pkg/gen/client/specs/clientset/versioned/fake"
	splitfake "github.com/deislabs/smi-sdk-go/pkg/gen/client/split/clientset/versioned/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

// TODO:
// - test port "" becomes all service ports
// - test Ignore namespaces
// - test multiple sources (service-account)

func TestTopologyBuilder_Build(t *testing.T) {
	selectorAppA := map[string]string{"app": "app-a"}
	selectorAppB := map[string]string{"app": "app-b"}
	annotations := map[string]string{
		"maesh.containo.us/traffic-type":      "http",
		"maesh.containo.us/ratelimit-average": "100",
		"maesh.containo.us/ratelimit-burst":   "200",
	}

	saA := createServiceAccount("my-ns", "service-account-a")
	svcA := createService("my-ns", "svc-a", annotations, []int32{9090}, selectorAppA, "10.10.1.15")
	podA1 := createPod("my-ns", "app-a-1", saA, svcA.Spec.Selector, "10.10.1.1")

	saB := createServiceAccount("my-ns", "service-account-b")
	svcB := createService("my-ns", "svc-b", annotations, []int32{8080}, selectorAppB, "10.10.1.16")
	podB1 := createPod("my-ns", "app-b-1", saB, svcB.Spec.Selector, "10.10.2.1")

	svcC := createService("my-ns", "svc-c", annotations, []int32{9091}, selectorAppA, "10.10.1.17")
	svcD := createService("my-ns", "svc-d", annotations, []int32{9092}, selectorAppA, "10.10.1.18")

	apiMatch := createHTTPMatch("api", []string{"GET", "POST"}, "/api")
	metricMatch := createHTTPMatch("metric", []string{"GET"}, "/metric")
	rtGrp := createHTTPRouteGroup("my-ns", "http-rt-grp", []spec.HTTPMatch{apiMatch, metricMatch})

	tt := createTrafficTarget("my-ns", "tt", saB, "", []*corev1.ServiceAccount{saA}, rtGrp, []string{})
	ts := createTrafficSplit("my-ns", "ts", svcB, svcC, 80, svcD, 20)

	k8sClient := fake.NewSimpleClientset(saA, svcA, podA1, saB, svcB, podB1, svcC, svcD)
	smiAccessClient := accessfake.NewSimpleClientset(tt)
	smiSplitClient := splitfake.NewSimpleClientset(ts)
	smiSpecClient := specfake.NewSimpleClientset(rtGrp)

	ctx := context.Background()
	builder, err := topology.NewTopologyBuilder(ctx, k8sClient, smiAccessClient, smiSpecClient, smiSplitClient)
	require.NoError(t, err)

	ignored := k8s.NewIgnored()
	got, err := builder.Build(ignored)
	require.NoError(t, err)

	wantPodA1 := podToTopologyPod(podA1)
	wantPodB1 := podToTopologyPod(podB1)

	wantServiceA := serviceToTopologyService(svcA)
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
				Pods:           []*topology.Pod{wantPodA1},
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

	wantPodB1.Incoming = []*topology.ServiceTrafficTarget{wantServiceBTrafficTarget}
	wantPodA1.Outgoing = []*topology.ServiceTrafficTarget{wantServiceBTrafficTarget}
	wantServiceB.TrafficTargets = []*topology.ServiceTrafficTarget{wantServiceBTrafficTarget}
	wantServiceB.TrafficSplits = []*topology.TrafficSplit{wantTrafficSplit}

	want := &topology.Topology{
		Services: map[topology.NameNamespace]*topology.Service{
			nn(svcA.Name, svcA.Namespace): wantServiceA,
			nn(svcB.Name, svcB.Namespace): wantServiceB,
			nn(svcC.Name, svcC.Namespace): wantServiceC,
			nn(svcD.Name, svcD.Namespace): wantServiceD,
		},
		Pods: map[topology.NameNamespace]*topology.Pod{
			nn(podA1.Name, podA1.Namespace): wantPodA1,
			nn(podB1.Name, podB1.Namespace): wantPodB1,
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
