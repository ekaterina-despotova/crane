package carbonidle

import (
	"strings"

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

	cpuCoresThreshold  float64 
	netBytesThreshold  float64 

	sustainedIdleFraction float64
	protectedLabel        string
	protectedNamespaces   []string

	enableConsolidation bool
	headroomFraction    float64
	minNodes            int64
}

func init() {
	recommender.RegisterRecommenderProvider(recommender.CarbonIdleResourceRecommender, NewCarbonIdleResourceRecommender)
}

func (r *CarbonIdleResourceRecommender) Name() string {
	return recommender.CarbonIdleResourceRecommender
}

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

	sustainedIdleFraction, err := rec.GetConfigFloat("sustained-idle-fraction", 0.95)
	if err != nil {
		return nil, err
	}

	cpuCoresThreshold, err := rec.GetConfigFloat("cpu-cores-threshold", 0.075)
	if err != nil {
		return nil, err
	}

	netBytesThreshold, err := rec.GetConfigFloat("network-bytes-threshold", 1000)
	if err != nil {
		return nil, err
	}

	enableConsolidation, err := rec.GetConfigBool("enable-consolidation", true)
	if err != nil {
		return nil, err
	}

	headroomFraction, err := rec.GetConfigFloat("headroom-fraction", defaultHeadroomFraction)
	if err != nil {
		return nil, err
	}

	minNodes, err := rec.GetConfigInt("min-nodes", 1)
	if err != nil {
		return nil, err
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

	return &CarbonIdleResourceRecommender{
		BaseRecommender:       *base.NewBaseRecommender(rec),
		energyIdleThreshold:   energyIdleThreshold,
		observationDays:       observationDays,
		minEnergyWatts:        minEnergyWatts,
		cpuUsageThreshold:     cpuUsageThreshold,
		cpuCoresThreshold:     cpuCoresThreshold,
		netBytesThreshold:     netBytesThreshold,
		sustainedIdleFraction: sustainedIdleFraction,
		protectedLabel:        protectedLabel,
		protectedNamespaces:   protectedNamespaces,
		enableConsolidation:   enableConsolidation,
		headroomFraction:      headroomFraction,
		minNodes:              minNodes,
	}, nil
}
