package carbonshift

import (
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/klog/v2"

	"github.com/gocrane/crane/pkg/common"
	"github.com/gocrane/crane/pkg/recommendation/framework"
)

func (r *CarbonLoadShiftingRecommender) PreRecommend(ctx *framework.RecommendationContext) error {
	return nil
}

func (r *CarbonLoadShiftingRecommender) Recommend(ctx *framework.RecommendationContext) error {
	profileList := ctx.InputValue(keyHourlyProfile)
	if len(profileList) == 0 || len(profileList[0].Samples) < 24 {
		return fmt.Errorf("no hourly energy profile available for load shifting analysis")
	}

	profile := profileList[0].Samples

	var totalEnergy float64
	var highCarbonEnergy float64
	var lowCarbonEnergy float64
	var peakHours []int

	for _, s := range profile {
		hour := int(s.Timestamp)
		watts := s.Value
		totalEnergy += watts

		if r.isHighCarbonHour(hour) && watts > 0 {
			highCarbonEnergy += watts
			if watts > r.minEnergyWatts {
				peakHours = append(peakHours, hour)
			}
		}
		if r.isLowCarbonHour(hour) {
			lowCarbonEnergy += watts
		}
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

	highCarbonFraction := highCarbonEnergy / totalEnergy

	if highCarbonFraction < 0.3 {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = fmt.Sprintf(
			"Only %.0f%% of energy consumed during high-carbon hours — shifting not needed",
			highCarbonFraction*100)
		return nil
	}

	currentCarbonGrams := highCarbonEnergy*r.highCarbonGCO2 + lowCarbonEnergy*r.lowCarbonGCO2
	shiftedCarbonGrams := totalEnergy * r.lowCarbonGCO2
	savingsGrams := currentCarbonGrams - shiftedCarbonGrams

	if savingsGrams <= 0 {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = "No carbon savings achievable through temporal shifting"
		return nil
	}

	ctx.Recommendation.Status.Action = "Patch"
	ctx.Recommendation.Status.Description = fmt.Sprintf(
		"Temporal load shifting recommended: shift workload from high-carbon hours [%s] to low-carbon window [%02d:00-%02d:00]. "+
			"Current high-carbon energy: %.2fWh (%.0f%% of total). "+
			"Estimated carbon savings: %.1f gCO₂/day (from %.1f to %.1f gCO₂/day)",
		formatHours(peakHours),
		r.lowCarbonStartHour, r.lowCarbonEndHour,
		highCarbonEnergy, highCarbonFraction*100,
		savingsGrams, currentCarbonGrams, shiftedCarbonGrams)

	manifest := ShiftManifest{
		Recommendation:    "TemporalShift",
		TargetWindow:      fmt.Sprintf("%02d:00-%02d:00 UTC", r.lowCarbonStartHour, r.lowCarbonEndHour),
		CurrentPeakHours:  formatHours(peakHours),
		HighCarbonFraction: highCarbonFraction,
		EstimatedSavings: CarbonSavings{
			GramsCO2PerDay:   savingsGrams,
			CurrentGCO2:      currentCarbonGrams,
			ShiftedGCO2:      shiftedCarbonGrams,
			ReductionPercent: (savingsGrams / currentCarbonGrams) * 100,
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

	klog.Infof("%s: recommending temporal shift for %s/%s, savings: %.1f gCO₂/day",
		r.Name(), ctx.Recommendation.Spec.TargetRef.Namespace,
		ctx.Recommendation.Spec.TargetRef.Name, savingsGrams)

	return nil
}

func (r *CarbonLoadShiftingRecommender) Policy(ctx *framework.RecommendationContext) error {
	return nil
}

func (r *CarbonLoadShiftingRecommender) isHighCarbonHour(hour int) bool {
	return !r.isLowCarbonHour(hour)
}

func (r *CarbonLoadShiftingRecommender) isLowCarbonHour(hour int) bool {
	start := int(r.lowCarbonStartHour)
	end := int(r.lowCarbonEndHour)

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
	EstimatedSavings   CarbonSavings      `json:"estimatedSavings"`
	EnergyProfile      map[string]float64 `json:"energyProfile"`
}

type CarbonSavings struct {
	GramsCO2PerDay   float64 `json:"gramsCO2PerDay"`
	CurrentGCO2      float64 `json:"currentGCO2PerDay"`
	ShiftedGCO2      float64 `json:"shiftedGCO2PerDay"`
	ReductionPercent float64 `json:"reductionPercent"`
}

type CurrentScheduleInfo struct {
	Kind     string `json:"kind"`
	Schedule string `json:"schedule"`
}
