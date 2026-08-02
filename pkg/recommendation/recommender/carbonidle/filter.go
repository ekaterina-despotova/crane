package carbonidle

import (
	"fmt"

	"github.com/gocrane/crane/pkg/recommendation/framework"
)

var acceptedKinds = map[string]bool{
	"Deployment":  true,
	"StatefulSet": true,
	"DaemonSet":   true,
	"Node":        true,
	"Pod":         true,
}

func (r *CarbonIdleResourceRecommender) Filter(ctx *framework.RecommendationContext) error {
	kind := ctx.Recommendation.Spec.TargetRef.Kind
	if !acceptedKinds[kind] {
		return fmt.Errorf("CarbonIdleResource recommender does not support resource kind %q; accepted kinds are Deployment, StatefulSet, DaemonSet, Node, Pod", kind)
	}

	if err := r.BaseRecommender.Filter(ctx); err != nil {
		return err
	}

	switch kind {
	case "Node", "DaemonSet":
		if err := framework.RetrievePods(ctx); err != nil {
			return err
		}
	case "Pod":
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
