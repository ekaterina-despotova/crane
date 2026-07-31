package carbonshift

import (
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/klog/v2"

	"github.com/gocrane/crane/pkg/common"
	"github.com/gocrane/crane/pkg/recommendation/framework"
)

func (r *CarbonTemporalShiftingRecommender) PreRecommend(ctx *framework.RecommendationContext) error {
	return nil
}

func (r *CarbonTemporalShiftingRecommender) Recommend(ctx *framework.RecommendationContext) error {
	profileList := ctx.InputValue(keyHourlyProfile)
	if len(profileList) == 0 || len(profileList[0].Samples) < 24 {
		return fmt.Errorf("no hourly energy profile available for load shifting analysis")
	}

	profile := profileList[0].Samples // 24 samples, one per hour

	var totalEnergy float64
	for _, s := range profile {
		totalEnergy += s.Value
	}

	if totalEnergy <= 0 {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = fmt.Sprintf(
			"Workload %s/%s has no measurable energy consumption — nothing to shift",
			ctx.Recommendation.Spec.TargetRef.Namespace, ctx.Recommendation.Spec.TargetRef.Name)
		return nil
	}

	avgWatts := totalEnergy / 24.0
	if avgWatts < r.minEnergyWatts {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = fmt.Sprintf(
			"Workload average power %.2fW is below threshold %.2fW, not worth shifting",
			avgWatts, r.minEnergyWatts)
		return nil
	}

	carbonData := GetHourlyCarbonIntensity()

	var lowCarbonStart, lowCarbonEnd int
	var highCarbonGCO2, lowCarbonGCO2 float64
	var dataSource string

	if carbonData != nil {
		optStart, optAvg := carbonData.FindOptimalWindow(6)
		_, peakAvg := carbonData.FindPeakWindow(6)
		lowCarbonStart = optStart
		lowCarbonEnd = (optStart + 6) % 24
		lowCarbonGCO2 = optAvg
		highCarbonGCO2 = peakAvg
		dataSource = fmt.Sprintf("Electricity Maps API (zone: %s, fetched: %s)",
			carbonData.Zone, carbonData.FetchedAt.Format("15:04 UTC"))
	} else {
		lowCarbonStart = int(r.lowCarbonStartHour)
		lowCarbonEnd = int(r.lowCarbonEndHour)
		highCarbonGCO2 = r.highCarbonGCO2
		lowCarbonGCO2 = r.lowCarbonGCO2
		dataSource = "static configuration"
	}

	var highCarbonEnergy float64
	var peakHours []int

	for _, s := range profile {
		hour := int(s.Timestamp)
		watts := s.Value
		if !isInWindow(hour, lowCarbonStart, lowCarbonEnd) && watts > 0 {
			highCarbonEnergy += watts
			if watts > r.minEnergyWatts {
				peakHours = append(peakHours, hour)
			}
		}
	}

	highCarbonFraction := highCarbonEnergy / totalEnergy

	if highCarbonFraction < 0.3 {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = fmt.Sprintf(
			"Only %.0f%% of energy consumed during high-carbon hours — shifting not needed (data: %s)",
			highCarbonFraction*100, dataSource)
		return nil
	}

	currentCarbonGrams := highCarbonEnergy*highCarbonGCO2 + (totalEnergy-highCarbonEnergy)*lowCarbonGCO2
	shiftedCarbonGrams := totalEnergy * lowCarbonGCO2
	savingsGrams := currentCarbonGrams - shiftedCarbonGrams

	if savingsGrams <= 0 {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = fmt.Sprintf(
			"No carbon savings achievable through temporal shifting (data: %s)", dataSource)
		return nil
	}

	ctx.Recommendation.Status.Action = "Patch"
	ctx.Recommendation.Status.Description = fmt.Sprintf(
		"Temporal load shifting recommended: shift workload from high-carbon hours [%s] to low-carbon window [%02d:00-%02d:00 UTC]. "+
			"High-carbon intensity: %.1f gCO₂/kWh, low-carbon intensity: %.1f gCO₂/kWh. "+
			"Current high-carbon energy: %.2fWh (%.0f%% of total). "+
			"Estimated carbon savings: %.1f gCO₂/day (from %.1f to %.1f gCO₂/day). Data source: %s",
		formatHours(peakHours),
		lowCarbonStart, lowCarbonEnd,
		highCarbonGCO2, lowCarbonGCO2,
		highCarbonEnergy, highCarbonFraction*100,
		savingsGrams, currentCarbonGrams, shiftedCarbonGrams,
		dataSource)

	manifest := ShiftManifest{
		Recommendation:     "TemporalShift",
		TargetWindow:       fmt.Sprintf("%02d:00-%02d:00 UTC", lowCarbonStart, lowCarbonEnd),
		CurrentPeakHours:   formatHours(peakHours),
		HighCarbonFraction: highCarbonFraction,
		DataSource:         dataSource,
		EstimatedSavings: CarbonSavings{
			GramsCO2PerDay:      savingsGrams,
			CurrentGCO2:         currentCarbonGrams,
			ShiftedGCO2:         shiftedCarbonGrams,
			ReductionPercent:    (savingsGrams / currentCarbonGrams) * 100,
			HighCarbonIntensity: highCarbonGCO2,
			LowCarbonIntensity:  lowCarbonGCO2,
		},
		EnergyProfile: buildEnergyProfileMap(profile),
	}

	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("failed to encode shift manifest: %v", err)
	}

	ctx.Recommendation.Status.RecommendedInfo = string(manifestBytes)

	currentInfo := CurrentScheduleInfo{
		Kind:     ctx.Recommendation.Spec.TargetRef.Kind,
		Schedule: "always-running",
	}
	currentBytes, err := json.Marshal(currentInfo)
	if err != nil {
		return fmt.Errorf("failed to encode current info: %v", err)
	}
	ctx.Recommendation.Status.CurrentInfo = string(currentBytes)

	klog.Infof("%s: recommending temporal shift for %s/%s, savings: %.1f gCO₂/day (source: %s)",
		r.Name(), ctx.Recommendation.Spec.TargetRef.Namespace,
		ctx.Recommendation.Spec.TargetRef.Name, savingsGrams, dataSource)

	return nil
}

func (r *CarbonTemporalShiftingRecommender) Policy(ctx *framework.RecommendationContext) error {
	return nil
}

func isInWindow(hour, start, end int) bool {
	if start <= end {
		return hour >= start && hour < end
	}
	return hour >= start || hour < end
}

func formatHours(hours []int) string {
	if len(hours) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(hours))
	for _, h := range hours {
		parts = append(parts, fmt.Sprintf("%02d:00", h))
	}
	return strings.Join(parts, ", ")
}

func buildEnergyProfileMap(profile []common.Sample) map[string]float64 {
	result := make(map[string]float64, 24)
	for _, s := range profile {
		hour := int(s.Timestamp)
		result[fmt.Sprintf("%02d:00", hour)] = s.Value
	}
	return result
}

type ShiftManifest struct {
	Recommendation     string             `json:"recommendation"`
	TargetWindow       string             `json:"targetWindow"`
	CurrentPeakHours   string             `json:"currentPeakHours"`
	HighCarbonFraction float64            `json:"highCarbonFraction"`
	DataSource         string             `json:"dataSource"`
	EstimatedSavings   CarbonSavings      `json:"estimatedSavings"`
	EnergyProfile      map[string]float64 `json:"energyProfile"`
}

type CarbonSavings struct {
	GramsCO2PerDay      float64 `json:"gramsCO2PerDay"`
	CurrentGCO2         float64 `json:"currentGCO2PerDay"`
	ShiftedGCO2         float64 `json:"shiftedGCO2PerDay"`
	ReductionPercent    float64 `json:"reductionPercent"`
	HighCarbonIntensity float64 `json:"highCarbonIntensity_gCO2_kWh"`
	LowCarbonIntensity  float64 `json:"lowCarbonIntensity_gCO2_kWh"`
}

type CurrentScheduleInfo struct {
	Kind     string `json:"kind"`
	Schedule string `json:"schedule"`
}
