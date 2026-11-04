package carbonhorizontalscale

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

const callerFormat = "CarbonHorizontalScalingRecommender-%s-%s"

const (
	keyCPUUsage    = "cpu-usage"
	keyPodCPUWatts = "kepler-pod-cpu-watts"
)

const keplerAvailabilityExpr = `kepler_container_package_joules_total`

const keplerPodCPUWattsExpr = `sum by (pod_name, container_namespace) (rate(kepler_container_package_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"}[5m]))`

func (r *CarbonHorizontalScalingRecommender) CheckDataProviders(ctx *framework.RecommendationContext) error {
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

func (r *CarbonHorizontalScalingRecommender) CollectData(ctx *framework.RecommendationContext) error {
	caller := fmt.Sprintf(callerFormat, klog.KObj(ctx.Recommendation), ctx.Recommendation.UID)
	now := time.Now()
	start := now.Add(-time.Hour * 24 * time.Duration(r.observationDays))
	step := time.Minute
	ns := ctx.Recommendation.Spec.TargetRef.Namespace
	kind := ctx.Recommendation.Spec.TargetRef.Kind
	name := ctx.Recommendation.Spec.TargetRef.Name

	cpuExpr := utils.GetWorkloadCpuUsageExpression(ns, name, kind)
	r.queryAndStore(ctx, caller, keyCPUUsage, cpuExpr, start, now, step)

	podNameRegex := buildPodNameRegex(ctx)
	if podNameRegex != "" {
		r.queryAndStore(ctx, caller, keyPodCPUWatts,
			fmt.Sprintf(keplerPodCPUWattsExpr, ns, podNameRegex), start, now, step)
	}

	return nil
}

func (r *CarbonHorizontalScalingRecommender) PostProcessing(ctx *framework.RecommendationContext) error {
	if ts := ctx.InputValue(keyCPUUsage); len(ts) == 0 {
		msg := fmt.Sprintf("Insufficient CPU usage data for %s/%s: cannot compute replica recommendation",
			ctx.Recommendation.Spec.TargetRef.Namespace, ctx.Recommendation.Spec.TargetRef.Name)
		ctx.Recommendation.Status.Description = msg
		return fmt.Errorf(msg)
	}
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

func (r *CarbonHorizontalScalingRecommender) queryAndStore(
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
