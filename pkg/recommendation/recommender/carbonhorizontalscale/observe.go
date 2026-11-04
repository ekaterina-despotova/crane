package carbonhorizontalscale

import (
	"k8s.io/klog/v2"

	"github.com/gocrane/crane/pkg/recommendation/framework"
)

func (r *CarbonHorizontalScalingRecommender) Observe(ctx *framework.RecommendationContext) error {
	if ctx.Recommendation.Status.Action != "Patch" {
		return nil
	}

	klog.Infof("%s: horizontal scaling recommended for %s/%s: %s",
		r.Name(),
		ctx.Recommendation.Spec.TargetRef.Namespace,
		ctx.Recommendation.Spec.TargetRef.Name,
		ctx.Recommendation.Status.Description)

	return nil
}
