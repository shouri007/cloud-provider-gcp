package ipam

import (
	"context"
	"fmt"
	"strings"

	compute "google.golang.org/api/compute/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	networkv1 "k8s.io/cloud-provider-gcp/crd/apis/network/v1"
	"k8s.io/klog/v2"
)

// PerformMultiNetworkCIDRAllocation allots pod CIDRs for all the networks that a node is connected to. It handles IPv6 only for default-network for now.
func (ca *cloudCIDRAllocator) PerformMultiNetworkCIDRAllocation(node *v1.Node, interfaces []*compute.NetworkInterface) (defaultNwCIDRs []string, northInterfaces networkv1.NorthInterfacesAnnotation, additionalNodeNetworks networkv1.MultiNetworkAnnotation, err error) {
	k8sNetworksList, err := ca.networkClient.NetworkingV1().Networks().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("error fetching networks: %v", err)
	}
	k8sNetworks := make([]networkv1.Network, 0)
	// ignore networks that are under deletion.
	for _, network := range k8sNetworksList.Items {
		if network.ObjectMeta.DeletionTimestamp.IsZero() {
			k8sNetworks = append(k8sNetworks, network)
		}
	}
	gkeNwParamsClient := ca.networkClient.NetworkingV1().GKENetworkParams()
	// Fetch the GKENetworkParams for every k8s-network object.
	// Match the fetched GKENetworkParams object with the interfaces on the node
	// to build the per-network north-interface and node-network annotations useful for IPAM.
	for _, inf := range interfaces {
		rangeNameAliasIPMap := map[string]*compute.AliasIpRange{}
		for _, ipRange := range inf.AliasIpRanges {
			rangeNameAliasIPMap[ipRange.SubnetworkRangeName] = ipRange
		}
		for _, network := range k8sNetworks {
			klog.V(4).Infof("allotting pod cidrs for network %s", network.Name)
			gnp, err := gkeNwParamsClient.Get(context.TODO(), network.Spec.ParametersRef.Name, metav1.GetOptions{})
			if err != nil {
				return nil, nil, nil, err
			}
			if resourceName(inf.Network) != resourceName(gnp.Spec.VPC) || resourceName(inf.Subnetwork) != resourceName(gnp.Spec.VPCSubnet) {
				continue
			}
			klog.V(2).Infof("interface %s matched, proceeding to find a secondary range", inf.Name)
			// TODO: Handle IPv6 in future.
			secondaryRangeNames := gnp.Spec.PodIPv4Ranges.RangeNames
			if len(secondaryRangeNames) == 0 && network.Name != networkv1.DefaultNetworkName {
				northInterfaces = append(northInterfaces, networkv1.NorthInterface{Network: network.Name, IpAddress: inf.NetworkIP})
			}
			// Each secondary range in a subnet corresponds to a pod-network. AliasIPRanges list on a node interface consists of IP ranges that belong to multiple secondary ranges (pod-networks).
			// Match the secondary range names of interface and GKENetworkParams and set the right IpCidrRange for current network.
			for _, secondaryRangeName := range secondaryRangeNames {
				ipRange, ok := rangeNameAliasIPMap[secondaryRangeName]
				if !ok {
					continue
				}
				klog.V(2).Infof("found an allocatable secondary range for the interface on network")
				if network.Name == networkv1.DefaultNetworkName {
					defaultNwCIDRs = append(defaultNwCIDRs, ipRange.IpCidrRange)
					defaultNwCIDRs = ca.cloud.AccommodateIPV6Addresses(defaultNwCIDRs, inf, node.Spec.ProviderID)
				} else {
					northInterfaces = append(northInterfaces, networkv1.NorthInterface{Network: network.Name, IpAddress: inf.NetworkIP})
					additionalNodeNetworks = append(additionalNodeNetworks, networkv1.NodeNetwork{Name: network.Name, Scope: "host-local", Cidrs: []string{ipRange.IpCidrRange}})
				}
				break
			}
		}
	}
	return defaultNwCIDRs, northInterfaces, additionalNodeNetworks, nil
}

func resourceName(name string) string {
	parts := strings.Split(name, "/")
	return parts[len(parts)-1]
}
