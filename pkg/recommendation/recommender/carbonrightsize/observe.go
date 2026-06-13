package carbonrightsize

import (
	"encoding/json"
	"fmt"

	"k8s.io/klog/v2"

	"github.com/gocrane/crane/pkg/recommendation/framework"
)

// Observe records the total energy reduction estimate and number of right-sized containers
// in the Recommendation status.
func (r *CarbonRightSizingRecommender) Observe(ctx *framework.RecommendationContext) error {
	if ctx.Recommendation.Status.Action != "Patch" {
		return nil
	}

	rightSizedCount := r.countRightSizedContainers(ctx)
	energyReduction := r.estimateEnergyReduction(ctx)

	observation := fmt.Sprintf("Observation: %d container(s) right-sized, estimated energy reduction: %.2fW",
		rightSizedCount, energyReduction)

	if ctx.Recommendation.Status.Description != "" {
		ctx.Recommendation.Status.Description += "; " + observation
	} else {
		ctx.Recommendation.Status.Description = observation
	}

	klog.Infof("%s: %s for %s/%s", r.Name(), observation,
		ctx.Recommendation.Spec.TargetRef.Namespace, ctx.Recommendation.Spec.TargetRef.Name)

	return nil
}

// countRightSizedContainers returns the number of containers in the recommended patch.
func (r *CarbonRightSizingRecommender) countRightSizedContainers(ctx *framework.RecommendationContext) int {
	if ctx.Recommendation.Status.RecommendedInfo == "" {
		return 0
	}

	var patch PatchResource
	if err := json.Unmarshal([]byte(ctx.Recommendation.Status.RecommendedInfo), &patch); err != nil {
		klog.Warningf("%s: failed to parse recommended info for container count: %v", r.Name(), err)
		return 0
	}

	return len(patch.Spec.Template.Spec.Containers)
}

// estimateEnergyReduction estimates the total energy reduction in watts by comparing
// current pod energy consumption against the efficiency target. Pods below the target
// are expected to save energy proportional to the gap between current and target efficiency.
func (r *CarbonRightSizingRecommender) estimateEnergyReduction(ctx *framework.RecommendationContext) float64 {
	podWattsList := ctx.InputValue(keyPodCPUWatts)
	efficiencyList := ctx.InputValue(keyEnergyEfficiency)

	if len(podWattsList) == 0 {
		return 0
	}

	var totalReduction float64
	for i, ts := range podWattsList {
		if len(ts.Samples) == 0 {
			continue
		}

		avgPower := avgSamples(ts.Samples)

		// Get the efficiency ratio for this pod (or use the first available).
		var effRatio float64
		if i < len(efficiencyList) && len(efficiencyList[i].Samples) > 0 {
			effRatio = efficiencyList[i].Samples[0].Value
		}

		if effRatio < r.energyEfficiencyTarget && effRatio > 0 {
			// Estimated savings: the wasted energy fraction that right-sizing would reclaim.
			wastedFraction := 1.0 - (effRatio / r.energyEfficiencyTarget)
			totalReduction += avgPower * wastedFraction
		}
	}

	return totalReduction
}
