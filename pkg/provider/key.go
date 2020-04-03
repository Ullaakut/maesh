package provider

import (
	"fmt"

	"github.com/containous/maesh/pkg/topology"
)

const (
	blockAllMiddlewareKey = "block-all-middleware"
	blockAllServiceKey    = "block-all-service"
)

func getMiddlewareKey(svc *topology.Service) string {
	return fmt.Sprintf("%s-%s", svc.Namespace, svc.Name)
}

func getServiceRouterKeyFromService(svc *topology.Service, port int32) string {
	return fmt.Sprintf("%s-%s-%d", svc.Namespace, svc.Name, port)
}

func getWhitelistMiddlewareDirectKeyFromTrafficTarget(tt *topology.ServiceTrafficTarget) string {
	return fmt.Sprintf("%s-%s-%s-whitelist-direct-traffic-target", tt.Service.Namespace, tt.Service.Name, tt.Name)
}

func getWhitelistMiddlewareIndirectKeyFromTrafficTarget(tt *topology.ServiceTrafficTarget) string {
	return fmt.Sprintf("%s-%s-%s-whitelist-indirect-traffic-target", tt.Service.Namespace, tt.Service.Name, tt.Name)
}

func getWhitelistMiddlewareDirectKeyFromTrafficSplit(ts *topology.TrafficSplit) string {
	return fmt.Sprintf("%s-%s-%s-whitelist-direct-traffic-split", ts.Service.Namespace, ts.Service.Name, ts.Name)
}

func getWhitelistMiddlewareIndirectKeyTrafficSplit(ts *topology.TrafficSplit) string {
	return fmt.Sprintf("%s-%s-%s-whitelist-indirect-traffic-split", ts.Service.Namespace, ts.Service.Name, ts.Name)
}

func getServiceRouterKeyFromTrafficTarget(tt *topology.ServiceTrafficTarget, port int32) string {
	return fmt.Sprintf("%s-%s-%s-%d-traffic-target", tt.Service.Namespace, tt.Service.Name, tt.Name, port)
}

func getRouterKeyFromTrafficTargetIndirect(tt *topology.ServiceTrafficTarget, port int32) string {
	return fmt.Sprintf("%s-%s-%s-%d-traffic-target-indirect", tt.Service.Namespace, tt.Service.Name, tt.Name, port)
}

func getRouterKeyFromTrafficSplitIndirect(ts *topology.TrafficSplit, port int32) string {
	return fmt.Sprintf("%s-%s-%s-%d-traffic-split-indirect", ts.Service.Namespace, ts.Service.Name, ts.Name, port)
}

func getServiceRouterKeyFromTrafficSplit(ts *topology.TrafficSplit, port int32) string {
	return fmt.Sprintf("%s-%s-%s-%d-traffic-split", ts.Service.Namespace, ts.Service.Name, ts.Name, port)
}

func getServiceRouterKeyFromTrafficSplitBackend(ts *topology.TrafficSplit, port int32, backend topology.TrafficSplitBackend) string {
	return fmt.Sprintf("%s-%s-%s-%d-%s-traffic-split-backend", ts.Service.Namespace, ts.Service.Name, ts.Name, port, backend.Service.Name)
}
