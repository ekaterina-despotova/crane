package carbonidle

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

const callerFormat = "CarbonIdleResourceRecommender-%s-%s"

// Input value keys for RecommendationContext.
const (
	keyPodCPUJoules       = "kepler-pod-cpu-joules"
	keyPodCPUWatts        = "kepler-pod-cpu-watts"
	keyPodGPUWatts        = "kepler-pod-gpu-watts"
	keyContainerCPUJoules = "kepler-container-cpu-joules"
	keyContainerCPUWatts  = "kepler-container-cpu-watts"
	keyContainerGPUWatts  = "kepler-container-gpu-watts"
	keyNodeCPUJoules      = "kepler-node-cpu-joules"
	keyNodeCPUWatts       = "kepler-node-cpu-watts"
	keyNodeCPUIdleWatts   = "kepler-node-cpu-idle-watts"
	keyNodeCPUActiveWatts = "kepler-node-cpu-active-watts"
	keyEnergyIdleRatio    = "kepler-energy-idle-ratio"
)

// Kepler metric availability check — kepler_container_package_joules_total is the
// primary counter exported by all Kepler versions.
const keplerAvailabilityExpr = `kepler_container_package_joules_total`

// Kepler PromQL expression templates for pod-level metrics.
// Labels: container_namespace, pod_name. Use rate() over 5m to get instantaneous watts.
// We sum across containers to get per-pod total.
const (
	keplerPodCPUJoulesExpr = `sum by (pod_name, container_namespace) (kepler_container_package_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"})`
	keplerPodCPUWattsExpr  = `sum by (pod_name, container_namespace) (rate(kepler_container_package_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"}[5m]))`
	keplerPodGPUWattsExpr  = `sum by (pod_name, container_namespace) (rate(kepler_container_other_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"}[5m]))`
)

// Kepler PromQL expression templates for container-level metrics.
// Labels: container_namespace, pod_name, container_name.
const (
	keplerContainerCPUJoulesExpr = `kepler_container_package_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"}`
	keplerContainerCPUWattsExpr  = `rate(kepler_container_package_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"}[5m])`
	keplerContainerGPUWattsExpr  = `rate(kepler_container_other_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"}[5m])`
)

// Kepler PromQL expression templates for node-level metrics.
// Node label: instance (node hostname). Sum across packages for total node power.
const (
	keplerNodeCPUJoulesExpr      = `sum by (instance) (kepler_node_package_joules_total{instance="%s",mode="dynamic"})`
	keplerNodeCPUWattsExpr       = `sum by (instance) (rate(kepler_node_package_joules_total{instance="%s",mode="dynamic"}[5m]))`
	keplerNodeCPUIdleWattsExpr   = `sum by (instance) (rate(kepler_node_package_joules_total{instance="%s",mode="idle"}[5m]))`
	keplerNodeCPUActiveWattsExpr = `sum by (instance) (rate(kepler_node_package_joules_total{instance="%s",mode="dynamic"}[5m]))`
)

// CheckDataProviders verifies that Kepler metrics are available in Prometheus.
// Returns an error if Kepler is not installed or not exporting metrics.
func (r *CarbonIdleResourceRecommender) CheckDataProviders(ctx *framework.RecommendationContext) error {
	if err := r.BaseRecommender.CheckDataProviders(ctx); err != nil {
		return err
	}

	// Verify Kepler metrics exist by querying kepler_node_cpu_joules_total.
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
		return fmt.Errorf("Kepler metrics not available: kepler_node_cpu_joules_total not found. Ensure Kepler is installed and exporting to Prometheus.")
	}

	return nil
}

// CollectData queries Kepler energy metrics from Prometheus and stores them in the context.
func (r *CarbonIdleResourceRecommender) CollectData(ctx *framework.RecommendationContext) error {
	caller := fmt.Sprintf(callerFormat, klog.KObj(ctx.Recommendation), ctx.Recommendation.UID)
	now := time.Now()
	start := now.Add(-time.Hour * 24 * time.Duration(r.observationDays))
	step := time.Minute
	ns := ctx.Recommendation.Spec.TargetRef.Namespace
	kind := ctx.Recommendation.Spec.TargetRef.Kind

	// Build pod name regex from retrieved pods.
	podNameRegex := buildPodNameRegex(ctx)
	if podNameRegex == "" && kind != "Node" {
		return fmt.Errorf("no pods found matching selector for %s/%s", ns, ctx.Recommendation.Spec.TargetRef.Name)
	}

	// Collect pod-level metrics.
	if podNameRegex != "" {
		r.collectPodMetrics(ctx, caller, ns, podNameRegex, start, now, step)
	}

	// Collect container-level metrics.
	if podNameRegex != "" {
		r.collectContainerMetrics(ctx, caller, ns, podNameRegex, start, now, step)
	}

	// Collect node-level metrics for Node targets.
	if kind == "Node" {
		nodeName := ctx.Recommendation.Spec.TargetRef.Name
		r.collectNodeMetrics(ctx, caller, nodeName, start, now, step)
	}

	return nil
}

// buildPodNameRegex constructs a regex matching all pod names from the context.
func buildPodNameRegex(ctx *framework.RecommendationContext) string {
	if len(ctx.Pods) == 0 {
		return ""
	}
	names := make([]string, 0, len(ctx.Pods))
	for _, pod := range ctx.Pods {
		names = append(names, pod.Name)
	}
	// Join with | for regex alternation.
	result := ""
	for i, name := range names {
		if i > 0 {
			result += "|"
		}
		result += name
	}
	return result
}

// collectPodMetrics queries pod-level Kepler energy metrics.
func (r *CarbonIdleResourceRecommender) collectPodMetrics(
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

// collectContainerMetrics queries container-level Kepler energy metrics.
func (r *CarbonIdleResourceRecommender) collectContainerMetrics(
	ctx *framework.RecommendationContext,
	caller, namespace, podNameRegex string,
	start, end time.Time, step time.Duration,
) {
	containerMetrics := []struct {
		key  string
		expr string
	}{
		{keyContainerCPUJoules, fmt.Sprintf(keplerContainerCPUJoulesExpr, namespace, podNameRegex)},
		{keyContainerCPUWatts, fmt.Sprintf(keplerContainerCPUWattsExpr, namespace, podNameRegex)},
		{keyContainerGPUWatts, fmt.Sprintf(keplerContainerGPUWattsExpr, namespace, podNameRegex)},
	}

	for _, m := range containerMetrics {
		r.queryAndStore(ctx, caller, m.key, m.expr, start, end, step)
	}
}

// collectNodeMetrics queries node-level Kepler energy metrics.
func (r *CarbonIdleResourceRecommender) collectNodeMetrics(
	ctx *framework.RecommendationContext,
	caller, nodeName string,
	start, end time.Time, step time.Duration,
) {
	nodeMetrics := []struct {
		key  string
		expr string
	}{
		{keyNodeCPUJoules, fmt.Sprintf(keplerNodeCPUJoulesExpr, nodeName)},
		{keyNodeCPUWatts, fmt.Sprintf(keplerNodeCPUWattsExpr, nodeName)},
		{keyNodeCPUIdleWatts, fmt.Sprintf(keplerNodeCPUIdleWattsExpr, nodeName)},
		{keyNodeCPUActiveWatts, fmt.Sprintf(keplerNodeCPUActiveWattsExpr, nodeName)},
	}

	for _, m := range nodeMetrics {
		r.queryAndStore(ctx, caller, m.key, m.expr, start, end, step)
	}
}

// queryAndStore queries a single Kepler metric and stores the result in the context.
// Errors are logged as warnings and the metric is skipped.
func (r *CarbonIdleResourceRecommender) queryAndStore(
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

// PostProcessing computes the Energy_Idle_Ratio for node targets and validates
// that sufficient metrics are available for each resource.
func (r *CarbonIdleResourceRecommender) PostProcessing(ctx *framework.RecommendationContext) error {
	kind := ctx.Recommendation.Spec.TargetRef.Kind

	// Compute Energy_Idle_Ratio for Node targets.
	if kind == "Node" {
		if err := r.computeEnergyIdleRatio(ctx); err != nil {
			klog.Warningf("%s: failed to compute Energy_Idle_Ratio: %v", r.Name(), err)
		}
	}

	// Validate metric availability: require ≥50% of expected metrics.
	if err := r.validateMetricAvailability(ctx); err != nil {
		return err
	}

	return nil
}

// computeEnergyIdleRatio computes the Energy_Idle_Ratio from node idle and total watts
// and stores the result as a synthetic time series in the context.
func (r *CarbonIdleResourceRecommender) computeEnergyIdleRatio(ctx *framework.RecommendationContext) error {
	idleWattsList := ctx.InputValue(keyNodeCPUIdleWatts)
	totalWattsList := ctx.InputValue(keyNodeCPUWatts)

	if len(idleWattsList) == 0 || len(totalWattsList) == 0 {
		return fmt.Errorf("missing node idle or total watts data for Energy_Idle_Ratio computation")
	}

	// Use the first time series from each (single node target).
	idleTS := idleWattsList[0]
	totalTS := totalWattsList[0]

	if len(idleTS.Samples) == 0 || len(totalTS.Samples) == 0 {
		return fmt.Errorf("empty samples in node watts time series")
	}

	// Build a map of timestamp -> total watts for alignment.
	totalByTime := make(map[int64]float64, len(totalTS.Samples))
	for _, s := range totalTS.Samples {
		totalByTime[s.Timestamp] = s.Value
	}

	// Compute idle ratio at each aligned time point.
	var sumRatio float64
	var count int
	for _, s := range idleTS.Samples {
		total, ok := totalByTime[s.Timestamp]
		if !ok || total <= 0 {
			continue
		}
		ratio := s.Value / total
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		sumRatio += ratio
		count++
	}

	if count == 0 {
		return fmt.Errorf("no aligned time points for Energy_Idle_Ratio computation")
	}

	avgRatio := sumRatio / float64(count)

	// Store as a single-sample synthetic time series.
	ratioTS := &common.TimeSeries{
		Labels:  []common.Label{{Name: "metric", Value: "energy_idle_ratio"}},
		Samples: []common.Sample{{Value: avgRatio, Timestamp: time.Now().Unix()}},
	}
	ctx.AddInputValue(keyEnergyIdleRatio, []*common.TimeSeries{ratioTS})

	klog.Infof("%s: computed Energy_Idle_Ratio = %.4f from %d aligned samples", r.Name(), avgRatio, count)
	return nil
}

// validateMetricAvailability checks that at least 50% of expected Kepler metrics
// are present in the context. If fewer than 50% are available, the recommendation
// is skipped with a descriptive status.
func (r *CarbonIdleResourceRecommender) validateMetricAvailability(ctx *framework.RecommendationContext) error {
	kind := ctx.Recommendation.Spec.TargetRef.Kind

	var expectedKeys []string
	if kind == "Node" {
		expectedKeys = []string{
			keyPodCPUJoules, keyPodCPUWatts, keyPodGPUWatts,
			keyContainerCPUJoules, keyContainerCPUWatts, keyContainerGPUWatts,
			keyNodeCPUJoules, keyNodeCPUWatts, keyNodeCPUIdleWatts, keyNodeCPUActiveWatts,
		}
	} else {
		expectedKeys = []string{
			keyPodCPUJoules, keyPodCPUWatts, keyPodGPUWatts,
			keyContainerCPUJoules, keyContainerCPUWatts, keyContainerGPUWatts,
		}
	}

	available := 0
	for _, key := range expectedKeys {
		if ts := ctx.InputValue(key); len(ts) > 0 {
			available++
		}
	}

	total := len(expectedKeys)
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
