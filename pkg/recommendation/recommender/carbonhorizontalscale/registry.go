package carbonhorizontalscale

import (
	"strings"

	analysisv1alph1 "github.com/gocrane/api/analysis/v1alpha1"
	"github.com/gocrane/crane/pkg/recommendation/config"
	"github.com/gocrane/crane/pkg/recommendation/recommender"
	"github.com/gocrane/crane/pkg/recommendation/recommender/apis"
	"github.com/gocrane/crane/pkg/recommendation/recommender/base"
)

var _ recommender.Recommender = &CarbonHorizontalScalingRecommender{}

type CarbonHorizontalScalingRecommender struct {
	base.BaseRecommender
	observationDays    int64
	cpuPercentile      float64
	replicaTargetCores float64
	minReplicas        int32

	protectedLabel      string
	protectedNamespaces []string
}

func init() {
	recommender.RegisterRecommenderProvider(recommender.CarbonHorizontalScalingRecommender, NewCarbonHorizontalScalingRecommender)
}

func (r *CarbonHorizontalScalingRecommender) Name() string {
	return recommender.CarbonHorizontalScalingRecommender
}

func NewCarbonHorizontalScalingRecommender(rec apis.Recommender, recommendationRule analysisv1alph1.RecommendationRule) (recommender.Recommender, error) {
	rec = config.MergeRecommenderConfigFromRule(rec, recommendationRule)

	observationDays, err := rec.GetConfigInt("observation-window-days", 7)
	if err != nil {
		return nil, err
	}

	cpuPercentile, err := rec.GetConfigFloat("cpu-percentile", 0.95)
	if err != nil {
		return nil, err
	}

	replicaTargetCores, err := rec.GetConfigFloat("replica-target-cores", 0.5)
	if err != nil {
		return nil, err
	}

	minReplicas, err := rec.GetConfigInt("min-replicas", 1)
	if err != nil {
		return nil, err
	}
	if minReplicas < 1 {
		minReplicas = 1
	}

	protectedLabel := rec.GetConfigString("protected-label", "carbon.crane.io/protected")

	var protectedNamespaces []string
	if raw := rec.GetConfigString("protected-namespaces", "kube-system"); raw != "" {
		for _, ns := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(ns); trimmed != "" {
				protectedNamespaces = append(protectedNamespaces, trimmed)
			}
		}
	}

	return &CarbonHorizontalScalingRecommender{
		BaseRecommender:     *base.NewBaseRecommender(rec),
		observationDays:     observationDays,
		cpuPercentile:       cpuPercentile,
		replicaTargetCores:  replicaTargetCores,
		minReplicas:         int32(minReplicas),
		protectedLabel:      protectedLabel,
		protectedNamespaces: protectedNamespaces,
	}, nil
}
