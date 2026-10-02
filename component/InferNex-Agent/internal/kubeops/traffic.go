package kubeops

import (
	"context"
	"fmt"
	"net"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TrafficReader is optional: platform readers can adopt this native capability
// without implementing an InferNex observer or enabling a mutation channel.
type TrafficReader interface {
	InspectServiceBackends(context.Context, ServiceBackendRequest) (ServiceBackendReport, error)
}

type ServiceBackendRequest struct {
	Namespace string `json:"namespace" jsonschema:"Explicit namespace of the Kubernetes Service"`
	Name      string `json:"name" jsonschema:"Name of the Kubernetes Service"`
}

type TrafficFinding struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ServicePortBackends struct {
	Name          string          `json:"name,omitempty"`
	Port          int32           `json:"port"`
	Protocol      corev1.Protocol `json:"protocol"`
	ReadyBackends int             `json:"readyBackends"`
	ReadyNodes    int             `json:"readyNodes"`
}

type ServiceBackendReport struct {
	Namespace                    string                 `json:"namespace"`
	Name                         string                 `json:"name"`
	UID                          types.UID              `json:"uid"`
	ResourceVersion              string                 `json:"resourceVersion"`
	EndpointSliceResourceVersion string                 `json:"endpointSliceResourceVersion,omitempty"`
	ServiceType                  corev1.ServiceType     `json:"serviceType"`
	Headless                     bool                   `json:"headless"`
	SessionAffinity              corev1.ServiceAffinity `json:"sessionAffinity"`
	Ports                        []ServicePortBackends  `json:"ports"`
	Findings                     []TrafficFinding       `json:"findings"`
	Truncated                    bool                   `json:"truncated"`
	TrafficVerified              bool                   `json:"trafficVerified"`
}

// InspectServiceBackends checks API configuration, not observed request traffic.
// EndpointSlice.ready nil follows Kubernetes' "unknown means ready" convention.
// Terminating backends are excluded even when serving is true: draining fallback
// behavior is implementation-dependent and cannot establish healthy redundancy.
func (r *KubernetesReader) InspectServiceBackends(ctx context.Context, request ServiceBackendRequest) (ServiceBackendReport, error) {
	if len(validation.IsDNS1123Label(request.Namespace)) != 0 || len(validation.IsDNS1123Label(request.Name)) != 0 {
		return ServiceBackendReport{}, fmt.Errorf("explicit valid namespace and Service name are required")
	}
	key := types.NamespacedName{Namespace: request.Namespace, Name: request.Name}
	service := &corev1.Service{}
	if err := r.client.Get(ctx, key, service); err != nil {
		return ServiceBackendReport{}, fmt.Errorf("read Service: %w", err)
	}
	if len(service.Spec.Ports) > 32 {
		return ServiceBackendReport{}, fmt.Errorf("Service exceeds the 32-port inspection budget")
	}
	result := ServiceBackendReport{Namespace: service.Namespace, Name: service.Name, UID: service.UID, ResourceVersion: service.ResourceVersion,
		ServiceType: service.Spec.Type, Headless: service.Spec.ClusterIP == corev1.ClusterIPNone, SessionAffinity: service.Spec.SessionAffinity,
		Ports: []ServicePortBackends{}, Findings: []TrafficFinding{}}
	add := func(code, message string) {
		result.Findings = append(result.Findings, TrafficFinding{Code: code, Message: message})
	}
	add("traffic-not-measured", "Endpoint availability is not request-distribution evidence. Verify per-backend requests, active streams, errors and latency through the actual client entrypoint; Service connection balancing does not guarantee HTTP/2 or keep-alive request balancing.")
	if service.DeletionTimestamp != nil {
		add("service-terminating", "The Service is being deleted.")
	}
	if result.Headless {
		add("headless-service", "DNS publishes backend addresses; client or L7 routing determines distribution.")
	}
	if service.Spec.Type == corev1.ServiceTypeExternalName {
		add("external-name", "This Service is a DNS alias. EndpointSlice counts cannot describe the external destination's balancing.")
		return result, nil
	}
	if len(service.Spec.Selector) == 0 {
		add("manual-endpoints", "No Pod selector is configured; endpoint population is managed externally.")
	}
	if service.Spec.SessionAffinity == corev1.ServiceAffinityClientIP {
		add("client-ip-affinity", "ClientIP affinity can pin requests from one client or shared NAT address to one backend.")
	}
	if service.Spec.InternalTrafficPolicy != nil && *service.Spec.InternalTrafficPolicy == corev1.ServiceInternalTrafficPolicyLocal {
		add("internal-local-policy", "Internal clients only use node-local endpoints; cluster-wide counts overstate their eligible pool.")
	}
	if service.Spec.ExternalTrafficPolicy == corev1.ServiceExternalTrafficPolicyLocal {
		add("external-local-policy", "External traffic is restricted to endpoints local to its receiving node.")
	}
	if service.Spec.TrafficDistribution != nil {
		add("traffic-distribution-preference", "Topology-based traffic preference is set; ready backends need not receive equal traffic.")
	}
	if service.Spec.PublishNotReadyAddresses {
		add("publish-not-ready", "Published endpoint readiness is not proof of application readiness when publishNotReadyAddresses is enabled.")
	}
	if len(service.Spec.Ports) == 0 {
		add("no-service-ports", "No Service ports are configured.")
	}
	var slices discoveryv1.EndpointSliceList
	if err := r.client.List(ctx, &slices, client.InNamespace(request.Namespace), client.MatchingLabels{discoveryv1.LabelServiceName: request.Name}, client.Limit(100)); err != nil {
		return ServiceBackendReport{}, fmt.Errorf("read EndpointSlices (discovery.k8s.io list permission required): %w", err)
	}
	result.EndpointSliceResourceVersion = slices.ResourceVersion
	result.Truncated = slices.Continue != ""
	backends := make([]map[string]bool, len(service.Spec.Ports))
	nodes := make([]map[string]bool, len(service.Spec.Ports))
	for i := range backends {
		backends[i], nodes[i] = map[string]bool{}, map[string]bool{}
	}
	stale, hints, unidentified, unknownOwner := false, false, false, false
	seenEndpoints := 0
	for _, slice := range slices.Items {
		owned, foreign := false, false
		for _, owner := range slice.OwnerReferences {
			if owner.Kind == "Service" && owner.APIVersion == "v1" {
				if owner.Name == service.Name && owner.UID == service.UID {
					owned = true
				} else {
					foreign = true
				}
			}
		}
		if foreign {
			stale = true
			continue
		}
		if !owned {
			unknownOwner = true
		}
		if slice.AddressType != discoveryv1.AddressTypeIPv4 && slice.AddressType != discoveryv1.AddressTypeIPv6 {
			unidentified = true
			continue
		}
		for _, endpoint := range slice.Endpoints {
			seenEndpoints++
			if seenEndpoints > 10000 {
				result.Truncated = true
				break
			}
			if endpoint.Hints != nil {
				hints = true
			}
			if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready || endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating {
				continue
			}
			if len(endpoint.Addresses) == 0 {
				continue
			}
			id := ""
			if endpoint.TargetRef != nil && endpoint.TargetRef.Kind == "Pod" && endpoint.TargetRef.UID != "" {
				id = "pod:" + string(endpoint.TargetRef.UID)
			} else {
				unidentified = true
			}
			for _, address := range endpoint.Addresses {
				ip := net.ParseIP(address)
				if ip == nil || (slice.AddressType == discoveryv1.AddressTypeIPv4) != (ip.To4() != nil) {
					continue
				}
				identity := id
				if identity == "" {
					identity = "address:" + ip.String()
				}
				for i, port := range service.Spec.Ports {
					protocol := port.Protocol
					if protocol == "" {
						protocol = corev1.ProtocolTCP
					}
					for _, backendPort := range slice.Ports {
						name, bp := "", corev1.ProtocolTCP
						if backendPort.Name != nil {
							name = *backendPort.Name
						}
						if backendPort.Protocol != nil {
							bp = *backendPort.Protocol
						}
						if name != port.Name || bp != protocol || backendPort.Port == nil || *backendPort.Port < 1 {
							continue
						}
						backends[i][identity] = true
						if endpoint.NodeName != nil {
							nodes[i][*endpoint.NodeName] = true
						}
					}
				}
			}
		}
		if result.Truncated && seenEndpoints > 10000 {
			break
		}
	}
	if stale {
		add("stale-service-identity", "EndpointSlices owned by a different Service UID were excluded.")
	}
	if unknownOwner {
		add("unverified-slice-ownership", "Some slices have no matching Service owner; custom-controller data must be verified independently.")
	}
	if hints {
		add("topology-hints", "Endpoint topology hints can restrict the backend pool used by each client.")
	}
	if unidentified {
		add("incomplete-backend-identity", "Some endpoints lack a Pod UID or a supported IP address type; address counts may not equal unique serving instances.")
	}
	if result.Truncated {
		add("incomplete-snapshot", "Inspection budget or pagination limit reached; counts are lower bounds, not an availability verdict.")
	}
	for i, port := range service.Spec.Ports {
		protocol := port.Protocol
		if protocol == "" {
			protocol = corev1.ProtocolTCP
		}
		result.Ports = append(result.Ports, ServicePortBackends{Name: port.Name, Port: port.Port, Protocol: protocol, ReadyBackends: len(backends[i]), ReadyNodes: len(nodes[i])})
		if !result.Truncated && len(backends[i]) < 2 {
			add("insufficient-backends", fmt.Sprintf("Service port %d/%s has %d ready non-terminating backend identities; this does not provide a redundant backend pool.", port.Port, protocol, len(backends[i])))
		}
	}
	latest := &corev1.Service{}
	if err := r.client.Get(ctx, key, latest); err != nil {
		return ServiceBackendReport{}, fmt.Errorf("recheck Service: %w", err)
	}
	if latest.UID != service.UID || latest.ResourceVersion != service.ResourceVersion {
		return ServiceBackendReport{}, fmt.Errorf("Service changed during inspection; retry with a fresh snapshot")
	}
	return result, nil
}
