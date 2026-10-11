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

func (r *CarbonIdleResourceRecommender) PreRecommend(ctx *framework.RecommendationContext) error {
	return nil
}

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
	if reason := r.protectedReason(ctx); reason != "" {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = fmt.Sprintf(
			"Workload %s/%s is protected from idle shutdown: %s",
			ctx.Recommendation.Spec.TargetRef.Namespace,
			ctx.Recommendation.Spec.TargetRef.Name, reason)
		return nil
	}

	cpuCoresList := ctx.InputValue(keyPodCPUCores)
	netBytesList := ctx.InputValue(keyPodNetBytes)
	if len(cpuCoresList) == 0 {
		return fmt.Errorf("no CPU activity data available for idle classification")
	}

	var idleDescriptions []string

	for _, ts := range cpuCoresList {
		podName := labelValue(ts, "pod", "")
		if podName == "" {
			continue
		}
		avgCores := avgSamples(ts.Samples)
		avgNet := r.netForPod(podName, netBytesList)

		idleFrac := fractionBelow(ts.Samples, r.cpuCoresThreshold)

		cpuIdle := avgCores < r.cpuCoresThreshold
		netIdle := avgNet < r.netBytesThreshold

		if cpuIdle && netIdle && idleFrac >= r.sustainedIdleFraction {
			avgPower := r.avgPowerForPod(podName, ctx.InputValue(keyPodCPUWatts))
			desc := fmt.Sprintf("Pod %s idle: CPU %.4f cores < %.4f, network %.0f B/s < %.0f B/s, idle for %.0f%% of window (>= %.0f%%); reclaimable power %.3fW",
				podName, avgCores, r.cpuCoresThreshold, avgNet, r.netBytesThreshold,
				idleFrac*100, r.sustainedIdleFraction*100, avgPower)
			idleDescriptions = append(idleDescriptions, desc)
			klog.Infof("%s: %s", r.Name(), desc)
		} else if cpuIdle && netIdle {
			klog.Infof("%s: Pod %s idle on average but only %.0f%% of window (< %.0f%% required) — not flagged",
				r.Name(), podName, idleFrac*100, r.sustainedIdleFraction*100)
		} else {
			klog.V(4).Infof("%s: Pod %s active (cpu=%.4f cores, net=%.0f B/s) — not idle",
				r.Name(), podName, avgCores, avgNet)
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
	if r.enableConsolidation {
		decision, err := r.evaluateConsolidation(ctx)
		if err != nil {
			klog.Warningf("%s: consolidation analysis failed for node %s: %v — falling back to idle check",
				r.Name(), ctx.Recommendation.Spec.TargetRef.Name, err)
		} else if decision.Feasible {
			ctx.Recommendation.Status.Action = "Delete"
			ctx.Recommendation.Status.Description = decision.Reason
			if decision.FreedNodeWatts > 0 {
				ctx.Recommendation.Status.Description += fmt.Sprintf(
					"; freeing this node reclaims ~%.2fW of measured node power", decision.FreedNodeWatts)
			}

			if infoBytes, mErr := json.Marshal(decision); mErr == nil {
				ctx.Recommendation.Status.RecommendedValue = string(infoBytes)
			}
			klog.Infof("%s: node %s is a consolidation candidate: %s",
				r.Name(), ctx.Recommendation.Spec.TargetRef.Name, decision.Reason)
			return nil
		} else {
			klog.Infof("%s: node %s not a consolidation candidate: %s",
				r.Name(), ctx.Recommendation.Spec.TargetRef.Name, decision.Reason)
		}
	}

	cpuCoresList := ctx.InputValue(keyPodCPUCores)
	if len(cpuCoresList) == 0 {
		return fmt.Errorf("no CPU activity data available for node %s", ctx.Recommendation.Spec.TargetRef.Name)
	}
	netBytesList := ctx.InputValue(keyPodNetBytes)

	daemonSetPods := make(map[string]bool)
	for i := range ctx.Pods {
		if isDaemonSetPod(&ctx.Pods[i]) {
			daemonSetPods[ctx.Pods[i].Name] = true
		}
	}

	var activePods []string
	var idleCount int
	for _, ts := range cpuCoresList {
		podName := labelValue(ts, "pod", "")
		if podName == "" || daemonSetPods[podName] {
			continue
		}
		avgCores := avgSamples(ts.Samples)
		avgNet := r.netForPod(podName, netBytesList)
		idleFrac := fractionBelow(ts.Samples, r.cpuCoresThreshold)
		if avgCores >= r.cpuCoresThreshold || avgNet >= r.netBytesThreshold || idleFrac < r.sustainedIdleFraction {
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
		"Node %s idle: all %d non-DaemonSet pod(s) are idle (CPU < %.4f cores, network < %.0f B/s)",
		ctx.Recommendation.Spec.TargetRef.Name, idleCount, r.cpuCoresThreshold, r.netBytesThreshold)
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

func (r *CarbonIdleResourceRecommender) netForPod(podName string, netList []*common.TimeSeries) float64 {
	for _, ts := range netList {
		if labelValue(ts, "pod", "") == podName {
			return avgSamples(ts.Samples)
		}
	}
	return 0
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

func fractionBelow(samples []common.Sample, threshold float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	var below int
	for _, s := range samples {
		if s.Value < threshold {
			below++
		}
	}
	return float64(below) / float64(len(samples))
}

func (r *CarbonIdleResourceRecommender) protectedReason(ctx *framework.RecommendationContext) string {
	ns := ctx.Recommendation.Spec.TargetRef.Namespace
	for _, p := range r.protectedNamespaces {
		if p == ns {
			return fmt.Sprintf("namespace %q is in the protected list", ns)
		}
	}

	if r.protectedLabel != "" {
		if v, ok := ctx.Object.GetLabels()[r.protectedLabel]; ok && v == "true" {
			return fmt.Sprintf("workload carries protected label %q", r.protectedLabel)
		}
		for i := range ctx.Pods {
			if v, ok := ctx.Pods[i].Labels[r.protectedLabel]; ok && v == "true" {
				return fmt.Sprintf("pod %s carries protected label %q", ctx.Pods[i].Name, r.protectedLabel)
			}
		}
	}
	return ""
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
