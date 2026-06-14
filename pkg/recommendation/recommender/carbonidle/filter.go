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

	// RetrievePods dispatches based on Kind. For Deployment/StatefulSet it calls
	// GetPodsFromScale which dereferences ctx.Scale — so we must populate Scale first.
	// For Node/DaemonSet, RetrievePods has its own path that doesn't need Scale.
	// For Pod, RetrievePods falls into the "else" path (GetPodsFromScale) which panics
	// on nil Scale — so we skip RetrievePods entirely for Pod targets.
	switch kind {
	case "Node", "DaemonSet":
		// These have dedicated paths in RetrievePods that don't need Scale.
		if err := framework.RetrievePods(ctx); err != nil {
			return err
		}
	case "Pod":
		// Standalone Pod: RetrievePods would panic (nil Scale in GetPodsFromScale).
		// The pod info comes from TargetRef; CollectData queries by pod name directly.
		// Nothing to do here.
	default:
		// Deployment/StatefulSet: need Scale populated before RetrievePods.
		if err := framework.RetrieveScale(ctx); err != nil {
			return err
		}
		if err := framework.RetrievePods(ctx); err != nil {
			return err
		}
	}

	return nil
}
