package carbonshift

import (
	"fmt"

	"github.com/gocrane/crane/pkg/recommendation/framework"
)

var acceptedKinds = map[string]bool{
	"Deployment":  true,
	"StatefulSet": true,
	"DaemonSet":   true,
	"CronJob":     true,
	"Job":         true,
}

func filterShared(ctx *framework.RecommendationContext) error {
	kind := ctx.Recommendation.Spec.TargetRef.Kind
	if !acceptedKinds[kind] {
		return fmt.Errorf("carbon shifting recommender does not support resource kind %q; accepted kinds are Deployment, StatefulSet, DaemonSet, CronJob, Job", kind)
	}

	switch kind {
	case "CronJob", "Job":
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

func (r *CarbonTemporalShiftingRecommender) Filter(ctx *framework.RecommendationContext) error {
	if err := r.BaseRecommender.Filter(ctx); err != nil {
		return err
	}
	return filterShared(ctx)
}

func (r *CarbonSpatialShiftingRecommender) Filter(ctx *framework.RecommendationContext) error {
	if err := r.BaseRecommender.Filter(ctx); err != nil {
		return err
	}
	return filterShared(ctx)
}
