// Copyright 2020 Intel Corporation. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fpga

import (
	"fmt"
	"time"

	"github.com/intel/intel-device-plugins-for-kubernetes/test/e2e/utils"
	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/kubernetes/test/e2e/framework"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
)

const (
	pluginDeployScript  = "scripts/deploy-fpgaplugin.sh"
	webhookDeployScript = "scripts/webhook-deploy.sh"
	nlb0NodeResource    = "fpga.intel.com/af-d8424dc4a4a3c413f89e433683f9040b"
	nlb0PodResource     = "fpga.intel.com/arria10.dcp1.2-nlb0"
	nlb3PodResource     = "fpga.intel.com/arria10.dcp1.2-nlb3"
	arria10NodeResource = "fpga.intel.com/region-69528db6eb31577a8c3668f9faa081f6"
)

func init() {
	ginkgo.Describe("FPGA Plugin E2E tests", describe)
}

func describe() {
	webhookDeployScriptPath, err := utils.LocateRepoFile(webhookDeployScript)
	if err != nil {
		framework.Failf("unable to locate %q: %v", webhookDeployScript, err)
	}

	pluginDeployScriptPath, err := utils.LocateRepoFile(pluginDeployScript)
	if err != nil {
		framework.Failf("unable to locate %q: %v", pluginDeployScript, err)
	}

	fmw := framework.NewDefaultFramework("fpgaplugin-e2e")

	ginkgo.It("Run FPGA plugin tests", func() {
		// Run region test case twice to ensure that device is reprogrammed at least once
		runTestCase(fmw, webhookDeployScriptPath, pluginDeployScriptPath, "region", "orchestrated", arria10NodeResource, nlb3PodResource, "nlb3", "nlb0")
		runTestCase(fmw, webhookDeployScriptPath, pluginDeployScriptPath, "region", "orchestrated", arria10NodeResource, nlb0PodResource, "nlb0", "nlb3")
		// Run af test case
		runTestCase(fmw, webhookDeployScriptPath, pluginDeployScriptPath, "af", "preprogrammed", nlb0NodeResource, nlb0PodResource, "nlb0", "nlb3")
	})
}

func runTestCase(fmw *framework.Framework, webhookDeployScriptPath, pluginDeployScriptPath, pluginMode, webhookMode, nodeResource, podResource, cmd1, cmd2 string) {
	ginkgo.By(fmt.Sprintf("deploying webhook in %s mode", webhookMode))
	_, _, err := framework.RunCmd(webhookDeployScriptPath, "--mode", webhookMode, "--namespace", fmw.Namespace.Name)
	framework.ExpectNoError(err)

	waitForPod(fmw, "intel-fpga-webhook")

	ginkgo.By(fmt.Sprintf("deploying FPGA plugin in %s mode", pluginMode))
	_, _, err = framework.RunCmd(pluginDeployScriptPath, "--mode", pluginMode, "--namespace", fmw.Namespace.Name)
	framework.ExpectNoError(err)

	waitForPod(fmw, "intel-fpga-plugin")

	resource := v1.ResourceName(nodeResource)
	ginkgo.By("checking if the resource is allocatable")
	if err := utils.WaitForNodesWithResource(fmw.ClientSet, resource, 30*time.Second); err != nil {
		framework.Failf("unable to wait for nodes to have positive allocatable resource: %v", err)
	}

	resource = v1.ResourceName(podResource)
	image := "intel/opae-nlb-demo:devel"

	ginkgo.By("submitting a pod requesting correct FPGA resources")
	pod := createPod(fmw, fmt.Sprintf("fpgaplugin-nlb-%s-%s-%s-correct", pluginMode, cmd1, cmd2), resource, image, []string{cmd1})

	ginkgo.By("waiting the pod to finish successfully")
	fmw.PodClient().WaitForSuccess(pod.ObjectMeta.Name, 60*time.Second)
	// If WaitForSuccess fails, ginkgo doesn't show the logs of the failed container.
	// Replacing WaitForSuccess with WaitForFinish + 'kubelet logs' would show the logs
	// fmw.PodClient().WaitForFinish(pod.ObjectMeta.Name, 60*time.Second)
	// framework.RunKubectlOrDie("--namespace", fmw.Namespace.Name, "logs", pod.ObjectMeta.Name)
	// return

	ginkgo.By("submitting a pod requesting incorrect FPGA resources")
	pod = createPod(fmw, fmt.Sprintf("fpgaplugin-nlb-%s-%s-%s-incorrect", pluginMode, cmd1, cmd2), resource, image, []string{cmd2})

	ginkgo.By("waiting the pod failure")
	fmw.PodClient().WaitForFailure(pod.ObjectMeta.Name, 60*time.Second)
}

func createPod(fmw *framework.Framework, name string, resourceName v1.ResourceName, image string, command []string) *v1.Pod {
	resourceList := v1.ResourceList{resourceName: resource.MustParse("1"),
		"cpu":           resource.MustParse("1"),
		"hugepages-2Mi": resource.MustParse("20Mi")}
	podSpec := fmw.NewTestPod(name, resourceList, resourceList)
	podSpec.Spec.RestartPolicy = v1.RestartPolicyNever
	podSpec.Spec.Containers[0].Image = image
	podSpec.Spec.Containers[0].Command = command
	podSpec.Spec.Containers[0].SecurityContext = &v1.SecurityContext{
		Capabilities: &v1.Capabilities{
			Add: []v1.Capability{"IPC_LOCK"},
		},
	}

	pod, err := fmw.ClientSet.CoreV1().Pods(fmw.Namespace.Name).Create(podSpec)
	framework.ExpectNoError(err, "pod Create API error")
	return pod
}

func waitForPod(fmw *framework.Framework, name string) {
	ginkgo.By(fmt.Sprintf("waiting for %s availability", name))
	if _, err := e2epod.WaitForPodsWithLabelRunningReady(fmw.ClientSet, fmw.Namespace.Name,
		labels.Set{"app": name}.AsSelector(), 1, 10*time.Second); err != nil {
		framework.DumpAllNamespaceInfo(fmw.ClientSet, fmw.Namespace.Name)
		framework.LogFailedContainers(fmw.ClientSet, fmw.Namespace.Name, framework.Logf)
		framework.Failf("unable to wait for all pods to be running and ready: %v", err)
	}
}
