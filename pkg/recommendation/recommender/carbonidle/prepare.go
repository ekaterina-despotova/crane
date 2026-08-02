package carbonidle

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	"github.com/gocrane/crane/pkg/metricnaming"
	"github.com/gocrane/crane/pkg/providers"
	"github.com/gocrane/crane/pkg/recommendation/framework"
)

const callerFormat = "CarbonIdleResourceRecommender-%s-%s"

const (
	keyPodCPUJoules       = "kepler-pod-cpu-joules"
	keyPodCPUWatts        = "kepler-pod-cpu-watts"
	keyPodGPUWatts        = "kepler-pod-gpu-watts"
	keyContainerCPUJoules = "kepler-container-cpu-joules"
	keyContainerCPUWatts  = "kepler-container-cpu-watts"
	keyContainerGPUWatts  = "kepler-container-gpu-watts"
	keyNodeCPUJoules      = "kepler-node-cpu-joules"
	keyNodeCPUWatts       = "kepler-node-cpu-watts"
	keyNodeCPUActiveWatts = "kepler-node-cpu-active-watts"
)

const keplerAvailabilityExpr = `kepler_container_package_joules_total`

const (
	keplerPodCPUJoulesExpr = `sum by (pod_name, container_namespace) (kepler_container_package_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"})`
	keplerPodCPUWattsExpr  = `sum by (pod_name, container_namespace) (rate(kepler_container_package_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"}[5m]))`
	keplerPodGPUWattsExpr  = `sum by (pod_name, container_namespace) (rate(kepler_container_other_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"}[5m]))`
)

const (
	keplerContainerCPUJoulesExpr = `kepler_container_package_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"}`
	keplerContainerCPUWattsExpr  = `rate(kepler_container_package_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"}[5m])`
	keplerContainerGPUWattsExpr  = `rate(kepler_container_other_joules_total{container_namespace="%s",pod_name=~"%s",mode="dynamic"}[5m])`
)

const (
	keplerNodeCPUJoulesExpr      = `sum by (instance) (kepler_node_package_joules_total{instance="%s",mode="dynamic"})`
	keplerNodeCPUWattsExpr       = `sum by (instance) (rate(kepler_node_package_joules_total{instance="%s",mode="dynamic"}[5m]))`
	keplerNodeCPUActiveWattsExpr = `sum by (instance) (rate(kepler_node_package_joules_total{instance="%s",mode="dynamic"}[5m]))`
)

const (
	keplerPodCPUWattsNoNSExpr       = `sum by (pod_name, container_namespace) (rate(kepler_container_package_joules_total{pod_name=~"%s",mode="dynamic"}[5m]))`
	keplerContainerCPUWattsNoNSExpr = `rate(kepler_container_package_joules_total{pod_name=~"%s",mode="dynamic"}[5m])`
)

func (r *CarbonIdleResourceRecommender) CheckDataProviders(ctx *framework.RecommendationContext) error {
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

func (r *CarbonIdleResourceRecommender) CollectData(ctx *framework.RecommendationContext) error {
	caller := fmt.Sprintf(callerFormat, klog.KObj(ctx.Recommendation), ctx.Recommendation.UID)
	now := time.Now()
	start := now.Add(-time.Hour * 24 * time.Duration(r.observationDays))
	step := time.Minute
	ns := ctx.Recommendation.Spec.TargetRef.Namespace
	kind := ctx.Recommendation.Spec.TargetRef.Kind
	name := ctx.Recommendation.Spec.TargetRef.Name

	switch kind {
	case "Node":
		r.collectNodeMetrics(ctx, caller, name, start, now, step)
		podNameRegex := buildPodNameRegex(ctx)
		if podNameRegex != "" {
			r.queryAndStore(ctx, caller, keyPodCPUWatts,
				fmt.Sprintf(keplerPodCPUWattsNoNSExpr, podNameRegex), start, now, step)
			r.queryAndStore(ctx, caller, keyContainerCPUWatts,
				fmt.Sprintf(keplerContainerCPUWattsNoNSExpr, podNameRegex), start, now, step)
		}
		return nil

	case "Pod":
		r.collectPodMetrics(ctx, caller, ns, name, start, now, step)
		r.collectContainerMetrics(ctx, caller, ns, name, start, now, step)
		return nil

	default:
		podNameRegex := buildPodNameRegex(ctx)
		if podNameRegex == "" {
			return fmt.Errorf("no pods found matching selector for %s/%s", ns, name)
		}
		r.collectPodMetrics(ctx, caller, ns, podNameRegex, start, now, step)
		r.collectContainerMetrics(ctx, caller, ns, podNameRegex, start, now, step)
		return nil
	}
}

func buildPodNameRegex(ctx *framework.RecommendationContext) string {
	if len(ctx.Pods) == 0 {
		return ""
	}
	names := make([]string, 0, len(ctx.Pods))
	for _, pod := range ctx.Pods {
		names = append(names, pod.Name)
	}
	result := ""
	for i, name := range names {
		if i > 0 {
			result += "|"
		}
		result += name
	}
	return result
}

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
		{keyNodeCPUActiveWatts, fmt.Sprintf(keplerNodeCPUActiveWattsExpr, nodeName)},
	}

	for _, m := range nodeMetrics {
		r.queryAndStore(ctx, caller, m.key, m.expr, start, end, step)
	}
}

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


func (r *CarbonIdleResourceRecommender) PostProcessing(ctx *framework.RecommendationContext) error {
	if err := r.validateMetricAvailability(ctx); err != nil {
		return err
	}
	return nil
}

func (r *CarbonIdleResourceRecommender) validateMetricAvailability(ctx *framework.RecommendationContext) error {
	kind := ctx.Recommendation.Spec.TargetRef.Kind

	var required []string
	switch kind {
	case "Node":
		required = []string{keyNodeCPUWatts, keyPodCPUWatts}
	case "Pod":
		required = []string{keyPodCPUWatts}
	default:
		required = []string{keyPodCPUWatts, keyContainerCPUWatts}
	}

	for _, key := range required {
		if ts := ctx.InputValue(key); len(ts) == 0 {
			msg := fmt.Sprintf("Insufficient energy data for %s/%s: missing %s",
				ctx.Recommendation.Spec.TargetRef.Namespace,
				ctx.Recommendation.Spec.TargetRef.Name, key)
			ctx.Recommendation.Status.Description = msg
			return fmt.Errorf(msg)
		}
	}

	return nil
}
