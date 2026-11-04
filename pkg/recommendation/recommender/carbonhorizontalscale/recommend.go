package carbonhorizontalscale

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"k8s.io/klog/v2"

	"github.com/gocrane/crane/pkg/common"
	"github.com/gocrane/crane/pkg/recommendation/framework"
)

type PatchReplicas struct {
	Spec PatchReplicasSpec `json:"spec,omitempty"`
}

type PatchReplicasSpec struct {
	Replicas *int32 `json:"replicas,omitempty"`
}

func (r *CarbonHorizontalScalingRecommender) PreRecommend(ctx *framework.RecommendationContext) error {
	return nil
}

func (r *CarbonHorizontalScalingRecommender) Recommend(ctx *framework.RecommendationContext) error {
	if reason := r.protectedReason(ctx); reason != "" {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = fmt.Sprintf(
			"Workload %s/%s is protected from horizontal scaling: %s",
			ctx.Recommendation.Spec.TargetRef.Namespace,
			ctx.Recommendation.Spec.TargetRef.Name, reason)
		return nil
	}

	cpuUsageList := ctx.InputValue(keyCPUUsage)
	if len(cpuUsageList) == 0 {
		return fmt.Errorf("missing CPU usage data for horizontal scaling")
	}

	var currentReplicas int32 = 1
	if ctx.Scale != nil {
		currentReplicas = ctx.Scale.Spec.Replicas
	}

	if currentReplicas <= r.minReplicas {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = fmt.Sprintf(
			"No horizontal scaling needed: already at %d replica(s) (floor %d)",
			currentReplicas, r.minReplicas)
		return nil
	}

	needed := r.computeNeededReplicas(cpuUsageList, currentReplicas)
	if needed >= currentReplicas {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = fmt.Sprintf(
			"No horizontal scaling needed: current %d replica(s) already fit the load (target %.2f cores/replica)",
			currentReplicas, r.replicaTargetCores)
		return nil
	}

	freedWatts := r.estimateFreedWatts(ctx, currentReplicas, needed)

	ctx.Recommendation.Status.Action = "Patch"
	desc := fmt.Sprintf(
		"Consolidate replicas %d -> %d (target %.2f cores/replica)",
		currentReplicas, needed, r.replicaTargetCores)
	if freedWatts > 0 {
		desc += fmt.Sprintf("; estimated energy reclaimed ~%.3fW", freedWatts)
	}
	ctx.Recommendation.Status.Description = desc
	klog.Infof("%s: %s/%s %s",
		r.Name(), ctx.Recommendation.Spec.TargetRef.Namespace,
		ctx.Recommendation.Spec.TargetRef.Name, desc)

	newPatch := PatchReplicas{Spec: PatchReplicasSpec{Replicas: &needed}}
	cur := currentReplicas
	oldPatch := PatchReplicas{Spec: PatchReplicasSpec{Replicas: &cur}}

	newPatchBytes, err := json.Marshal(newPatch)
	if err != nil {
		return fmt.Errorf("failed to encode recommended manifest: %v", err)
	}
	oldPatchBytes, err := json.Marshal(oldPatch)
	if err != nil {
		return fmt.Errorf("failed to encode current manifest: %v", err)
	}

	ctx.Recommendation.Status.RecommendedInfo = string(newPatchBytes)
	ctx.Recommendation.Status.CurrentInfo = string(oldPatchBytes)

	return nil
}

func (r *CarbonHorizontalScalingRecommender) computeNeededReplicas(cpuUsageList []*common.TimeSeries, currentReplicas int32) int32 {
	if r.replicaTargetCores <= 0 {
		return currentReplicas
	}

	aggregateCores := computePercentile(cpuUsageList, r.cpuPercentile)
	if aggregateCores <= 0 {
		return currentReplicas
	}
	needed := int32(math.Ceil(aggregateCores / r.replicaTargetCores))
	if needed < r.minReplicas {
		needed = r.minReplicas
	}
	if needed > currentReplicas {
		needed = currentReplicas
	}
	return needed
}

func (r *CarbonHorizontalScalingRecommender) estimateFreedWatts(ctx *framework.RecommendationContext, current, needed int32) float64 {
	wattsList := ctx.InputValue(keyPodCPUWatts)
	if len(wattsList) == 0 {
		return 0
	}
	var sum float64
	var n int
	for _, ts := range wattsList {
		w := avgSamples(ts.Samples)
		if w > 0 {
			sum += w
			n++
		}
	}
	if n == 0 {
		return 0
	}
	avgPerPod := sum / float64(n)
	return avgPerPod * float64(current-needed)
}

func (r *CarbonHorizontalScalingRecommender) protectedReason(ctx *framework.RecommendationContext) string {
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
	}
	return ""
}

func computePercentile(tsList []*common.TimeSeries, p float64) float64 {
	var values []float64
	for _, ts := range tsList {
		for _, s := range ts.Samples {
			if s.Value > 0 {
				values = append(values, s.Value)
			}
		}
	}
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)

	idx := int(math.Ceil(p*float64(len(values)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(values) {
		idx = len(values) - 1
	}
	return values[idx]
}

func (r *CarbonHorizontalScalingRecommender) Policy(ctx *framework.RecommendationContext) error {
	return nil
}
