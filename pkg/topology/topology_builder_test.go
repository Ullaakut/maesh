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

func TestTopologyBuilder_Build(t *testing.T) {
	selectorAppA := map[string]string{"app": "app-a"}
	selectorAppB := map[string]string{"app": "app-b"}
	annotations := map[string]string{}

	saA := createServiceAccount("my-ns", "service-account-a")
	svcA := createService("my-ns", "svc-a", annotations, []int32{8080}, selectorAppA, "10.10.1.15")
	podA1 := createPod("my-ns", "app-a-1", saA, svcA.Spec.Selector, "10.10.1.1")

	saB := createServiceAccount("my-ns", "service-account-b")
	svcB := createService("my-ns", "svc-b", annotations, []int32{8080}, selectorAppB, "10.10.1.15")
	podB1 := createPod("my-ns", "app-b-1", saB, svcB.Spec.Selector, "10.10.2.1")

	tt := createTrafficTarget("my-ns", "tt", saB, "", []*corev1.ServiceAccount{saA})

	k8sClient := fake.NewSimpleClientset(svcA, podA1, svcB, podB1)
	smiAccessClient := accessfake.NewSimpleClientset(tt)
	smiSplitClient := splitfake.NewSimpleClientset()
	smiSpecClient := specfake.NewSimpleClientset()

	ctx := context.Background()
	builder, err := topology.NewTopologyBuilder(ctx, k8sClient, smiAccessClient, smiSpecClient, smiSplitClient)
	require.NoError(t, err)

	ignored := k8s.NewIgnored()
	got, err := builder.Build(ignored)
	require.NoError(t, err)

	wantPodA1 := &topology.Pod{
		Name:           podA1.Name,
		Namespace:      podA1.Namespace,
		ServiceAccount: podA1.Spec.ServiceAccountName,
		Owner:          podA1.OwnerReferences,
		IP:             podA1.Status.PodIP,
	}

	wantPodB1 := &topology.Pod{
		Name:           podB1.Name,
		Namespace:      podB1.Namespace,
		ServiceAccount: podB1.Spec.ServiceAccountName,
		Owner:          podB1.OwnerReferences,
		IP:             podB1.Status.PodIP,
	}

	wantServiceB := &topology.Service{
		Name:        svcB.Name,
		Namespace:   svcB.Namespace,
		Selector:    svcB.Spec.Selector,
		Annotations: svcB.Annotations,
		Ports:       svcB.Spec.Ports,
		ClusterIP:   svcB.Spec.ClusterIP,
	}

	wantServiceA := &topology.Service{
		Name:        svcA.Name,
		Namespace:   svcA.Namespace,
		Selector:    svcA.Spec.Selector,
		Annotations: svcA.Annotations,
		Ports:       svcA.Spec.Ports,
		ClusterIP:   svcA.Spec.ClusterIP,
	}

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
	}
	wantPodB1.Incoming = []*topology.ServiceTrafficTarget{wantServiceBTrafficTarget}
	wantPodA1.Outgoing = []*topology.ServiceTrafficTarget{wantServiceBTrafficTarget}
	wantServiceB.TrafficTargets = []*topology.ServiceTrafficTarget{wantServiceBTrafficTarget}

	want := &topology.Topology{
		Services: map[topology.NameNamespace]*topology.Service{
			nn(svcB.Name, svcB.Namespace): wantServiceB,
			nn(svcA.Name, svcA.Namespace): wantServiceA,
		},
		Pods: map[topology.NameNamespace]*topology.Pod{
			nn(podA1.Name, podA1.Namespace): wantPodA1,
			nn(podB1.Name, podB1.Namespace): wantPodB1,
		},
		TrafficTargets: map[topology.NameNamespace]*access.TrafficTarget{
			nn(tt.Name, tt.Namespace): tt,
		},
		TrafficSpecs:    make(map[topology.NameNamespace]*topology.TrafficSpec),
		TrafficSplits:   make(map[topology.NameNamespace]*split.TrafficSplit),
		HTTPRouteGroups: make(map[topology.NameNamespace]*spec.HTTPRouteGroup),
		TCPRoutes:       make(map[topology.NameNamespace]*spec.TCPRoute),
	}

	assert.Equal(t, want, got)
}

func nn(name, ns string) topology.NameNamespace {
	return topology.NameNamespace{
		Name:      name,
		Namespace: ns,
	}
}

func createTrafficTarget(ns, name string, destSa *corev1.ServiceAccount, destPort string, srcsSa []*corev1.ServiceAccount) *access.TrafficTarget {
	sources := make([]access.IdentityBindingSubject, len(srcsSa))
	for i, sa := range srcsSa {
		sources[i] = access.IdentityBindingSubject{
			Kind:      "ServiceAccount",
			Name:      sa.Name,
			Namespace: sa.Namespace,
		}
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
		Specs:   nil,
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
