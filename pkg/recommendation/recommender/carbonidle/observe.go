package carbonidle

import (
	"fmt"
	"strings"

	"k8s.io/klog/v2"

	"github.com/gocrane/crane/pkg/common"
	"github.com/gocrane/crane/pkg/recommendation/framework"
)

func (r *CarbonIdleResourceRecommender) Observe(ctx *framework.RecommendationContext) error {
	if ctx.Recommendation.Status.Action != "Delete" {
		return nil
	}

	idleCount := countIdleResources(ctx.Recommendation.Status.Description)
	estimatedSavingsWatts := r.estimateEnergySavings(ctx)

	observation := fmt.Sprintf("Observation: %d idle resource(s) detected, estimated energy savings: %.2fW",
		idleCount, estimatedSavingsWatts)

	if ctx.Recommendation.Status.Description != "" {
		ctx.Recommendation.Status.Description += "; " + observation
	} else {
		ctx.Recommendation.Status.Description = observation
	}

	klog.Infof("%s: %s for %s/%s", r.Name(), observation,
		ctx.Recommendation.Spec.TargetRef.Namespace, ctx.Recommendation.Spec.TargetRef.Name)

	return nil
}

func countIdleResources(description string) int {
	return strings.Count(description, "idle:")
}

func (r *CarbonIdleResourceRecommender) estimateEnergySavings(ctx *framework.RecommendationContext) float64 {
	var totalSavings float64

	podWattsList := ctx.InputValue(keyPodCPUWatts)
	for _, ts := range podWattsList {
		podName := labelValue(ts, "pod_name", common.LabelNamePodName)
		if podName == "" {
			continue
		}
		avgPower := avgSamples(ts.Samples)
		cpuUtil := r.cpuUtilForPod(podName, ctx)
		if avgPower < r.minEnergyWatts && cpuUtil < r.cpuUsageThreshold {
			totalSavings += avgPower
		}
	}

	return totalSavings
}
