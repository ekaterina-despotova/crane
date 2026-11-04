package carbonhorizontalscale

import (
	"fmt"

	"github.com/gocrane/crane/pkg/recommendation/framework"
)

var acceptedKinds = map[string]bool{
	"Deployment":  true,
	"StatefulSet": true,
}

func (r *CarbonHorizontalScalingRecommender) Filter(ctx *framework.RecommendationContext) error {
	kind := ctx.Recommendation.Spec.TargetRef.Kind
	if !acceptedKinds[kind] {
		return fmt.Errorf("CarbonHorizontalScaling recommender does not support resource kind %q; accepted kinds are Deployment, StatefulSet", kind)
	}

	if err := r.BaseRecommender.Filter(ctx); err != nil {
		return err
	}

	if err := framework.RetrieveScale(ctx); err != nil {
		return err
	}

	if err := framework.RetrievePods(ctx); err != nil {
		return err
	}

	return nil
}
