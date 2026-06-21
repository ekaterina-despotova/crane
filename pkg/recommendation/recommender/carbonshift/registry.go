package carbonshift

import (
	analysisv1alph1 "github.com/gocrane/api/analysis/v1alpha1"
	"github.com/gocrane/crane/pkg/recommendation/config"
	"github.com/gocrane/crane/pkg/recommendation/recommender"
	"github.com/gocrane/crane/pkg/recommendation/recommender/apis"
	"github.com/gocrane/crane/pkg/recommendation/recommender/base"
)

var _ recommender.Recommender = &CarbonLoadShiftingRecommender{}

// CarbonLoadShiftingRecommender analyzes workload energy consumption patterns
// and recommends temporal shifting to low-carbon-intensity time windows.
type CarbonLoadShiftingRecommender struct {
	base.BaseRecommender
	lowCarbonStartHour int64   // Hour (0-23) when low-carbon window starts
	lowCarbonEndHour   int64   // Hour (0-23) when low-carbon window ends
	highCarbonGCO2     float64 // gCO2/kWh threshold above which a time period is "high carbon"
	lowCarbonGCO2      float64 // gCO2/kWh during the low-carbon window
	observationDays    int64   // Days of historical data to analyze
	minEnergyWatts     float64 // Minimum average watts for a workload to be worth shifting
}

func init() {
	recommender.RegisterRecommenderProvider(recommender.CarbonLoadShiftingRecommender, NewCarbonLoadShiftingRecommender)
}

func (r *CarbonLoadShiftingRecommender) Name() string {
	return recommender.CarbonLoadShiftingRecommender
}

// NewCarbonLoadShiftingRecommender creates a new CarbonLoadShifting recommender.
func NewCarbonLoadShiftingRecommender(rec apis.Recommender, recommendationRule analysisv1alph1.RecommendationRule) (recommender.Recommender, error) {
	rec = config.MergeRecommenderConfigFromRule(rec, recommendationRule)

	lowCarbonStartHour, err := rec.GetConfigInt("low-carbon-start-hour", 0)
	if err != nil {
		return nil, err
	}

	lowCarbonEndHour, err := rec.GetConfigInt("low-carbon-end-hour", 6)
	if err != nil {
		return nil, err
	}

	highCarbonGCO2, err := rec.GetConfigFloat("high-carbon-gco2", 300.0)
	if err != nil {
		return nil, err
	}

	lowCarbonGCO2, err := rec.GetConfigFloat("low-carbon-gco2", 120.0)
	if err != nil {
		return nil, err
	}

	observationDays, err := rec.GetConfigInt("observation-window-days", 7)
	if err != nil {
		return nil, err
	}

	minEnergyWatts, err := rec.GetConfigFloat("min-energy-watts", 1.0)
	if err != nil {
		return nil, err
	}

	return &CarbonLoadShiftingRecommender{
		BaseRecommender:    *base.NewBaseRecommender(rec),
		lowCarbonStartHour: lowCarbonStartHour,
		lowCarbonEndHour:   lowCarbonEndHour,
		highCarbonGCO2:     highCarbonGCO2,
		lowCarbonGCO2:      lowCarbonGCO2,
		observationDays:    observationDays,
		minEnergyWatts:     minEnergyWatts,
	}, nil
}
