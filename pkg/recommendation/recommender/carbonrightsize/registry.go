package carbonrightsize

import (
	analysisv1alph1 "github.com/gocrane/api/analysis/v1alpha1"
	"github.com/gocrane/crane/pkg/recommendation/config"
	"github.com/gocrane/crane/pkg/recommendation/recommender"
	"github.com/gocrane/crane/pkg/recommendation/recommender/apis"
	"github.com/gocrane/crane/pkg/recommendation/recommender/base"
)

var _ recommender.Recommender = &CarbonRightSizingRecommender{}

type CarbonRightSizingRecommender struct {
	base.BaseRecommender
	energyEfficiencyTarget float64
	observationDays        int64
	cpuPercentile          float64
	memoryPercentile       float64
}

func init() {
	recommender.RegisterRecommenderProvider(recommender.CarbonRightSizingRecommender, NewCarbonRightSizingRecommender)
}

func (r *CarbonRightSizingRecommender) Name() string {
	return recommender.CarbonRightSizingRecommender
}

// NewCarbonRightSizingRecommender creates a new CarbonRightSizing recommender.
func NewCarbonRightSizingRecommender(rec apis.Recommender, recommendationRule analysisv1alph1.RecommendationRule) (recommender.Recommender, error) {
	rec = config.MergeRecommenderConfigFromRule(rec, recommendationRule)

	energyEfficiencyTarget, err := rec.GetConfigFloat("energy-efficiency-target", 0.7)
	if err != nil {
		return nil, err
	}

	observationDays, err := rec.GetConfigInt("observation-window-days", 7)
	if err != nil {
		return nil, err
	}

	cpuPercentile, err := rec.GetConfigFloat("cpu-percentile", 0.95)
	if err != nil {
		return nil, err
	}

	memoryPercentile, err := rec.GetConfigFloat("memory-percentile", 0.95)
	if err != nil {
		return nil, err
	}

	return &CarbonRightSizingRecommender{
		BaseRecommender:        *base.NewBaseRecommender(rec),
		energyEfficiencyTarget: energyEfficiencyTarget,
		observationDays:        observationDays,
		cpuPercentile:          cpuPercentile,
		memoryPercentile:       memoryPercentile,
	}, nil
}
