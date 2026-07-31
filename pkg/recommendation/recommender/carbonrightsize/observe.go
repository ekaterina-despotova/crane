package carbonrightsize

import (
	"encoding/json"
	"fmt"

	"k8s.io/klog/v2"

	"github.com/gocrane/crane/pkg/recommendation/framework"
)

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

		var effRatio float64
		if i < len(efficiencyList) && len(efficiencyList[i].Samples) > 0 {
			effRatio = efficiencyList[i].Samples[0].Value
		}

		if effRatio < r.energyEfficiencyTarget && effRatio > 0 {
			wastedFraction := 1.0 - (effRatio / r.energyEfficiencyTarget)
			totalReduction += avgPower * wastedFraction
		}
	}

	return totalReduction
}
