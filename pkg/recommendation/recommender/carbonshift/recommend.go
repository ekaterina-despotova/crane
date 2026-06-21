package carbonshift

import (
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/klog/v2"

	"github.com/gocrane/crane/pkg/common"
	"github.com/gocrane/crane/pkg/recommendation/framework"
)

// PreRecommend is a no-op for CarbonLoadShifting.
func (r *CarbonLoadShiftingRecommender) PreRecommend(ctx *framework.RecommendationContext) error {
	return nil
}

// Recommend analyzes the hourly energy profile and recommends temporal shifting
// if the workload runs primarily during high-carbon hours.
func (r *CarbonLoadShiftingRecommender) Recommend(ctx *framework.RecommendationContext) error {
	profileList := ctx.InputValue(keyHourlyProfile)
	if len(profileList) == 0 || len(profileList[0].Samples) < 24 {
		return fmt.Errorf("no hourly energy profile available for load shifting analysis")
	}

	profile := profileList[0].Samples // 24 samples, one per hour

	// Compute total energy and energy during high-carbon vs low-carbon hours.
	var totalEnergy float64
	var highCarbonEnergy float64
	var lowCarbonEnergy float64
	var peakHours []int

	for _, s := range profile {
		hour := int(s.Timestamp) // We stored hour as timestamp
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
		return fmt.Errorf("workload %s/%s has no measurable energy consumption",
			ctx.Recommendation.Spec.TargetRef.Namespace, ctx.Recommendation.Spec.TargetRef.Name)
	}

	// Check if the workload's average watts are worth shifting.
	avgWatts := totalEnergy / 24.0
	if avgWatts < r.minEnergyWatts {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = fmt.Sprintf(
			"Workload average power %.2fW is below threshold %.2fW, not worth shifting",
			avgWatts, r.minEnergyWatts)
		return nil
	}

	// Compute the fraction of energy consumed during high-carbon hours.
	highCarbonFraction := highCarbonEnergy / totalEnergy

	// If less than 30% of energy is during high-carbon hours, no shift needed.
	if highCarbonFraction < 0.3 {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = fmt.Sprintf(
			"Only %.0f%% of energy consumed during high-carbon hours — shifting not needed",
			highCarbonFraction*100)
		return nil
	}

	// Compute estimated carbon savings from shifting.
	// Current carbon cost: highCarbonEnergy * highCarbonGCO2 + lowCarbonEnergy * lowCarbonGCO2
	// Shifted carbon cost: all energy * lowCarbonGCO2 (if shifted entirely to low-carbon window)
	currentCarbonGrams := highCarbonEnergy*r.highCarbonGCO2 + lowCarbonEnergy*r.lowCarbonGCO2
	shiftedCarbonGrams := totalEnergy * r.lowCarbonGCO2
	savingsGrams := currentCarbonGrams - shiftedCarbonGrams

	if savingsGrams <= 0 {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = "No carbon savings achievable through temporal shifting"
		return nil
	}

	// Build the recommendation.
	ctx.Recommendation.Status.Action = "Patch"
	ctx.Recommendation.Status.Description = fmt.Sprintf(
		"Temporal load shifting recommended: shift workload from high-carbon hours [%s] to low-carbon window [%02d:00-%02d:00]. "+
			"Current high-carbon energy: %.2fWh (%.0f%% of total). "+
			"Estimated carbon savings: %.1f gCO₂/day (from %.1f to %.1f gCO₂/day)",
		formatHours(peakHours),
		r.lowCarbonStartHour, r.lowCarbonEndHour,
		highCarbonEnergy, highCarbonFraction*100,
		savingsGrams, currentCarbonGrams, shiftedCarbonGrams)

	// Generate the shift manifest.
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

	// Current info: the workload's current schedule (if CronJob) or "always running".
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

// Policy is a no-op — manifest generation is done in Recommend.
func (r *CarbonLoadShiftingRecommender) Policy(ctx *framework.RecommendationContext) error {
	return nil
}

// isHighCarbonHour returns true if the given hour is outside the low-carbon window.
func (r *CarbonLoadShiftingRecommender) isHighCarbonHour(hour int) bool {
	return !r.isLowCarbonHour(hour)
}

// isLowCarbonHour returns true if the given hour falls within the low-carbon window.
func (r *CarbonLoadShiftingRecommender) isLowCarbonHour(hour int) bool {
	start := int(r.lowCarbonStartHour)
	end := int(r.lowCarbonEndHour)

	if start <= end {
		// Normal range: e.g., 0-6
		return hour >= start && hour < end
	}
	// Wrapping range: e.g., 22-4 (means 22,23,0,1,2,3)
	return hour >= start || hour < end
}

// formatHours formats a slice of hours as a human-readable range string.
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

// buildEnergyProfileMap converts hourly profile samples to a map for the manifest.
func buildEnergyProfileMap(profile []common.Sample) map[string]float64 {
	result := make(map[string]float64, 24)
	for _, s := range profile {
		hour := int(s.Timestamp)
		result[fmt.Sprintf("%02d:00", hour)] = s.Value
	}
	return result
}

// ShiftManifest represents the recommended temporal shift.
type ShiftManifest struct {
	Recommendation     string             `json:"recommendation"`
	TargetWindow       string             `json:"targetWindow"`
	CurrentPeakHours   string             `json:"currentPeakHours"`
	HighCarbonFraction float64            `json:"highCarbonFraction"`
	EstimatedSavings   CarbonSavings      `json:"estimatedSavings"`
	EnergyProfile      map[string]float64 `json:"energyProfile"`
}

// CarbonSavings holds the estimated carbon reduction from shifting.
type CarbonSavings struct {
	GramsCO2PerDay   float64 `json:"gramsCO2PerDay"`
	CurrentGCO2      float64 `json:"currentGCO2PerDay"`
	ShiftedGCO2      float64 `json:"shiftedGCO2PerDay"`
	ReductionPercent float64 `json:"reductionPercent"`
}

// CurrentScheduleInfo describes the workload's current scheduling state.
type CurrentScheduleInfo struct {
	Kind     string `json:"kind"`
	Schedule string `json:"schedule"`
}
