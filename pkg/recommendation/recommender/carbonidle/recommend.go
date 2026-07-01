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

func (r *CarbonIdleResourceRecommender) recommendWorkload(ctx *framework.RecommendationContext) error {
	podWattsList := ctx.InputValue(keyPodCPUWatts)
	if len(podWattsList) == 0 {
		return fmt.Errorf("no pod energy data available for idle classification")
	}

	var idleDescriptions []string

	for _, ts := range podWattsList {
		podName := labelValue(ts, "pod_name", common.LabelNamePodName)
		podNS := labelValue(ts, "container_namespace", "")
		if podName == "" {
			continue
		}
		avgPower := avgSamples(ts.Samples)
		cpuUtil := r.cpuUtilForPod(podName, ctx)

		if avgPower < r.minEnergyWatts && cpuUtil < r.cpuUsageThreshold {
			desc := fmt.Sprintf("Pod %s/%s idle: avg power %.4fW < %.4fW, CPU util %.2f%% < %.2f%%",
				podNS, podName, avgPower, r.minEnergyWatts, cpuUtil*100, r.cpuUsageThreshold*100)
			idleDescriptions = append(idleDescriptions, desc)
			klog.Infof("%s: %s", r.Name(), desc)
		}
	}

	containerWattsList := ctx.InputValue(keyContainerCPUWatts)
	for _, ts := range containerWattsList {
		podName := labelValue(ts, "pod_name", common.LabelNamePodName)
		podNS := labelValue(ts, "container_namespace", "")
		cName := labelValue(ts, "container_name", common.LabelNameContainerName)
		if podName == "" || cName == "" {
			continue
		}
		avgPower := avgSamples(ts.Samples)
		cpuUtil := r.cpuUtilForPod(podName, ctx)

		if avgPower < r.minEnergyWatts && cpuUtil < r.cpuUsageThreshold {
			desc := fmt.Sprintf("Container %s/%s/%s idle: avg power %.4fW < %.4fW, CPU util %.2f%% < %.2f%%",
				podNS, podName, cName, avgPower, r.minEnergyWatts, cpuUtil*100, r.cpuUsageThreshold*100)
			idleDescriptions = append(idleDescriptions, desc)
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

func (r *CarbonIdleResourceRecommender) recommendNode(ctx *framework.RecommendationContext) error {
	podWattsList := ctx.InputValue(keyPodCPUWatts)
	if len(podWattsList) == 0 {
		return fmt.Errorf("no pod energy data available for node %s", ctx.Recommendation.Spec.TargetRef.Name)
	}

	daemonSetPods := make(map[string]bool)
	for i := range ctx.Pods {
		if isDaemonSetPod(&ctx.Pods[i]) {
			daemonSetPods[ctx.Pods[i].Name] = true
		}
	}

	var activePods []string
	var idleCount int
	for _, ts := range podWattsList {
		podName := labelValue(ts, "pod_name", common.LabelNamePodName)
		if podName == "" || daemonSetPods[podName] {
			continue
		}
		avgPower := avgSamples(ts.Samples)
		cpuUtil := r.cpuUtilForPod(podName, ctx)
		if avgPower >= r.minEnergyWatts || cpuUtil >= r.cpuUsageThreshold {
			activePods = append(activePods, podName)
		} else {
			idleCount++
		}
	}

	if len(activePods) > 0 {
		return fmt.Errorf("node %s not idle: %d active non-DaemonSet pod(s) (e.g. %s)",
			ctx.Recommendation.Spec.TargetRef.Name, len(activePods), activePods[0])
	}

	ctx.Recommendation.Status.Action = "Delete"
	ctx.Recommendation.Status.Description = fmt.Sprintf(
		"Node %s idle: all %d non-DaemonSet pod(s) are energy-idle (< %.4fW, CPU < %.2f%%)",
		ctx.Recommendation.Spec.TargetRef.Name, idleCount, r.minEnergyWatts, r.cpuUsageThreshold*100)
	return nil
}

func labelValue(ts *common.TimeSeries, primary, fallback string) string {
	v := common.GetValueByName(ts.Labels, primary)
	if v == "" && fallback != "" {
		v = common.GetValueByName(ts.Labels, fallback)
	}
	return v
}

func isDaemonSetPod(pod *corev1.Pod) bool {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

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


func (r *CarbonIdleResourceRecommender) cpuUtilForPod(podName string, ctx *framework.RecommendationContext) float64 {
	podWattsList := ctx.InputValue(keyPodCPUWatts)
	nodeActiveList := ctx.InputValue(keyNodeCPUActiveWatts)

	podAvg := r.avgPowerForPod(podName, podWattsList)
	if podAvg <= 0 {
		return 0
	}

	if len(nodeActiveList) > 0 && len(nodeActiveList[0].Samples) > 0 {
		nodeActive := avgSamples(nodeActiveList[0].Samples)
		if nodeActive > 0 {
			return podAvg / nodeActive
		}
	}

	if podAvg < r.minEnergyWatts {
		return 0
	}
	return r.cpuUsageThreshold 
}

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

type PatchReplicas struct {
	Spec PatchReplicasSpec `json:"spec,omitempty"`
}

type PatchReplicasSpec struct {
	Replicas *int32 `json:"replicas,omitempty"`
}

type PatchNodeUnschedulable struct {
	Spec PatchNodeSpec `json:"spec,omitempty"`
}

type PatchNodeSpec struct {
	Unschedulable bool `json:"unschedulable"`
}

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

func (r *CarbonIdleResourceRecommender) policyPod(ctx *framework.RecommendationContext) error {
	for _, pod := range ctx.Pods {
		ownerKind := getControllerOwnerKind(&pod)
		if ownerKind != "" {
			return r.policyController(ctx)
		}
	}

	ctx.Recommendation.Status.RecommendedInfo = ""
	ctx.Recommendation.Status.CurrentInfo = ""
	return nil
}

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

func getControllerOwnerKind(pod *corev1.Pod) string {
	for _, ref := range pod.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			return ref.Kind
		}
	}
	return ""
}
