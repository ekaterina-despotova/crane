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

const (
	keyPodCPUJoules     = "kepler-pod-cpu-joules"
	keyPodCPUWatts      = "kepler-pod-cpu-watts"
	keyPodGPUWatts      = "kepler-pod-gpu-watts"
	keyCPUUsage         = "cpu-usage"
	keyMemUsage         = "mem-usage"
	keyEnergyEfficiency = "kepler-energy-efficiency-ratio"
)

const keplerAvailabilityExpr = `kepler_container_package_joules_total`

const (
	keplerPodCPUJoulesExpr = `sum by (pod_name, container_namespace) (kepler_container_package_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"})`
	keplerPodCPUWattsExpr  = `sum by (pod_name, container_namespace) (rate(kepler_container_package_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"}[5m]))`
	keplerPodGPUWattsExpr  = `sum by (pod_name, container_namespace) (rate(kepler_container_other_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"}[5m]))`
)

var expectedMetricKeys = []string{
	keyPodCPUJoules, keyPodCPUWatts, keyPodGPUWatts,
	keyCPUUsage, keyMemUsage,
}

func (r *CarbonRightSizingRecommender) CheckDataProviders(ctx *framework.RecommendationContext) error {
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

func (r *CarbonRightSizingRecommender) CollectData(ctx *framework.RecommendationContext) error {
	caller := fmt.Sprintf(callerFormat, klog.KObj(ctx.Recommendation), ctx.Recommendation.UID)
	now := time.Now()
	start := now.Add(-time.Hour * 24 * time.Duration(r.observationDays))
	step := time.Minute
	ns := ctx.Recommendation.Spec.TargetRef.Namespace
	kind := ctx.Recommendation.Spec.TargetRef.Kind
	name := ctx.Recommendation.Spec.TargetRef.Name

	podNameRegex := buildPodNameRegex(ctx)
	if podNameRegex == "" {
		return fmt.Errorf("no pods found matching selector for %s/%s", ns, name)
	}

	r.collectPodEnergyMetrics(ctx, caller, ns, podNameRegex, start, now, step)

	r.collectUtilizationMetrics(ctx, caller, ns, name, kind, start, now, step)

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

func (r *CarbonRightSizingRecommender) PostProcessing(ctx *framework.RecommendationContext) error {
	if err := r.computeEnergyEfficiencyRatio(ctx); err != nil {
		klog.Warningf("%s: failed to compute energy-efficiency ratio: %v", r.Name(), err)
	}

	if err := r.validateMetricAvailability(ctx); err != nil {
		return err
	}

	return nil
}

func (r *CarbonRightSizingRecommender) computeEnergyEfficiencyRatio(ctx *framework.RecommendationContext) error {
	podWattsList := ctx.InputValue(keyPodCPUWatts)
	cpuUsageList := ctx.InputValue(keyCPUUsage)

	if len(podWattsList) == 0 || len(cpuUsageList) == 0 {
		return fmt.Errorf("missing pod watts or CPU usage data for energy-efficiency computation")
	}

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


func estimateActiveEnergy(cpuUsageList []*common.TimeSeries, totalWatts float64) float64 {
	if len(cpuUsageList) == 0 {
		return 0
	}
	cpuAvg := avgSamples(cpuUsageList[0].Samples)

	if cpuAvg <= 0 {
		return 0
	}

	active := cpuAvg * totalWatts
	if active > totalWatts {
		active = totalWatts
	}
	return active
}

func getLabel(ts *common.TimeSeries, name string) string {
	for _, l := range ts.Labels {
		if l.Name == name {
			return l.Value
		}
	}
	return ""
}

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
