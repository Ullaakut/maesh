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
