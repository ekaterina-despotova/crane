package carbonshift

import (
	"encoding/json"
	"fmt"
	"sort"

	"k8s.io/klog/v2"

	"github.com/gocrane/crane/pkg/recommendation/framework"
)

func (r *CarbonSpatialShiftingRecommender) PreRecommend(ctx *framework.RecommendationContext) error {
	return nil
}

func (r *CarbonSpatialShiftingRecommender) Recommend(ctx *framework.RecommendationContext) error {
	profileList := ctx.InputValue(keyHourlyProfile)
	if len(profileList) == 0 || len(profileList[0].Samples) < 24 {
		return fmt.Errorf("no hourly energy profile available for spatial shifting analysis")
	}

	var totalEnergy float64
	for _, s := range profileList[0].Samples {
		totalEnergy += s.Value
	}

	if totalEnergy <= 0 {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = fmt.Sprintf(
			"Workload %s/%s has no measurable energy consumption — spatial analysis skipped",
			ctx.Recommendation.Spec.TargetRef.Namespace, ctx.Recommendation.Spec.TargetRef.Name)
		return nil
	}

	avgWatts := totalEnergy / 24.0
	if avgWatts < r.minEnergyWatts {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = fmt.Sprintf(
			"Workload average power %.2fW is below threshold %.2fW, not worth spatial analysis",
			avgWatts, r.minEnergyWatts)
		return nil
	}

	allZones := append([]string{r.currentZone}, r.comparisonZones...)
	zoneIntensities := GetMultiZoneCarbonIntensity(allZones)

	if zoneIntensities == nil || len(zoneIntensities) == 0 {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = "Spatial shifting analysis unavailable: Electricity Maps API key not set or API unreachable"
		return nil
	}

	currentIntensity, ok := zoneIntensities[r.currentZone]
	if !ok {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = fmt.Sprintf(
			"No carbon intensity data available for current zone %s", r.currentZone)
		return nil
	}

	type ZoneRanking struct {
		Zone      string
		Intensity float64
		Savings   float64
	}

	var rankings []ZoneRanking
	for zone, intensity := range zoneIntensities {
		dailyEnergyKWh := (totalEnergy * 24) / 1000.0
		savings := (currentIntensity - intensity) * dailyEnergyKWh
		rankings = append(rankings, ZoneRanking{
			Zone:      zone,
			Intensity: intensity,
			Savings:   savings,
		})
	}

	sort.Slice(rankings, func(i, j int) bool {
		return rankings[i].Intensity < rankings[j].Intensity
	})

	bestZone := rankings[0]
	if bestZone.Zone == r.currentZone || bestZone.Savings <= 0 {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = fmt.Sprintf(
			"Current zone %s (%.1f gCO2/kWh) is already the greenest among configured zones. No spatial shift needed.",
			r.currentZone, currentIntensity)
		return nil
	}

	ctx.Recommendation.Status.Action = "Patch"
	ctx.Recommendation.Status.Description = fmt.Sprintf(
		"Spatial load shifting recommended: migrate workload from %s (%.1f gCO2/kWh) to %s (%.1f gCO2/kWh). "+
			"Estimated carbon savings: %.1f gCO2/day (%.0f%% reduction). "+
			"Data source: Electricity Maps API.",
		r.currentZone, currentIntensity,
		bestZone.Zone, bestZone.Intensity,
		bestZone.Savings, (bestZone.Savings/(currentIntensity*(totalEnergy*24)/1000.0))*100)

	manifest := SpatialShiftManifest{
		Recommendation:   "SpatialShift",
		CurrentZone:      r.currentZone,
		CurrentIntensity: currentIntensity,
		BestZone:         bestZone.Zone,
		BestIntensity:    bestZone.Intensity,
		EstimatedSavings: bestZone.Savings,
		ReductionPercent: (bestZone.Savings / (currentIntensity * (totalEnergy * 24) / 1000.0)) * 100,
		ZoneRankings:     make([]ZoneRankingEntry, 0, len(rankings)),
		DataSource:       "Electricity Maps API",
	}

	for _, z := range rankings {
		manifest.ZoneRankings = append(manifest.ZoneRankings, ZoneRankingEntry{
			Zone:      z.Zone,
			Intensity: z.Intensity,
			Savings:   z.Savings,
		})
	}

	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("failed to encode spatial shift manifest: %v", err)
	}
	ctx.Recommendation.Status.RecommendedInfo = string(manifestBytes)
	ctx.Recommendation.Status.CurrentInfo = fmt.Sprintf(`{"currentZone":"%s","intensity":%.1f}`, r.currentZone, currentIntensity)

	klog.Infof("%s: recommending spatial shift for %s/%s from %s to %s, savings: %.1f gCO2/day",
		r.Name(), ctx.Recommendation.Spec.TargetRef.Namespace,
		ctx.Recommendation.Spec.TargetRef.Name, r.currentZone, bestZone.Zone, bestZone.Savings)

	return nil
}

func (r *CarbonSpatialShiftingRecommender) Policy(ctx *framework.RecommendationContext) error {
	return nil
}

type SpatialShiftManifest struct {
	Recommendation   string             `json:"recommendation"`
	CurrentZone      string             `json:"currentZone"`
	CurrentIntensity float64            `json:"currentIntensity_gCO2_kWh"`
	BestZone         string             `json:"bestZone"`
	BestIntensity    float64            `json:"bestIntensity_gCO2_kWh"`
	EstimatedSavings float64            `json:"estimatedSavings_gCO2_day"`
	ReductionPercent float64            `json:"reductionPercent"`
	ZoneRankings     []ZoneRankingEntry `json:"zoneRankings"`
	DataSource       string             `json:"dataSource"`
}

type ZoneRankingEntry struct {
	Zone      string  `json:"zone"`
	Intensity float64 `json:"intensity_gCO2_kWh"`
	Savings   float64 `json:"savings_gCO2_day"`
}
