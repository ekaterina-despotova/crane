package carbonidle

import (
	"fmt"

	"github.com/gocrane/crane/pkg/recommendation/framework"
)

// acceptedKinds lists the Kubernetes resource kinds supported by CarbonIdleResource.
var acceptedKinds = map[string]bool{
	"Deployment":  true,
	"StatefulSet": true,
	"DaemonSet":   true,
	"Node":        true,
	"Pod":         true,
}

// Filter checks whether the target resource kind is supported by CarbonIdleResource.
func (r *CarbonIdleResourceRecommender) Filter(ctx *framework.RecommendationContext) error {
	kind := ctx.Recommendation.Spec.TargetRef.Kind
	if !acceptedKinds[kind] {
		return fmt.Errorf("CarbonIdleResource recommender does not support resource kind %q; accepted kinds are Deployment, StatefulSet, DaemonSet, Node, Pod", kind)
	}

	// Delegate base filtering (label selectors, cooldown, deletion check).
	if err := r.BaseRecommender.Filter(ctx); err != nil {
		return err
	}

	// For workload kinds (not Node/Pod) we must retrieve Scale before RetrievePods,
	// because GetPodsFromScale dereferences ctx.Scale.
	if kind != "Node" && kind != "Pod" && kind != "DaemonSet" {
		if err := framework.RetrieveScale(ctx); err != nil {
			return err
		}
	}

	// Retrieve pods for the target resource.
	if err := framework.RetrievePods(ctx); err != nil {
		return err
	}

	return nil
}
