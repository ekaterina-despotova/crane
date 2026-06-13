package carbonidle

import (
	analysisv1alph1 "github.com/gocrane/api/analysis/v1alpha1"
	"github.com/gocrane/crane/pkg/recommendation/config"
	"github.com/gocrane/crane/pkg/recommendation/recommender"
	"github.com/gocrane/crane/pkg/recommendation/recommender/apis"
	"github.com/gocrane/crane/pkg/recommendation/recommender/base"
)

var _ recommender.Recommender = &CarbonIdleResourceRecommender{}

type CarbonIdleResourceRecommender struct {
	base.BaseRecommender
	energyIdleThreshold float64
	observationDays     int64
	minEnergyWatts      float64
	cpuUsageThreshold   float64
}

func init() {
	recommender.RegisterRecommenderProvider(recommender.CarbonIdleResourceRecommender, NewCarbonIdleResourceRecommender)
}

func (r *CarbonIdleResourceRecommender) Name() string {
	return recommender.CarbonIdleResourceRecommender
}

// NewCarbonIdleResourceRecommender creates a new CarbonIdleResource recommender.
func NewCarbonIdleResourceRecommender(rec apis.Recommender, recommendationRule analysisv1alph1.RecommendationRule) (recommender.Recommender, error) {
	rec = config.MergeRecommenderConfigFromRule(rec, recommendationRule)

	energyIdleThreshold, err := rec.GetConfigFloat("energy-idle-threshold", 0.8)
	if err != nil {
		return nil, err
	}

	observationDays, err := rec.GetConfigInt("observation-window-days", 7)
	if err != nil {
		return nil, err
	}

	minEnergyWatts, err := rec.GetConfigFloat("min-energy-watts", 0.5)
	if err != nil {
		return nil, err
	}

	cpuUsageThreshold, err := rec.GetConfigFloat("cpu-usage-threshold", 0.05)
	if err != nil {
		return nil, err
	}

	return &CarbonIdleResourceRecommender{
		BaseRecommender:     *base.NewBaseRecommender(rec),
		energyIdleThreshold: energyIdleThreshold,
		observationDays:     observationDays,
		minEnergyWatts:      minEnergyWatts,
		cpuUsageThreshold:   cpuUsageThreshold,
	}, nil
}
