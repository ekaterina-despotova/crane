package carbonshift

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
)

const callerFormat = "CarbonLoadShiftingRecommender-%s-%s"

// Input value keys for RecommendationContext.
const (
	keyPodCPUJoules  = "kepler-pod-cpu-joules"
	keyPodCPUWatts   = "kepler-pod-cpu-watts"
	keyPodGPUWatts   = "kepler-pod-gpu-watts"
	keyNodeCPUWatts  = "kepler-node-cpu-watts"
	keyHourlyProfile = "hourly-energy-profile"
)

// Kepler metric availability check.
const keplerAvailabilityExpr = `kepler_container_cpu_joules_total`

// Kepler PromQL expression templates for pod-level metrics.
const (
	keplerPodCPUJoulesExpr = `kepler_pod_cpu_joules_total{pod_namespace="%s",pod_name=~"%s"}`
	keplerPodCPUWattsExpr  = `kepler_pod_cpu_watts{pod_namespace="%s",pod_name=~"%s"}`
	keplerPodGPUWattsExpr  = `kepler_pod_gpu_watts{pod_namespace="%s",pod_name=~"%s"}`
)

// Kepler PromQL expression for node-level watts (used for node targets).
const keplerNodeCPUWattsExpr = `kepler_node_cpu_watts{node_name="%s"}`

// CheckDataProviders verifies that Kepler metrics are available in Prometheus.
func (r *CarbonLoadShiftingRecommender) CheckDataProviders(ctx *framework.RecommendationContext) error {
	if err := r.BaseRecommender.CheckDataProviders(ctx); err != nil {
		return err
	}

	// Verify Kepler metrics exist by querying kepler_container_cpu_joules_total.
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
		return fmt.Errorf("Kepler metrics not available: kepler_container_cpu_joules_total not found. Ensure Kepler is installed and exporting to Prometheus.")
	}

	return nil
}

// CollectData queries Kepler energy metrics from Prometheus and stores them in the context.
func (r *CarbonLoadShiftingRecommender) CollectData(ctx *framework.RecommendationContext) error {
	caller := fmt.Sprintf(callerFormat, klog.KObj(ctx.Recommendation), ctx.Recommendation.UID)
	now := time.Now()
	start := now.Add(-time.Hour * 24 * time.Duration(r.observationDays))
	step := time.Minute
	ns := ctx.Recommendation.Spec.TargetRef.Namespace
	kind := ctx.Recommendation.Spec.TargetRef.Kind

	// Build pod name regex from retrieved pods.
	podNameRegex := buildPodNameRegex(ctx)

	// For Jobs/CronJobs, pods may have completed. Use workload name as fallback.
	if podNameRegex == "" && (kind == "CronJob" || kind == "Job") {
		// Match pods whose name starts with the job/cronjob name.
		podNameRegex = ctx.Recommendation.Spec.TargetRef.Name + ".*"
	}

	if podNameRegex == "" {
		return fmt.Errorf("no pods found matching selector for %s/%s", ns, ctx.Recommendation.Spec.TargetRef.Name)
	}

	// Collect pod-level energy metrics.
	r.collectPodMetrics(ctx, caller, ns, podNameRegex, start, now, step)

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

// collectPodMetrics queries pod-level Kepler energy metrics.
func (r *CarbonLoadShiftingRecommender) collectPodMetrics(
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

// queryAndStore queries a single Kepler metric and stores the result in the context.
func (r *CarbonLoadShiftingRecommender) queryAndStore(
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

// PostProcessing computes the hourly energy profile from collected time series.
// It buckets energy consumption by hour-of-day to identify when the workload runs.
func (r *CarbonLoadShiftingRecommender) PostProcessing(ctx *framework.RecommendationContext) error {
	podWattsList := ctx.InputValue(keyPodCPUWatts)
	if len(podWattsList) == 0 {
		return fmt.Errorf("no energy data available for hourly profile computation")
	}

	// Compute hourly energy profile: average watts per hour-of-day (0-23).
	hourlyProfile := r.computeHourlyProfile(podWattsList)

	// Store as synthetic time series (hour as timestamp, avg watts as value).
	var samples []common.Sample
	for hour := 0; hour < 24; hour++ {
		samples = append(samples, common.Sample{
			Value:     hourlyProfile[hour],
			Timestamp: int64(hour),
		})
	}

	ts := &common.TimeSeries{
		Labels:  []common.Label{{Name: "metric", Value: "hourly_energy_profile"}},
		Samples: samples,
	}
	ctx.AddInputValue(keyHourlyProfile, []*common.TimeSeries{ts})

	klog.Infof("%s: computed hourly energy profile for %s/%s",
		r.Name(), ctx.Recommendation.Spec.TargetRef.Namespace, ctx.Recommendation.Spec.TargetRef.Name)

	return nil
}

// computeHourlyProfile buckets time series samples by hour-of-day and returns
// the average watts for each hour (0-23).
func (r *CarbonLoadShiftingRecommender) computeHourlyProfile(tsList []*common.TimeSeries) [24]float64 {
	var hourSums [24]float64
	var hourCounts [24]int

	for _, ts := range tsList {
		for _, s := range ts.Samples {
			// Timestamp is Unix seconds. Extract hour-of-day.
			t := time.Unix(s.Timestamp, 0).UTC()
			hour := t.Hour()
			hourSums[hour] += s.Value
			hourCounts[hour]++
		}
	}

	var profile [24]float64
	for h := 0; h < 24; h++ {
		if hourCounts[h] > 0 {
			profile[h] = hourSums[h] / float64(hourCounts[h])
		}
	}
	return profile
}
