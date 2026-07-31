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

const callerFormat = "CarbonTemporalShiftingRecommender-%s-%s"

const (
	keyPodCPUJoules  = "kepler-pod-cpu-joules"
	keyPodCPUWatts   = "kepler-pod-cpu-watts"
	keyPodGPUWatts   = "kepler-pod-gpu-watts"
	keyNodeCPUWatts  = "kepler-node-cpu-watts"
	keyHourlyProfile = "hourly-energy-profile"
)

const keplerAvailabilityExpr = `kepler_container_package_joules_total`

const (
	keplerPodCPUJoulesExpr = `sum by (pod_name, container_namespace) (kepler_container_package_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"})`
	keplerPodCPUWattsExpr  = `sum by (pod_name, container_namespace) (rate(kepler_container_package_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"}[5m]))`
	keplerPodGPUWattsExpr  = `sum by (pod_name, container_namespace) (rate(kepler_container_other_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"}[5m]))`
)

const keplerNodeCPUWattsExpr = `sum by (instance) (rate(kepler_node_package_joules_total{instance="%s",mode="dynamic"}[5m]))`

func (r *CarbonTemporalShiftingRecommender) CheckDataProviders(ctx *framework.RecommendationContext) error {
	if err := r.BaseRecommender.CheckDataProviders(ctx); err != nil {
		return err
	}

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

func (r *CarbonTemporalShiftingRecommender) CollectData(ctx *framework.RecommendationContext) error {
	caller := fmt.Sprintf(callerFormat, klog.KObj(ctx.Recommendation), ctx.Recommendation.UID)
	now := time.Now()
	start := now.Add(-time.Hour * 24 * time.Duration(r.observationDays))
	step := time.Minute
	ns := ctx.Recommendation.Spec.TargetRef.Namespace
	kind := ctx.Recommendation.Spec.TargetRef.Kind

	podNameRegex := buildPodNameRegex(ctx)

	if podNameRegex == "" && (kind == "CronJob" || kind == "Job") {
		podNameRegex = ctx.Recommendation.Spec.TargetRef.Name + ".*"
	}

	if podNameRegex == "" {
		return fmt.Errorf("no pods found matching selector for %s/%s", ns, ctx.Recommendation.Spec.TargetRef.Name)
	}

	r.collectPodMetrics(ctx, caller, ns, podNameRegex, start, now, step)

	return nil
}

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

func (r *CarbonTemporalShiftingRecommender) collectPodMetrics(
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

func (r *CarbonTemporalShiftingRecommender) queryAndStore(
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

func (r *CarbonTemporalShiftingRecommender) PostProcessing(ctx *framework.RecommendationContext) error {
	podWattsList := ctx.InputValue(keyPodCPUWatts)
	if len(podWattsList) == 0 {
		return fmt.Errorf("no energy data available for hourly profile computation")
	}

	hourlyProfile := r.computeHourlyProfile(podWattsList)

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

func (r *CarbonTemporalShiftingRecommender) computeHourlyProfile(tsList []*common.TimeSeries) [24]float64 {
	var hourSums [24]float64
	var hourCounts [24]int

	for _, ts := range tsList {
		for _, s := range ts.Samples {
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


// --- CarbonSpatialShifting uses the same data collection as temporal ---

func (r *CarbonSpatialShiftingRecommender) CheckDataProviders(ctx *framework.RecommendationContext) error {
	if err := r.BaseRecommender.CheckDataProviders(ctx); err != nil {
		return err
	}

	caller := fmt.Sprintf("CarbonSpatialShiftingRecommender-%s-%s", klog.KObj(ctx.Recommendation), ctx.Recommendation.UID)
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
		return fmt.Errorf("Kepler metrics not available: kepler_container_package_joules_total not found.")
	}

	return nil
}

func (r *CarbonSpatialShiftingRecommender) CollectData(ctx *framework.RecommendationContext) error {
	caller := fmt.Sprintf("CarbonSpatialShiftingRecommender-%s-%s", klog.KObj(ctx.Recommendation), ctx.Recommendation.UID)
	now := time.Now()
	start := now.Add(-time.Hour * 24 * time.Duration(r.observationDays))
	step := time.Minute
	ns := ctx.Recommendation.Spec.TargetRef.Namespace
	kind := ctx.Recommendation.Spec.TargetRef.Kind

	podNameRegex := buildPodNameRegex(ctx)
	if podNameRegex == "" && (kind == "CronJob" || kind == "Job") {
		podNameRegex = ctx.Recommendation.Spec.TargetRef.Name + ".*"
	}
	if podNameRegex == "" {
		return fmt.Errorf("no pods found matching selector for %s/%s", ns, ctx.Recommendation.Spec.TargetRef.Name)
	}

	temporal := &CarbonTemporalShiftingRecommender{}
	temporal.collectPodMetrics(ctx, caller, ns, podNameRegex, start, now, step)

	return nil
}

func (r *CarbonSpatialShiftingRecommender) PostProcessing(ctx *framework.RecommendationContext) error {
	podWattsList := ctx.InputValue(keyPodCPUWatts)
	if len(podWattsList) == 0 {
		return fmt.Errorf("no energy data available for spatial analysis")
	}

	temporal := &CarbonTemporalShiftingRecommender{}
	hourlyProfile := temporal.computeHourlyProfile(podWattsList)

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

	return nil
}
