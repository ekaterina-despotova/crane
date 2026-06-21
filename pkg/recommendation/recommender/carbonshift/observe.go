package carbonshift

import (
	"encoding/json"
	"fmt"

	"k8s.io/klog/v2"

	"github.com/gocrane/crane/pkg/recommendation/framework"
)

// Observe records the carbon savings estimate in the Recommendation status.
func (r *CarbonLoadShiftingRecommender) Observe(ctx *framework.RecommendationContext) error {
	if ctx.Recommendation.Status.Action != "Patch" {
		return nil
	}

	// Parse the manifest to extract savings for the observation.
	var manifest ShiftManifest
	if err := json.Unmarshal([]byte(ctx.Recommendation.Status.RecommendedInfo), &manifest); err != nil {
		klog.Warningf("%s: failed to parse manifest for observation: %v", r.Name(), err)
		return nil
	}

	observation := fmt.Sprintf("Observation: temporal shift to %s could save %.1f gCO₂/day (%.0f%% reduction)",
		manifest.TargetWindow,
		manifest.EstimatedSavings.GramsCO2PerDay,
		manifest.EstimatedSavings.ReductionPercent)

	if ctx.Recommendation.Status.Description != "" {
		ctx.Recommendation.Status.Description += "; " + observation
	} else {
		ctx.Recommendation.Status.Description = observation
	}

	klog.Infof("%s: %s for %s/%s", r.Name(), observation,
		ctx.Recommendation.Spec.TargetRef.Namespace, ctx.Recommendation.Spec.TargetRef.Name)

	return nil
}
