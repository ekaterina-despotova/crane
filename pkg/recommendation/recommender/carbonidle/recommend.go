package carbonidle

import (
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/gocrane/crane/pkg/common"
	"github.com/gocrane/crane/pkg/recommendation/framework"
)

// PreRecommend is a no-op for CarbonIdleResource.
func (r *CarbonIdleResourceRecommender) PreRecommend(ctx *framework.RecommendationContext) error {
	return nil
}

// Recommend classifies pods, containers, and nodes as idle based on energy thresholds
// and sets Action="Delete" for idle resources.
func (r *CarbonIdleResourceRecommender) Recommend(ctx *framework.RecommendationContext) error {
	kind := ctx.Recommendation.Spec.TargetRef.Kind

	switch kind {
	case "Node":
		return r.recommendNode(ctx)
	default:
		return r.recommendWorkload(ctx)
	}
}

// recommendWorkload classifies pods and containers as idle for Deployment/StatefulSet/DaemonSet/Pod targets.
func (r *CarbonIdleResourceRecommender) recommendWorkload(ctx *framework.RecommendationContext) error {
	podWattsList := ctx.InputValue(keyPodCPUWatts)
	if len(podWattsList) == 0 {
		return fmt.Errorf("no pod energy data available for idle classification")
	}

	var idleDescriptions []string

	// Classify pods as idle.
	for _, pod := range ctx.Pods {
		avgPower := r.avgPowerForPod(pod.Name, podWattsList)
		cpuUtil := r.cpuUtilForPod(pod.Name, ctx)

		if avgPower < r.minEnergyWatts && cpuUtil < r.cpuUsageThreshold {
			desc := fmt.Sprintf("Pod %s/%s idle: avg power %.4fW < %.4fW, CPU util %.2f%% < %.2f%%",
				pod.Namespace, pod.Name, avgPower, r.minEnergyWatts, cpuUtil*100, r.cpuUsageThreshold*100)
			idleDescriptions = append(idleDescriptions, desc)
			klog.Infof("%s: %s", r.Name(), desc)
		}
	}

	// Classify containers as idle.
	containerWattsList := ctx.InputValue(keyContainerCPUWatts)
	for _, pod := range ctx.Pods {
		for _, c := range pod.Spec.Containers {
			avgPower := r.avgPowerForContainer(pod.Name, c.Name, containerWattsList)
			cpuUtil := r.cpuUtilForPod(pod.Name, ctx) // approximate container CPU from pod

			if avgPower < r.minEnergyWatts && cpuUtil < r.cpuUsageThreshold {
				desc := fmt.Sprintf("Container %s/%s/%s idle: avg power %.4fW < %.4fW, CPU util %.2f%% < %.2f%%",
					pod.Namespace, pod.Name, c.Name, avgPower, r.minEnergyWatts, cpuUtil*100, r.cpuUsageThreshold*100)
				idleDescriptions = append(idleDescriptions, desc)
			}
		}
	}

	if len(idleDescriptions) == 0 {
		return fmt.Errorf("no idle resources detected for %s/%s",
			ctx.Recommendation.Spec.TargetRef.Namespace, ctx.Recommendation.Spec.TargetRef.Name)
	}

	ctx.Recommendation.Status.Action = "Delete"
	ctx.Recommendation.Status.Description = strings.Join(idleDescriptions, "; ")
	return nil
}

// recommendNode classifies a node as idle when Energy_Idle_Ratio exceeds threshold
// and all non-DaemonSet pods are idle.
func (r *CarbonIdleResourceRecommender) recommendNode(ctx *framework.RecommendationContext) error {
	// Get the computed Energy_Idle_Ratio.
	ratioList := ctx.InputValue(keyEnergyIdleRatio)
	if len(ratioList) == 0 || len(ratioList[0].Samples) == 0 {
		return fmt.Errorf("no Energy_Idle_Ratio data available for node %s", ctx.Recommendation.Spec.TargetRef.Name)
	}
	energyIdleRatio := ratioList[0].Samples[0].Value

	if energyIdleRatio <= r.energyIdleThreshold {
		return fmt.Errorf("node %s Energy_Idle_Ratio %.4f does not exceed threshold %.4f",
			ctx.Recommendation.Spec.TargetRef.Name, energyIdleRatio, r.energyIdleThreshold)
	}

	// Check that all non-DaemonSet pods are idle.
	podWattsList := ctx.InputValue(keyPodCPUWatts)
	for _, pod := range ctx.Pods {
		if isDaemonSetPod(&pod) {
			continue
		}
		avgPower := r.avgPowerForPod(pod.Name, podWattsList)
		cpuUtil := r.cpuUtilForPod(pod.Name, ctx)
		if avgPower >= r.minEnergyWatts || cpuUtil >= r.cpuUsageThreshold {
			return fmt.Errorf("node %s not idle: non-DaemonSet pod %s/%s is active (power=%.4fW, cpu=%.2f%%)",
				ctx.Recommendation.Spec.TargetRef.Name, pod.Namespace, pod.Name, avgPower, cpuUtil*100)
		}
	}

	ctx.Recommendation.Status.Action = "Delete"
	ctx.Recommendation.Status.Description = fmt.Sprintf(
		"Node %s idle: Energy_Idle_Ratio %.4f > %.4f, all non-DaemonSet pods idle",
		ctx.Recommendation.Spec.TargetRef.Name, energyIdleRatio, r.energyIdleThreshold)
	return nil
}

// isDaemonSetPod returns true if the pod is owned by a DaemonSet.
func isDaemonSetPod(pod *corev1.Pod) bool {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

// avgPowerForPod computes the average power consumption for a pod from time series data.
func (r *CarbonIdleResourceRecommender) avgPowerForPod(podName string, tsList []*common.TimeSeries) float64 {
	for _, ts := range tsList {
		name := common.GetValueByName(ts.Labels, "pod_name")
		if name == "" {
			name = common.GetValueByName(ts.Labels, common.LabelNamePodName)
		}
		if name == podName {
			return avgSamples(ts.Samples)
		}
	}
	return 0
}

// avgPowerForContainer computes the average power for a specific container.
func (r *CarbonIdleResourceRecommender) avgPowerForContainer(podName, containerName string, tsList []*common.TimeSeries) float64 {
	for _, ts := range tsList {
		pName := common.GetValueByName(ts.Labels, "pod_name")
		if pName == "" {
			pName = common.GetValueByName(ts.Labels, common.LabelNamePodName)
		}
		cName := common.GetValueByName(ts.Labels, "container_name")
		if cName == "" {
			cName = common.GetValueByName(ts.Labels, common.LabelNameContainerName)
		}
		if pName == podName && cName == containerName {
			return avgSamples(ts.Samples)
		}
	}
	return 0
}

// cpuUtilForPod estimates CPU utilization for a pod. It uses pod CPU watts relative
// to node active watts as a proxy when direct CPU utilization metrics are unavailable.
func (r *CarbonIdleResourceRecommender) cpuUtilForPod(podName string, ctx *framework.RecommendationContext) float64 {
	// Use pod CPU joules as a proxy: if watts are very low, utilization is near zero.
	podWattsList := ctx.InputValue(keyPodCPUWatts)
	nodeActiveList := ctx.InputValue(keyNodeCPUActiveWatts)

	podAvg := r.avgPowerForPod(podName, podWattsList)
	if podAvg <= 0 {
		return 0
	}

	// If node active watts are available, compute ratio as utilization proxy.
	if len(nodeActiveList) > 0 && len(nodeActiveList[0].Samples) > 0 {
		nodeActive := avgSamples(nodeActiveList[0].Samples)
		if nodeActive > 0 {
			return podAvg / nodeActive
		}
	}

	// Fallback: if power is below threshold, assume very low utilization.
	if podAvg < r.minEnergyWatts {
		return 0
	}
	return r.cpuUsageThreshold // conservative: assume at threshold
}

// avgSamples computes the arithmetic mean of sample values.
func avgSamples(samples []common.Sample) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		sum += s.Value
	}
	return sum / float64(len(samples))
}

// PatchReplicas represents a JSON patch for scaling replicas.
type PatchReplicas struct {
	Spec PatchReplicasSpec `json:"spec,omitempty"`
}

// PatchReplicasSpec holds the replicas field for patching.
type PatchReplicasSpec struct {
	Replicas *int32 `json:"replicas,omitempty"`
}

// PatchNodeUnschedulable represents a JSON patch for cordoning a node.
type PatchNodeUnschedulable struct {
	Spec PatchNodeSpec `json:"spec,omitempty"`
}

// PatchNodeSpec holds the unschedulable field for patching.
type PatchNodeSpec struct {
	Unschedulable bool `json:"unschedulable"`
}

// Policy generates shutdown manifests for idle resources.
// Controller-owned pods: scale to zero. Standalone pods: delete. Nodes: cordon.
func (r *CarbonIdleResourceRecommender) Policy(ctx *framework.RecommendationContext) error {
	if ctx.Recommendation.Status.Action != "Delete" {
		return nil
	}

	kind := ctx.Recommendation.Spec.TargetRef.Kind

	switch kind {
	case "Node":
		return r.policyNode(ctx)
	case "Pod":
		return r.policyPod(ctx)
	default:
		return r.policyController(ctx)
	}
}

// policyController generates a scale-to-zero manifest for Deployment/StatefulSet/DaemonSet targets.
func (r *CarbonIdleResourceRecommender) policyController(ctx *framework.RecommendationContext) error {
	var zero int32

	var newPatch PatchReplicas
	newPatch.Spec.Replicas = &zero

	currentReplicas := int32(1)
	if ctx.Scale != nil {
		currentReplicas = ctx.Scale.Spec.Replicas
	}
	var oldPatch PatchReplicas
	oldPatch.Spec.Replicas = &currentReplicas

	newPatchBytes, err := json.Marshal(newPatch)
	if err != nil {
		return fmt.Errorf("failed to encode shutdown manifest: %v", err)
	}
	oldPatchBytes, err := json.Marshal(oldPatch)
	if err != nil {
		return fmt.Errorf("failed to encode current manifest: %v", err)
	}

	ctx.Recommendation.Status.RecommendedInfo = string(newPatchBytes)
	ctx.Recommendation.Status.CurrentInfo = string(oldPatchBytes)
	return nil
}

// policyPod generates a delete action for standalone pods or scale-to-zero for controller-owned pods.
func (r *CarbonIdleResourceRecommender) policyPod(ctx *framework.RecommendationContext) error {
	// Check if the pod is owned by a controller.
	for _, pod := range ctx.Pods {
		ownerKind := getControllerOwnerKind(&pod)
		if ownerKind != "" {
			// Controller-owned: generate scale-to-zero for the owner.
			return r.policyController(ctx)
		}
	}

	// Standalone pod: the Action="Delete" is sufficient; no patch needed.
	ctx.Recommendation.Status.RecommendedInfo = ""
	ctx.Recommendation.Status.CurrentInfo = ""
	return nil
}

// policyNode generates a cordon manifest (set unschedulable=true) for idle nodes.
func (r *CarbonIdleResourceRecommender) policyNode(ctx *framework.RecommendationContext) error {
	newPatch := PatchNodeUnschedulable{Spec: PatchNodeSpec{Unschedulable: true}}
	oldPatch := PatchNodeUnschedulable{Spec: PatchNodeSpec{Unschedulable: false}}

	newPatchBytes, err := json.Marshal(newPatch)
	if err != nil {
		return fmt.Errorf("failed to encode cordon manifest: %v", err)
	}
	oldPatchBytes, err := json.Marshal(oldPatch)
	if err != nil {
		return fmt.Errorf("failed to encode current node manifest: %v", err)
	}

	ctx.Recommendation.Status.RecommendedInfo = string(newPatchBytes)
	ctx.Recommendation.Status.CurrentInfo = string(oldPatchBytes)
	return nil
}

// getControllerOwnerKind returns the Kind of the controller owning the pod, or empty string.
func getControllerOwnerKind(pod *corev1.Pod) string {
	for _, ref := range pod.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			return ref.Kind
		}
	}
	return ""
}
