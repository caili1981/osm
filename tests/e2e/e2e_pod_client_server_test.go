package e2e

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/openservicemesh/osm/tests/framework"
)

var _ = OSMDescribe("Test HTTP traffic from 1 pod client -> 1 pod server",
	OSMDescribeInfo{
		Tier:   1,
		Bucket: 1,
	},
	func() {
		Context("SimpleClientServer with a Kubernetes Service for the Source: HTTP", func() {
			testTraffic(true)
		})

		Context("SimpleClientServer without a Kubernetes Service for the Source: HTTP", func() {
			testTraffic(false)
		})

		Context("SimpleClientServer without a Kubernetes Service for the Source: TCP", func() {
			testTCPTraffic(false)
		})
	})

func testTraffic(withSourceKubernetesService bool) {
	{
		const sourceName = "client"
		const destName = "server"
		var ns = []string{sourceName, destName}

		It("Tests HTTP traffic for client pod -> server pod", func() {
			// Install OSM
			Expect(Td.InstallOSM(Td.GetOSMInstallOpts())).To(Succeed())

			// Create Test NS
			for _, n := range ns {
				Expect(Td.CreateNs(n, nil)).To(Succeed())
				Expect(Td.AddNsToMesh(true, n)).To(Succeed())
			}

			// Get simple pod definitions for the HTTP server
			svcAccDef, podDef, svcDef := Td.SimplePodApp(
				SimplePodAppDef{
					Name:      destName,
					Namespace: destName,
					Image:     "kennethreitz/httpbin",
					Ports:     []int{80},
				})

			_, err := Td.CreateServiceAccount(destName, &svcAccDef)
			Expect(err).NotTo(HaveOccurred())
			_, err = Td.CreatePod(destName, podDef)
			Expect(err).NotTo(HaveOccurred())
			dstSvc, err := Td.CreateService(destName, svcDef)
			Expect(err).NotTo(HaveOccurred())

			// Expect it to be up and running in it's receiver namespace
			Expect(Td.WaitForPodsRunningReady(destName, 90*time.Second, 1)).To(Succeed())

			srcPod := setupSource(sourceName, withSourceKubernetesService)

			By("Creating SMI policies")
			// Deploy allow rule client->server
			httpRG, trafficTarget := Td.CreateSimpleAllowPolicy(
				SimpleAllowPolicy{
					RouteGroupName:    "routes",
					TrafficTargetName: "test-target",

					SourceNamespace:      sourceName,
					SourceSVCAccountName: sourceName,

					DestinationNamespace:      destName,
					DestinationSvcAccountName: destName,
				})

			// Configs have to be put into a monitored NS
			_, err = Td.CreateHTTPRouteGroup(sourceName, httpRG)
			Expect(err).NotTo(HaveOccurred())
			_, err = Td.CreateTrafficTarget(sourceName, trafficTarget)
			Expect(err).NotTo(HaveOccurred())

			// All ready. Expect client to reach server
			clientToServer := HTTPRequestDef{
				SourceNs:        sourceName,
				SourcePod:       srcPod.Name,
				SourceContainer: sourceName,

				Destination: fmt.Sprintf("%s.%s", dstSvc.Name, dstSvc.Namespace),
			}

			srcToDestStr := fmt.Sprintf("%s -> %s",
				fmt.Sprintf("%s/%s", sourceName, srcPod.Name),
				clientToServer.Destination)

			cond := Td.WaitForRepeatedSuccess(func() bool {
				result := Td.HTTPRequest(clientToServer)

				if result.Err != nil || result.StatusCode != 200 {
					Td.T.Logf("> (%s) HTTP Req failed %d %v",
						srcToDestStr, result.StatusCode, result.Err)
					return false
				}
				Td.T.Logf("> (%s) HTTP Req succeeded: %d", srcToDestStr, result.StatusCode)
				return true
			}, 5, 90*time.Second)

			sourceService := map[bool]string{true: "with", false: "without"}[withSourceKubernetesService]
			Expect(cond).To(BeTrue(), "Failed testing HTTP traffic from source pod %s Kubernetes Service to a destination", sourceService)

			By("Deleting SMI policies")
			Expect(Td.SmiClients.AccessClient.AccessV1alpha2().TrafficTargets(sourceName).Delete(context.TODO(), trafficTarget.Name, metav1.DeleteOptions{})).To(Succeed())
			Expect(Td.SmiClients.SpecClient.SpecsV1alpha3().HTTPRouteGroups(sourceName).Delete(context.TODO(), httpRG.Name, metav1.DeleteOptions{})).To(Succeed())

			// Expect client not to reach server
			cond = Td.WaitForRepeatedSuccess(func() bool {
				result := Td.HTTPRequest(clientToServer)

				// Curl exit code 7 == Conn refused
				if result.Err == nil || !strings.Contains(result.Err.Error(), "command terminated with exit code 7 ") {
					Td.T.Logf("> (%s) HTTP Req failed, incorrect expected result: %d, %v", srcToDestStr, result.StatusCode, result.Err)
					return false
				}
				Td.T.Logf("> (%s) HTTP Req failed correctly: %v", srcToDestStr, result.Err)
				return true
			}, 5, 150*time.Second)
			Expect(cond).To(BeTrue())
		})
	}
}

func testTCPTraffic(withSourceKubernetesService bool) {
	{
		const sourceName = "client"
		const destName = "server"
		var ns = []string{sourceName, destName}

		It("Tests TCP traffic for client pod -> server pod", func() {
			installOpts := Td.GetOSMInstallOpts()
			// Install OSM
			Expect(Td.InstallOSM(installOpts)).To(Succeed())

			// Load TCP server image
			Expect(Td.LoadImagesToKind([]string{"tcp-echo-server"})).To(Succeed())

			// Create Test NS
			for _, n := range ns {
				Expect(Td.CreateNs(n, nil)).To(Succeed())
				Expect(Td.AddNsToMesh(true, n)).To(Succeed())
			}

			destinationPort := 80

			// Get simple pod definitions for the TCP server
			svcAccDef, podDef, svcDef := Td.SimplePodApp(
				SimplePodAppDef{
					Name:        destName,
					Namespace:   destName,
					Image:       fmt.Sprintf("%s/tcp-echo-server:%s", installOpts.ContainerRegistryLoc, installOpts.OsmImagetag),
					Command:     []string{"/tcp-echo-server"},
					Args:        []string{"--port", fmt.Sprintf("%d", destinationPort)},
					Ports:       []int{destinationPort},
					AppProtocol: AppProtocolTCP,
				})

			_, err := Td.CreateServiceAccount(destName, &svcAccDef)
			Expect(err).NotTo(HaveOccurred())
			_, err = Td.CreatePod(destName, podDef)
			Expect(err).NotTo(HaveOccurred())
			dstSvc, err := Td.CreateService(destName, svcDef)
			Expect(err).NotTo(HaveOccurred())

			// Expect it to be up and running in it's receiver namespace
			Expect(Td.WaitForPodsRunningReady(destName, 120*time.Second, 1)).To(Succeed())

			srcPod := setupSource(sourceName, withSourceKubernetesService)

			By("Creating SMI policies")
			// Deploy allow rule client->server
			tcpRoute, trafficTarget := Td.CreateSimpleTCPAllowPolicy(
				SimpleAllowPolicy{
					RouteGroupName:    "routes",
					TrafficTargetName: "test-target",

					SourceNamespace:      sourceName,
					SourceSVCAccountName: sourceName,

					DestinationNamespace:      destName,
					DestinationSvcAccountName: destName,
				},
				destinationPort,
			)

			// Configs have to be put into a monitored NS
			_, err = Td.CreateTCPRoute(sourceName, tcpRoute)
			Expect(err).NotTo(HaveOccurred())
			_, err = Td.CreateTrafficTarget(sourceName, trafficTarget)
			Expect(err).NotTo(HaveOccurred())

			// All ready. Expect client to reach server
			requestMsg := "test request"
			clientToServer := TCPRequestDef{
				SourceNs:        sourceName,
				SourcePod:       srcPod.Name,
				SourceContainer: sourceName,

				DestinationHost: fmt.Sprintf("%s.%s", dstSvc.Name, dstSvc.Namespace),
				DestinationPort: destinationPort,
				Message:         requestMsg,
			}

			srcToDestStr := fmt.Sprintf("%s -> %s:%d",
				fmt.Sprintf("%s/%s", sourceName, srcPod.Name),
				clientToServer.DestinationHost, clientToServer.DestinationPort)

			cond := Td.WaitForRepeatedSuccess(func() bool {
				result := Td.TCPRequest(clientToServer)

				if result.Err != nil {
					Td.T.Logf("> (%s) TCP Req failed, response: %s, err: %s",
						srcToDestStr, result.Response, result.Err)
					return false
				}

				// Ensure the echo response contains request message
				if !strings.Contains(result.Response, requestMsg) {
					Td.T.Logf("> (%s) Unexpected response: %s.\n Response expected to contain: %s", result.Response, requestMsg)
					return false
				}
				Td.T.Logf("> (%s) TCP Req succeeded, response: %s", srcToDestStr, result.Response)
				return true
			}, 5, 90*time.Second)

			sourceService := map[bool]string{true: "with", false: "without"}[withSourceKubernetesService]
			Expect(cond).To(BeTrue(), "Failed testing TCP traffic from source pod %s Kubernetes Service to a destination", sourceService)

			By("Deleting SMI policies")
			Expect(Td.SmiClients.AccessClient.AccessV1alpha2().TrafficTargets(sourceName).Delete(context.TODO(), trafficTarget.Name, metav1.DeleteOptions{})).To(Succeed())
			Expect(Td.SmiClients.SpecClient.SpecsV1alpha3().TCPRoutes(sourceName).Delete(context.TODO(), tcpRoute.Name, metav1.DeleteOptions{})).To(Succeed())

			// Expect client not to reach server
			cond = Td.WaitForRepeatedSuccess(func() bool {
				result := Td.TCPRequest(clientToServer)

				if result.Err == nil {
					Td.T.Logf("> (%s) TCP Req did not fail, expected it to fail,  response: %s", srcToDestStr, result.Response)
					return false
				}
				Td.T.Logf("> (%s) TCP Req failed correctly, response: %s, err: %s", srcToDestStr, result.Response, result.Err)
				return true
			}, 5, 150*time.Second)
			Expect(cond).To(BeTrue())
		})
	}
}

func setupSource(sourceName string, withKubernetesService bool) *v1.Pod {
	// Get simple Pod definitions for the client
	svcAccDef, podDef, svcDef := Td.SimplePodApp(SimplePodAppDef{
		Name:      sourceName,
		Namespace: sourceName,
		Command:   []string{"sleep", "365d"},
		Image:     "curlimages/curl",
		Ports:     []int{80},
	})

	_, err := Td.CreateServiceAccount(sourceName, &svcAccDef)
	Expect(err).NotTo(HaveOccurred())

	srcPod, err := Td.CreatePod(sourceName, podDef)
	Expect(err).NotTo(HaveOccurred())

	// In some cases we may want to skip the creation of a Kubernetes service for the source.
	if withKubernetesService {
		_, err = Td.CreateService(sourceName, svcDef)
		Expect(err).NotTo(HaveOccurred())
	}

	// Expect it to be up and running in it's receiver namespace
	Expect(Td.WaitForPodsRunningReady(sourceName, 90*time.Second, 1)).To(Succeed())

	return srcPod
}
