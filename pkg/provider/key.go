package provider

import (
	"fmt"

	"github.com/containous/maesh/pkg/topology"
)

func getMiddlewareKey(svc *topology.Service) string {
	return fmt.Sprintf("%s-%s", svc.Namespace, svc.Name)
}

func getServiceRouterKeyFromService(svc *topology.Service, port int32) string {
	return fmt.Sprintf("%s-%s-%d", svc.Namespace, svc.Name, port)
}

func getWhitelistMiddlewareDirectKey(tt *topology.ServiceTrafficTarget) string {
	return fmt.Sprintf("%s-%s-%s-whitelist-direct", tt.Service.Namespace, tt.Service.Name, tt.Name)
}

func getWhitelistMiddlewareIndirectKey(tt *topology.ServiceTrafficTarget) string {
	return fmt.Sprintf("%s-%s-%s-whitelist-indirect", tt.Service.Namespace, tt.Service.Name, tt.Name)
}

func getServiceRouterKeyFromTrafficTarget(tt *topology.ServiceTrafficTarget, port int32) string {
	return fmt.Sprintf("%s-%s-%s-%d-traffic-target", tt.Service.Namespace, tt.Service.Name, tt.Name, port)
}

func getRouterKeyFromTrafficTargetIndirect(tt *topology.ServiceTrafficTarget, port int32) string {
	return fmt.Sprintf("%s-%s-%s-%d-traffic-target-indirect", tt.Service.Namespace, tt.Service.Name, tt.Name, port)
}

func getServiceRouterKeyFromTrafficSplit(ts *topology.TrafficSplit, port int32) string {
	return fmt.Sprintf("%s-%s-%s-%d-traffic-split", ts.Service.Namespace, ts.Service.Name, ts.Name, port)
}

func getServiceRouterKeyFromTrafficSplitBackend(ts *topology.TrafficSplit, port int32, backend topology.TrafficSplitBackend) string {
	return fmt.Sprintf("%s-%s-%s-%d-%s-traffic-split-backend", ts.Service.Namespace, ts.Service.Name, ts.Name, port, backend.Service.Name)
}
