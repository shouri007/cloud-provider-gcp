package ipam

import (
	"testing"

	"github.com/stretchr/testify/assert"
	compute "google.golang.org/api/compute/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	networkv1 "k8s.io/cloud-provider-gcp/crd/apis/network/v1"
	fake "k8s.io/cloud-provider-gcp/crd/client/network/clientset/versioned/fake"
)

const (
	Group                = "networking.gke.io"
	GKENetworkParamsKind = "GKENetworkParams"
	// Default Network
	DefaultGKENetworkParamsName = "DefaultGKENetworkParams"
	DefaultVPCName              = "projects/testProject/global/networks/default"
	DefaultVPCSubnetName        = "projects/testProject/regions/us-central1/subnetworks/default"
	DefaultSecondaryRangeA      = "RangeA"
	DefaultSecondaryRangeB      = "RangeB"
	// Red Network
	RedNetworkName          = "Red-Network"
	RedGKENetworkParamsName = "RedGKENetworkParams"
	RedVPCName              = "projects/testProject/global/networks/red"
	RedVPCSubnetName        = "projects/testProject/regions/us-central1/subnetworks/red"
	RedSecondaryRangeA      = "RedRangeA"
	RedSecondaryRangeB      = "RedRangeB"
	// Blue Network
	BlueNetworkName          = "Blue-Network"
	BlueGKENetworkParamsName = "BlueGKENetworkParams"
	BlueVPCName              = "projects/testProject/global/networks/blue"
	BlueVPCSubnetName        = "projects/testProject/regions/us-central1/subnetworks/blue"
	BlueSecondaryRangeA      = "BlueRangeA"
	BlueSecondaryRangeB      = "BlueRangeB"
)

func network(name, gkeNetworkParamsName string) *networkv1.Network {
	return &networkv1.Network{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: networkv1.NetworkSpec{
			Type: "L3",
			ParametersRef: &networkv1.NetworkParametersReference{
				Group: Group,
				Kind:  GKENetworkParamsKind,
				Name:  gkeNetworkParamsName,
			},
		},
	}
}

func gkeNetworkParams(name, vpc, subnet string, secRangeNames []string) *networkv1.GKENetworkParams {
	return &networkv1.GKENetworkParams{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: networkv1.GKENetworkParamsSpec{
			VPC:       vpc,
			VPCSubnet: subnet,
			PodIPv4Ranges: &networkv1.SecondaryRanges{
				RangeNames: secRangeNames,
			},
		},
	}
}

func interfaces(network, subnetwork, networkIP string, aliasIPRanges []*compute.AliasIpRange) *compute.NetworkInterface {
	return &compute.NetworkInterface{
		AliasIpRanges: aliasIPRanges,
		Network:       network,
		Subnetwork:    subnetwork,
		NetworkIP:     networkIP,
	}
}

func TestPerformMultiNetworkCIDRAllocation(t *testing.T) {
	node := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node0"},
		Spec:       v1.NodeSpec{ProviderID: ""},
	}
	testCases := []struct {
		desc                       string
		networks                   []*networkv1.Network
		gkeNwParams                []*networkv1.GKENetworkParams
		interfaces                 []*compute.NetworkInterface
		wantDefaultNwPodCIDRs      []string
		wantNorthInterfaces        networkv1.NorthInterfacesAnnotation
		wantAdditionalNodeNetworks networkv1.MultiNetworkAnnotation
	}{
		{
			desc: "default network only - should return default network cidrs and no multi-network annotations",
			networks: []*networkv1.Network{
				network(networkv1.DefaultNetworkName, DefaultGKENetworkParamsName),
			},
			gkeNwParams: []*networkv1.GKENetworkParams{
				gkeNetworkParams(DefaultGKENetworkParamsName, DefaultVPCName, DefaultVPCSubnetName, []string{DefaultSecondaryRangeA, DefaultSecondaryRangeB}),
			},
			interfaces: []*compute.NetworkInterface{
				interfaces(DefaultVPCName, DefaultVPCSubnetName, "80.1.172.1", []*compute.AliasIpRange{
					{IpCidrRange: "10.11.1.0/24", SubnetworkRangeName: DefaultSecondaryRangeA},
				}),
			},
			wantDefaultNwPodCIDRs: []string{"10.11.1.0/24"},
		},
		{
			desc: "one additional network along with default network",
			networks: []*networkv1.Network{
				network(networkv1.DefaultNetworkName, DefaultGKENetworkParamsName),
				network(RedNetworkName, RedGKENetworkParamsName),
			},
			gkeNwParams: []*networkv1.GKENetworkParams{
				gkeNetworkParams(DefaultGKENetworkParamsName, DefaultVPCName, DefaultVPCSubnetName, []string{DefaultSecondaryRangeA, DefaultSecondaryRangeB}),
				gkeNetworkParams(RedGKENetworkParamsName, RedVPCName, RedVPCSubnetName, []string{RedSecondaryRangeA, RedSecondaryRangeB}),
			},
			interfaces: []*compute.NetworkInterface{
				interfaces(DefaultVPCName, DefaultVPCSubnetName, "80.1.172.1", []*compute.AliasIpRange{
					{IpCidrRange: "10.11.1.0/24", SubnetworkRangeName: DefaultSecondaryRangeA},
				}),
				interfaces(RedVPCName, RedVPCSubnetName, "10.1.1.1", []*compute.AliasIpRange{
					{IpCidrRange: "172.11.1.0/24", SubnetworkRangeName: RedSecondaryRangeA},
				}),
			},
			wantDefaultNwPodCIDRs: []string{"10.11.1.0/24"},
			wantNorthInterfaces: networkv1.NorthInterfacesAnnotation{
				{
					Network:   RedNetworkName,
					IpAddress: "10.1.1.1",
				},
			},
			wantAdditionalNodeNetworks: networkv1.MultiNetworkAnnotation{
				{
					Name:  RedNetworkName,
					Scope: "host-local",
					Cidrs: []string{"172.11.1.0/24"},
				},
			},
		},
		{
			desc: "no secondary ranges in GKENetworkParams",
			networks: []*networkv1.Network{
				network(networkv1.DefaultNetworkName, DefaultGKENetworkParamsName),
				network(RedNetworkName, RedGKENetworkParamsName),
				network(BlueNetworkName, BlueGKENetworkParamsName),
			},
			gkeNwParams: []*networkv1.GKENetworkParams{
				gkeNetworkParams(DefaultGKENetworkParamsName, DefaultVPCName, DefaultVPCSubnetName, []string{DefaultSecondaryRangeA, DefaultSecondaryRangeB}),
				gkeNetworkParams(RedGKENetworkParamsName, RedVPCName, RedVPCSubnetName, []string{RedSecondaryRangeA, RedSecondaryRangeB}),
				gkeNetworkParams(BlueGKENetworkParamsName, BlueVPCName, BlueVPCSubnetName, []string{}),
			},
			interfaces: []*compute.NetworkInterface{
				interfaces(DefaultVPCName, DefaultVPCSubnetName, "80.1.172.1", []*compute.AliasIpRange{
					{IpCidrRange: "10.11.1.0/24", SubnetworkRangeName: DefaultSecondaryRangeA},
				}),
				interfaces(RedVPCName, RedVPCSubnetName, "10.1.1.1", []*compute.AliasIpRange{
					{IpCidrRange: "172.11.1.0/24", SubnetworkRangeName: RedSecondaryRangeA},
				}),
				interfaces(BlueVPCName, BlueVPCSubnetName, "84.1.2.1", []*compute.AliasIpRange{
					{IpCidrRange: "20.28.1.0/24", SubnetworkRangeName: RedSecondaryRangeA},
				}),
			},
			wantDefaultNwPodCIDRs: []string{"10.11.1.0/24"},
			wantNorthInterfaces: networkv1.NorthInterfacesAnnotation{
				{
					Network:   RedNetworkName,
					IpAddress: "10.1.1.1",
				},
				{
					Network:   BlueNetworkName,
					IpAddress: "84.1.2.1",
				},
			},
			wantAdditionalNodeNetworks: networkv1.MultiNetworkAnnotation{
				{
					Name:  RedNetworkName,
					Scope: "host-local",
					Cidrs: []string{"172.11.1.0/24"},
				},
			},
		},
		{
			desc: "networks without matching interfaces should be ignored",
			networks: []*networkv1.Network{
				network(networkv1.DefaultNetworkName, DefaultGKENetworkParamsName),
				network(RedNetworkName, RedGKENetworkParamsName),
				network(BlueNetworkName, BlueGKENetworkParamsName),
			},
			gkeNwParams: []*networkv1.GKENetworkParams{
				gkeNetworkParams(DefaultGKENetworkParamsName, DefaultVPCName, DefaultVPCSubnetName, []string{DefaultSecondaryRangeA, DefaultSecondaryRangeB}),
				gkeNetworkParams(RedGKENetworkParamsName, RedVPCName, RedVPCSubnetName, []string{RedSecondaryRangeA, RedSecondaryRangeB}),
				gkeNetworkParams(BlueGKENetworkParamsName, BlueVPCName, BlueVPCSubnetName, []string{}),
			},
			interfaces: []*compute.NetworkInterface{
				interfaces(DefaultVPCName, DefaultVPCSubnetName, "80.1.172.1", []*compute.AliasIpRange{
					{IpCidrRange: "10.11.1.0/24", SubnetworkRangeName: DefaultSecondaryRangeA},
				}),
				interfaces(RedVPCName, RedVPCSubnetName, "10.1.1.1", []*compute.AliasIpRange{
					{IpCidrRange: "172.11.1.0/24", SubnetworkRangeName: RedSecondaryRangeA},
				}),
			},
			wantDefaultNwPodCIDRs: []string{"10.11.1.0/24"},
			wantNorthInterfaces: networkv1.NorthInterfacesAnnotation{
				{
					Network:   RedNetworkName,
					IpAddress: "10.1.1.1",
				},
			},
			wantAdditionalNodeNetworks: networkv1.MultiNetworkAnnotation{
				{
					Name:  RedNetworkName,
					Scope: "host-local",
					Cidrs: []string{"172.11.1.0/24"},
				},
			},
		},
		{
			desc: "interfaces without matching k8s networks should be ignored",
			networks: []*networkv1.Network{
				network(networkv1.DefaultNetworkName, DefaultGKENetworkParamsName),
				network(RedNetworkName, RedGKENetworkParamsName),
			},
			gkeNwParams: []*networkv1.GKENetworkParams{
				gkeNetworkParams(DefaultGKENetworkParamsName, DefaultVPCName, DefaultVPCSubnetName, []string{DefaultSecondaryRangeA, DefaultSecondaryRangeB}),
				gkeNetworkParams(RedGKENetworkParamsName, RedVPCName, RedVPCSubnetName, []string{RedSecondaryRangeA, RedSecondaryRangeB}),
			},
			interfaces: []*compute.NetworkInterface{
				interfaces(DefaultVPCName, DefaultVPCSubnetName, "80.1.172.1", []*compute.AliasIpRange{
					{IpCidrRange: "10.11.1.0/24", SubnetworkRangeName: DefaultSecondaryRangeA},
				}),
				interfaces(RedVPCName, RedVPCSubnetName, "10.1.1.1", []*compute.AliasIpRange{
					{IpCidrRange: "172.11.1.0/24", SubnetworkRangeName: RedSecondaryRangeA},
				}),
				interfaces(BlueVPCName, BlueVPCSubnetName, "84.1.2.1", []*compute.AliasIpRange{
					{IpCidrRange: "20.28.1.0/24", SubnetworkRangeName: RedSecondaryRangeA},
				}),
			},
			wantDefaultNwPodCIDRs: []string{"10.11.1.0/24"},
			wantNorthInterfaces: networkv1.NorthInterfacesAnnotation{
				{
					Network:   RedNetworkName,
					IpAddress: "10.1.1.1",
				},
			},
			wantAdditionalNodeNetworks: networkv1.MultiNetworkAnnotation{
				{
					Name:  RedNetworkName,
					Scope: "host-local",
					Cidrs: []string{"172.11.1.0/24"},
				},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			objects := make([]runtime.Object, 0)
			for _, nw := range tc.networks {
				objects = append(objects, nw)
			}
			for _, gnp := range tc.gkeNwParams {
				objects = append(objects, gnp)
			}
			clientSet := fake.NewSimpleClientset(objects...)
			ca := &cloudCIDRAllocator{
				networkClient: clientSet,
			}
			gotDefaultNwCIDRs, gotNorthInterfaces, gotAdditionalNodeNetworks, _ := ca.PerformMultiNetworkCIDRAllocation(node, tc.interfaces)
			assert.Equal(t, tc.wantDefaultNwPodCIDRs, gotDefaultNwCIDRs)
			assert.Equal(t, tc.wantNorthInterfaces, gotNorthInterfaces)
			assert.Equal(t, tc.wantAdditionalNodeNetworks, gotAdditionalNodeNetworks)
		})
	}
}
