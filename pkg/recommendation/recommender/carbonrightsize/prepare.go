package carbonrightsize

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	"github.com/gocrane/crane/pkg/common"
	"github.com/gocrane/crane/pkg/metricnaming"
	"github.com/gocrane/crane/pkg/providers"
	"github.com/gocrane/crane/pkg/recommendation/framework"
	"github.com/gocrane/crane/pkg/utils"
)

const callerFormat = "CarbonRightSizingRecommender-%s-%s"

// Input value keys for RecommendationContext.
const (
	keyPodCPUJoules     = "kepler-pod-cpu-joules"
	keyPodCPUWatts      = "kepler-pod-cpu-watts"
	keyPodGPUWatts      = "kepler-pod-gpu-watts"
	keyCPUUsage         = "cpu-usage"
	keyMemUsage         = "mem-usage"
	keyEnergyEfficiency = "kepler-energy-efficiency-ratio"
)

// Kepler metric availability check — kepler_container_package_joules_total is the
// primary counter exported by this Kepler version.
const keplerAvailabilityExpr = `kepler_container_package_joules_total`

// Kepler PromQL expression templates for pod-level metrics.
// Labels: container_namespace, pod_name, mode. rate() gives watts.
const (
	keplerPodCPUJoulesExpr = `sum by (pod_name, container_namespace) (kepler_container_package_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"})`
	keplerPodCPUWattsExpr  = `sum by (pod_name, container_namespace) (rate(kepler_container_package_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"}[5m]))`
	keplerPodGPUWattsExpr  = `sum by (pod_name, container_namespace) (rate(kepler_container_other_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"}[5m]))`
)

// expectedMetricKeys lists the metrics CarbonRightSizing expects to collect.
var expectedMetricKeys = []string{
	keyPodCPUJoules, keyPodCPUWatts, keyPodGPUWatts,
	keyCPUUsage, keyMemUsage,
}

// CheckDataProviders verifies that Kepler metrics are available in Prometheus.
// Returns an error if Kepler is not installed or not exporting metrics.
func (r *CarbonRightSizingRecommender) CheckDataProviders(ctx *framework.RecommendationContext) error {
	if err := r.BaseRecommender.CheckDataProviders(ctx); err != nil {
		return err
	}

	// Verify Kepler metrics exist by querying kepler_container_package_joules_total.
	caller := fmt.Sprintf(callerFormat, klog.KObj(ctx.Recommendation), ctx.Recommendation.UID)
	metricNamer := metricnaming.ResourceToGeneralMetricNamer(
		keplerAvailabilityExpr,
		corev1.ResourceCPU,
		labels.Everything(),
		caller,
	)
	if err := metricNamer.Validate(); err != nil {
		return fmt.Errorf("Kepler availability metric validation failed: %v", err)
	}

	now := time.Now()
	tsList, err := ctx.DataProviders[providers.PrometheusDataSource].QueryTimeSeries(
		metricNamer, now.Add(-time.Hour), now, time.Minute,
	)
	if err != nil {
		return fmt.Errorf("Prometheus connection failed: %v", err)
	}
	if len(tsList) == 0 {
		return fmt.Errorf("Kepler metrics not available: kepler_container_package_joules_total not found. Ensure Kepler is installed and exporting to Prometheus.")
	}

	return nil
}

// CollectData queries Kepler energy metrics and CPU/memory utilization from Prometheus.
func (r *CarbonRightSizingRecommender) CollectData(ctx *framework.RecommendationContext) error {
	caller := fmt.Sprintf(callerFormat, klog.KObj(ctx.Recommendation), ctx.Recommendation.UID)
	now := time.Now()
	start := now.Add(-time.Hour * 24 * time.Duration(r.observationDays))
	step := time.Minute
	ns := ctx.Recommendation.Spec.TargetRef.Namespace
	kind := ctx.Recommendation.Spec.TargetRef.Kind
	name := ctx.Recommendation.Spec.TargetRef.Name

	// Build pod name regex from retrieved pods.
	podNameRegex := buildPodNameRegex(ctx)
	if podNameRegex == "" {
		return fmt.Errorf("no pods found matching selector for %s/%s", ns, name)
	}

	// Collect pod-level Kepler energy metrics.
	r.collectPodEnergyMetrics(ctx, caller, ns, podNameRegex, start, now, step)

	// Collect CPU/memory utilization metrics using Crane's standard expressions.
	r.collectUtilizationMetrics(ctx, caller, ns, name, kind, start, now, step)

	return nil
}

// buildPodNameRegex constructs a regex matching all pod names from the context.
func buildPodNameRegex(ctx *framework.RecommendationContext) string {
	if len(ctx.Pods) == 0 {
		return ""
	}
	result := ""
	for i, pod := range ctx.Pods {
		if i > 0 {
			result += "|"
		}
		result += pod.Name
	}
	return result
}

// collectPodEnergyMetrics queries pod-level Kepler energy metrics.
func (r *CarbonRightSizingRecommender) collectPodEnergyMetrics(
	ctx *framework.RecommendationContext,
	caller, namespace, podNameRegex string,
	start, end time.Time, step time.Duration,
) {
	podMetrics := []struct {
		key  string
		expr string
	}{
		{keyPodCPUJoules, fmt.Sprintf(keplerPodCPUJoulesExpr, namespace, podNameRegex)},
		{keyPodCPUWatts, fmt.Sprintf(keplerPodCPUWattsExpr, namespace, podNameRegex)},
		{keyPodGPUWatts, fmt.Sprintf(keplerPodGPUWattsExpr, namespace, podNameRegex)},
	}

	for _, m := range podMetrics {
		r.queryAndStore(ctx, caller, m.key, m.expr, start, end, step)
	}
}

// collectUtilizationMetrics queries standard CPU/memory utilization metrics.
func (r *CarbonRightSizingRecommender) collectUtilizationMetrics(
	ctx *framework.RecommendationContext,
	caller, namespace, name, kind string,
	start, end time.Time, step time.Duration,
) {
	cpuExpr := utils.GetWorkloadCpuUsageExpression(namespace, name, kind)
	r.queryAndStore(ctx, caller, keyCPUUsage, cpuExpr, start, end, step)

	memExpr := utils.GetWorkloadMemUsageExpression(namespace, name, kind)
	r.queryAndStore(ctx, caller, keyMemUsage, memExpr, start, end, step)
}

// queryAndStore queries a single metric and stores the result in the context.
// Errors are logged as warnings and the metric is skipped.
func (r *CarbonRightSizingRecommender) queryAndStore(
	ctx *framework.RecommendationContext,
	caller, key, expr string,
	start, end time.Time, step time.Duration,
) {
	metricNamer := metricnaming.ResourceToGeneralMetricNamer(
		expr,
		corev1.ResourceCPU,
		labels.Everything(),
		caller,
	)
	if err := metricNamer.Validate(); err != nil {
		klog.Warningf("%s: failed to validate metric namer for %s: %v", r.Name(), key, err)
		return
	}

	klog.Infof("%s: %s query %s", ctx.String(), r.Name(), key)
	tsList, err := ctx.DataProviders[providers.PrometheusDataSource].QueryTimeSeries(metricNamer, start, end, step)
	if err != nil {
		klog.Warningf("%s: failed to query %s: %v", r.Name(), key, err)
		return
	}
	if len(tsList) == 0 {
		klog.Warningf("%s: no data returned for %s, excluding from analysis", r.Name(), key)
		return
	}

	ctx.AddInputValue(key, tsList)
}

// PostProcessing computes the energy-efficiency ratio for each pod and validates
// that sufficient metrics are available.
func (r *CarbonRightSizingRecommender) PostProcessing(ctx *framework.RecommendationContext) error {
	// Compute energy-efficiency ratio per pod.
	if err := r.computeEnergyEfficiencyRatio(ctx); err != nil {
		klog.Warningf("%s: failed to compute energy-efficiency ratio: %v", r.Name(), err)
	}

	// Validate metric availability: require ≥50% of expected metrics.
	if err := r.validateMetricAvailability(ctx); err != nil {
		return err
	}

	return nil
}

// computeEnergyEfficiencyRatio computes the energy-efficiency ratio for each pod.
// The ratio is active_energy / total_energy, derived from pod CPU watts correlated
// with CPU utilization. Stored as a synthetic time series in the context.
func (r *CarbonRightSizingRecommender) computeEnergyEfficiencyRatio(ctx *framework.RecommendationContext) error {
	podWattsList := ctx.InputValue(keyPodCPUWatts)
	cpuUsageList := ctx.InputValue(keyCPUUsage)

	if len(podWattsList) == 0 || len(cpuUsageList) == 0 {
		return fmt.Errorf("missing pod watts or CPU usage data for energy-efficiency computation")
	}

	// Compute per-pod efficiency: correlate energy with CPU utilization.
	// Active energy is approximated as total_watts * cpu_utilization_fraction.
	// Efficiency = active_energy / total_energy = cpu_utilization_fraction (when
	// energy is proportional to allocation).
	var ratioSeries []*common.TimeSeries
	for _, podTS := range podWattsList {
		podName := getLabel(podTS, "pod_name")
		if podName == "" {
			continue
		}

		totalAvg := avgSamples(podTS.Samples)
		if totalAvg <= 0 {
			continue
		}

		// Estimate active energy fraction from CPU utilization.
		// If workload-level CPU usage is available, use it as a proxy for the
		// fraction of energy doing useful work.
		activeAvg := estimateActiveEnergy(cpuUsageList, totalAvg)

		ratio := activeAvg / totalAvg
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}

		ts := &common.TimeSeries{
			Labels: []common.Label{
				{Name: "pod_name", Value: podName},
				{Name: "metric", Value: "energy_efficiency_ratio"},
			},
			Samples: []common.Sample{{Value: ratio, Timestamp: time.Now().Unix()}},
		}
		ratioSeries = append(ratioSeries, ts)

		klog.Infof("%s: pod %s energy-efficiency ratio = %.4f", r.Name(), podName, ratio)
	}

	if len(ratioSeries) > 0 {
		ctx.AddInputValue(keyEnergyEfficiency, ratioSeries)
	}

	return nil
}

// estimateActiveEnergy estimates the active energy from CPU usage time series.
// Returns the average CPU usage value as a proxy for active energy fraction.
func estimateActiveEnergy(cpuUsageList []*common.TimeSeries, totalWatts float64) float64 {
	if len(cpuUsageList) == 0 {
		return 0
	}
	// Use the first (aggregated) CPU usage time series.
	cpuAvg := avgSamples(cpuUsageList[0].Samples)
	// CPU usage in cores; normalize against total watts to get active fraction.
	// This is a simplified model: active_energy ≈ total_watts * (cpu_usage / max_cpu).
	// Since we don't have max_cpu here, we use the raw ratio as a proxy.
	if cpuAvg <= 0 {
		return 0
	}
	// Return the fraction of total watts attributable to active work.
	// Capped at totalWatts to keep ratio ≤ 1.
	active := cpuAvg * totalWatts
	if active > totalWatts {
		active = totalWatts
	}
	return active
}

// getLabel returns the value of a label from a time series, or empty string if not found.
func getLabel(ts *common.TimeSeries, name string) string {
	for _, l := range ts.Labels {
		if l.Name == name {
			return l.Value
		}
	}
	return ""
}

// avgSamples computes the arithmetic mean of sample values.
func avgSamples(samples []common.Sample) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		sum += s.Value
	}
	return sum / float64(len(samples))
}

// validateMetricAvailability checks that at least 50% of expected metrics
// are present in the context. If fewer than 50% are available, the recommendation
// is skipped with a descriptive status.
func (r *CarbonRightSizingRecommender) validateMetricAvailability(ctx *framework.RecommendationContext) error {
	available := 0
	for _, key := range expectedMetricKeys {
		if ts := ctx.InputValue(key); len(ts) > 0 {
			available++
		}
	}

	total := len(expectedMetricKeys)
	if total > 0 && available*2 < total {
		msg := fmt.Sprintf("Insufficient energy data for %s/%s: only %d/%d metrics available",
			ctx.Recommendation.Spec.TargetRef.Namespace,
			ctx.Recommendation.Spec.TargetRef.Name,
			available, total)
		ctx.Recommendation.Status.Description = msg
		return fmt.Errorf(msg)
	}

	return nil
}
