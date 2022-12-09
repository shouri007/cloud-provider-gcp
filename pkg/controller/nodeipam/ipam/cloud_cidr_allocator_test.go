//go:build !providerless
// +build !providerless

/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ipam

import (
	"fmt"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	networkv1 "k8s.io/cloud-provider-gcp/crd/apis/network/v1"
	"k8s.io/cloud-provider-gcp/pkg/controller/testutil"
	netutils "k8s.io/utils/net"
)

func hasNodeInProcessing(ca *cloudCIDRAllocator, name string) bool {
	ca.lock.Lock()
	defer ca.lock.Unlock()

	_, found := ca.nodesInProcessing[name]
	return found
}

func TestBoundedRetries(t *testing.T) {
	clientSet := fake.NewSimpleClientset()
	updateChan := make(chan string, 1) // need to buffer as we are using only on go routine
	stopChan := make(chan struct{})
	sharedInfomer := informers.NewSharedInformerFactory(clientSet, 1*time.Hour)
	ca := &cloudCIDRAllocator{
		client:            clientSet,
		nodeUpdateChannel: updateChan,
		nodeLister:        sharedInfomer.Core().V1().Nodes().Lister(),
		nodesSynced:       sharedInfomer.Core().V1().Nodes().Informer().HasSynced,
		nodesInProcessing: map[string]*nodeProcessingInfo{},
	}
	go ca.worker(stopChan)
	nodeName := "testNode"
	ca.AllocateOrOccupyCIDR(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
		},
	})
	for hasNodeInProcessing(ca, nodeName) {
		// wait for node to finish processing (should terminate and not time out)
	}
}

func withinExpectedRange(got time.Duration, expected time.Duration) bool {
	return got >= expected/2 && got <= 3*expected/2
}

func TestNodeUpdateRetryTimeout(t *testing.T) {
	for _, tc := range []struct {
		count int
		want  time.Duration
	}{
		{count: 0, want: 250 * time.Millisecond},
		{count: 1, want: 500 * time.Millisecond},
		{count: 2, want: 1000 * time.Millisecond},
		{count: 3, want: 2000 * time.Millisecond},
		{count: 50, want: 5000 * time.Millisecond},
	} {
		t.Run(fmt.Sprintf("count %d", tc.count), func(t *testing.T) {
			if got := nodeUpdateRetryTimeout(tc.count); !withinExpectedRange(got, tc.want) {
				t.Errorf("nodeUpdateRetryTimeout(tc.count) = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestNeedPodCIDRsUpdate(t *testing.T) {
	for _, tc := range []struct {
		desc         string
		cidrs        []string
		nodePodCIDR  string
		nodePodCIDRs []string
		want         bool
		wantErr      bool
	}{
		{
			desc:         "want error - invalid cidr",
			cidrs:        []string{"10.10.10.0/24"},
			nodePodCIDR:  "10.10..0/24",
			nodePodCIDRs: []string{"10.10..0/24"},
			want:         true,
		},
		{
			desc:         "want error - cidr len 2 but not dual stack",
			cidrs:        []string{"10.10.10.0/24", "10.10.11.0/24"},
			nodePodCIDR:  "10.10.10.0/24",
			nodePodCIDRs: []string{"10.10.10.0/24", "2001:db8::/64"},
			wantErr:      true,
		},
		{
			desc:         "want false - matching v4 only cidr",
			cidrs:        []string{"10.10.10.0/24"},
			nodePodCIDR:  "10.10.10.0/24",
			nodePodCIDRs: []string{"10.10.10.0/24"},
			want:         false,
		},
		{
			desc:  "want false - nil node.Spec.PodCIDR",
			cidrs: []string{"10.10.10.0/24"},
			want:  true,
		},
		{
			desc:         "want true - non matching v4 only cidr",
			cidrs:        []string{"10.10.10.0/24"},
			nodePodCIDR:  "10.10.11.0/24",
			nodePodCIDRs: []string{"10.10.11.0/24"},
			want:         true,
		},
		{
			desc:         "want false - matching v4 and v6 cidrs",
			cidrs:        []string{"10.10.10.0/24", "2001:db8::/64"},
			nodePodCIDR:  "10.10.10.0/24",
			nodePodCIDRs: []string{"10.10.10.0/24", "2001:db8::/64"},
			want:         false,
		},
		{
			desc:         "want false - matching v4 and v6 cidrs, different strings but same CIDRs",
			cidrs:        []string{"10.10.10.0/24", "2001:db8::/64"},
			nodePodCIDR:  "10.10.10.0/24",
			nodePodCIDRs: []string{"10.10.10.0/24", "2001:db8:0::/64"},
			want:         false,
		},
		{
			desc:         "want true - matching v4 and non matching v6 cidrs",
			cidrs:        []string{"10.10.10.0/24", "2001:db8::/64"},
			nodePodCIDR:  "10.10.10.0/24",
			nodePodCIDRs: []string{"10.10.10.0/24", "2001:dba::/64"},
			want:         true,
		},
		{
			desc:  "want true - nil node.Spec.PodCIDRs",
			cidrs: []string{"10.10.10.0/24", "2001:db8::/64"},
			want:  true,
		},
		{
			desc:         "want true - matching v6 and non matching v4 cidrs",
			cidrs:        []string{"10.10.10.0/24", "2001:db8::/64"},
			nodePodCIDR:  "10.10.1.0/24",
			nodePodCIDRs: []string{"10.10.1.0/24", "2001:db8::/64"},
			want:         true,
		},
		{
			desc:         "want true - missing v6",
			cidrs:        []string{"10.10.10.0/24", "2001:db8::/64"},
			nodePodCIDR:  "10.10.10.0/24",
			nodePodCIDRs: []string{"10.10.10.0/24"},
			want:         true,
		},
	} {
		var node v1.Node
		node.Spec.PodCIDR = tc.nodePodCIDR
		node.Spec.PodCIDRs = tc.nodePodCIDRs
		netCIDRs, err := netutils.ParseCIDRs(tc.cidrs)
		if err != nil {
			t.Errorf("failed to parse %v as CIDRs: %v", tc.cidrs, err)
		}

		t.Run(tc.desc, func(t *testing.T) {
			got, err := needPodCIDRsUpdate(&node, netCIDRs)
			if tc.wantErr == (err == nil) {
				t.Errorf("err: %v, wantErr: %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("got: %v, want: %v", got, tc.want)
			}
		})
	}
}

type multiNetworkTestCase struct {
	description            string
	fakeNodeHandler        *testutil.FakeNodeHandler
	northInterfaces        networkv1.NorthInterfacesAnnotation
	additionalNodeNetworks networkv1.MultiNetworkAnnotation
	expectedIPCapacities   map[string]int64
}

func TestUpdateMultiNetworkAnnotations(t *testing.T) {
	testCases := []multiNetworkTestCase{
		{
			description: "node with no additional networks",
			fakeNodeHandler: &testutil.FakeNodeHandler{
				Existing: []*v1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
							},
						},
					},
				},
				Clientset: fake.NewSimpleClientset(),
			},			
		},
		{
			description: "node with 2 additional networks",
			fakeNodeHandler: &testutil.FakeNodeHandler{
				Existing: []*v1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
							},
						},
					},
				},
				Clientset: fake.NewSimpleClientset(),
			},
			northInterfaces: []networkv1.NorthInterface{
				{
					Network:   "Blue-Network",
					IpAddress: "172.10.0.1",
				},
				{
					Network:   "Red-Network",
					IpAddress: "10.10.0.1",
				},
			},
			additionalNodeNetworks: []networkv1.NodeNetwork{
				{
					Name:  "Blue-Network",
					Cidrs: []string{"30.20.10.0/24"},
					Scope: "host-local",
				},
				{
					Name:  "Red-Network",
					Cidrs: []string{"10.20.30.0/24"},
					Scope: "host-local",
				},
			},
			expectedIPCapacities: map[string]int64{
				networkv1.NetworkResourceKeyPrefix + "Red-Network.IP":  128,
				networkv1.NetworkResourceKeyPrefix + "Blue-Network.IP": 128,
			},
		},
	}
	// test function
	testFunc := func(tc multiNetworkTestCase) {
		for _, node := range tc.fakeNodeHandler.Existing {
			ca := &cloudCIDRAllocator{
				client: tc.fakeNodeHandler,
			}
			if err := ca.updateMultiNetworkAnnotations(node, tc.northInterfaces, tc.additionalNodeNetworks); err != nil {
				t.Errorf("%v: unexpected error in updateMultiNetworkAnnotations: %v", tc.description, err)
			}
		}
		for _, updatedNode := range tc.fakeNodeHandler.GetUpdatedNodesCopy() {
			expectedNorthInterfaceAnnotation, _ := networkv1.MarshalNorthInterfacesAnnotation(tc.northInterfaces)
			a, ok := updatedNode.ObjectMeta.Annotations[networkv1.NorthInterfacesAnnotationKey]
			if a != expectedNorthInterfaceAnnotation || !ok {
				t.Errorf("%v: incorrect north-interface annotation on the node, got: %s, want: %s", tc.description, a, expectedNorthInterfaceAnnotation)
			}
			expectedMultiNetworkAnnotation, _ := networkv1.MarshalAnnotation(tc.additionalNodeNetworks)
			a, ok = updatedNode.ObjectMeta.Annotations[networkv1.MultiNetworkAnnotationKey]
			if a != expectedMultiNetworkAnnotation || !ok {
				t.Errorf("%v: incorrect multinetwork annotation on the node, got: %s, want: %s", tc.description, a, expectedMultiNetworkAnnotation)
			}			
			gotCapacities := updatedNode.Status.Capacity
			if len(gotCapacities) != len(tc.expectedIPCapacities) {
				t.Errorf("%s: incorrect capacities on the node status, got: %v, want: %v", tc.description, gotCapacities, tc.expectedIPCapacities)
			}
			for k, v := range tc.expectedIPCapacities {
				q, ok := gotCapacities[v1.ResourceName(k)]				
				if !ok || v != q.Value() {
					t.Errorf("%v: incorrect IP capacity for network %s on the node, got: %v, want: %v", tc.description, k, q.Value(), v)
				}
			}
		}
	}

	// run the test cases
	for _, tc := range testCases {
		testFunc(tc)
	}
}
