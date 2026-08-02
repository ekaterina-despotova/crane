package carbonrightsize

import (
	"fmt"

	"github.com/gocrane/crane/pkg/recommendation/framework"
)

var acceptedKinds = map[string]bool{
	"Deployment":  true,
	"StatefulSet": true,
	"DaemonSet":   true,
}

func (r *CarbonRightSizingRecommender) Filter(ctx *framework.RecommendationContext) error {
	kind := ctx.Recommendation.Spec.TargetRef.Kind
	if !acceptedKinds[kind] {
		return fmt.Errorf("CarbonRightSizing recommender does not support resource kind %q; accepted kinds are Deployment, StatefulSet, DaemonSet", kind)
	}

	if err := r.BaseRecommender.Filter(ctx); err != nil {
		return err
	}

	if err := framework.RetrievePodTemplate(ctx); err != nil {
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
