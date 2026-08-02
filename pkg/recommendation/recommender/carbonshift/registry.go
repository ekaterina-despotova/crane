package carbonshift

import (
	"strings"

	analysisv1alph1 "github.com/gocrane/api/analysis/v1alpha1"
	"github.com/gocrane/crane/pkg/recommendation/config"
	"github.com/gocrane/crane/pkg/recommendation/recommender"
	"github.com/gocrane/crane/pkg/recommendation/recommender/apis"
	"github.com/gocrane/crane/pkg/recommendation/recommender/base"
)

var _ recommender.Recommender = &CarbonTemporalShiftingRecommender{}
var _ recommender.Recommender = &CarbonSpatialShiftingRecommender{}

type CarbonTemporalShiftingRecommender struct {
	base.BaseRecommender
	lowCarbonStartHour int64
	lowCarbonEndHour   int64
	highCarbonGCO2     float64
	lowCarbonGCO2      float64
	observationDays    int64
	minEnergyWatts     float64
}

type CarbonSpatialShiftingRecommender struct {
	base.BaseRecommender
	currentZone     string
	comparisonZones []string
	observationDays int64
	minEnergyWatts  float64
}

func init() {
	recommender.RegisterRecommenderProvider(recommender.CarbonTemporalShiftingRecommender, NewCarbonTemporalShiftingRecommender)
	recommender.RegisterRecommenderProvider(recommender.CarbonSpatialShiftingRecommender, NewCarbonSpatialShiftingRecommender)
}

func (r *CarbonTemporalShiftingRecommender) Name() string {
	return recommender.CarbonTemporalShiftingRecommender
}

func (r *CarbonSpatialShiftingRecommender) Name() string {
	return recommender.CarbonSpatialShiftingRecommender
}

func NewCarbonTemporalShiftingRecommender(rec apis.Recommender, recommendationRule analysisv1alph1.RecommendationRule) (recommender.Recommender, error) {
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

	return &CarbonTemporalShiftingRecommender{
		BaseRecommender:    *base.NewBaseRecommender(rec),
		lowCarbonStartHour: lowCarbonStartHour,
		lowCarbonEndHour:   lowCarbonEndHour,
		highCarbonGCO2:     highCarbonGCO2,
		lowCarbonGCO2:      lowCarbonGCO2,
		observationDays:    observationDays,
		minEnergyWatts:     minEnergyWatts,
	}, nil
}

func NewCarbonSpatialShiftingRecommender(rec apis.Recommender, recommendationRule analysisv1alph1.RecommendationRule) (recommender.Recommender, error) {
	rec = config.MergeRecommenderConfigFromRule(rec, recommendationRule)

	currentZone := rec.GetConfigString("current-zone", "SE")
	comparisonZonesStr := rec.GetConfigString("comparison-zones", "NO,FI,DK,DE,PL,FR,GB")
	observationDays, err := rec.GetConfigInt("observation-window-days", 7)
	if err != nil {
		return nil, err
	}
	minEnergyWatts, err := rec.GetConfigFloat("min-energy-watts", 1.0)
	if err != nil {
		return nil, err
	}

	zones := strings.Split(comparisonZonesStr, ",")
	for i := range zones {
		zones[i] = strings.TrimSpace(zones[i])
	}

	return &CarbonSpatialShiftingRecommender{
		BaseRecommender: *base.NewBaseRecommender(rec),
		currentZone:     currentZone,
		comparisonZones: zones,
		observationDays: observationDays,
		minEnergyWatts:  minEnergyWatts,
	}, nil
}
