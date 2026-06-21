package carbonshift

import (
	"fmt"

	"github.com/gocrane/crane/pkg/recommendation/framework"
)

// acceptedKinds lists the Kubernetes resource kinds supported by CarbonLoadShifting.
var acceptedKinds = map[string]bool{
	"Deployment":  true,
	"StatefulSet": true,
	"DaemonSet":   true,
	"CronJob":     true,
	"Job":         true,
}

// Filter checks whether the target resource kind is supported by CarbonLoadShifting.
func (r *CarbonLoadShiftingRecommender) Filter(ctx *framework.RecommendationContext) error {
	kind := ctx.Recommendation.Spec.TargetRef.Kind
	if !acceptedKinds[kind] {
		return fmt.Errorf("CarbonLoadShifting recommender does not support resource kind %q; accepted kinds are Deployment, StatefulSet, DaemonSet, CronJob, Job", kind)
	}

	// Delegate base filtering (label selectors, cooldown, deletion check).
	if err := r.BaseRecommender.Filter(ctx); err != nil {
		return err
	}

	// Retrieve pods for workload types that support it.
	switch kind {
	case "CronJob", "Job":
		// Jobs may not have running pods at the time of analysis.
		// RetrievePods will get any currently running pods; if none exist,
		// we rely on historical Kepler data from prior runs.
		_ = framework.RetrievePods(ctx)
	default:
		if err := framework.RetrieveScale(ctx); err != nil {
			return err
		}
		if err := framework.RetrievePods(ctx); err != nil {
			return err
		}
	}

	return nil
}
