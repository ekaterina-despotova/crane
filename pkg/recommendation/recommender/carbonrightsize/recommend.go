package carbonrightsize

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/gocrane/crane/pkg/common"
	"github.com/gocrane/crane/pkg/recommendation/framework"
)

type PatchResource struct {
	Spec PatchResourceSpec `json:"spec,omitempty"`
}

type PatchResourceSpec struct {
	Template PatchResourcePodTemplateSpec `json:"template"`
}

type PatchResourcePodTemplateSpec struct {
	Spec PatchResourcePodSpec `json:"spec,omitempty"`
}

type PatchResourcePodSpec struct {
	Containers []corev1.Container `json:"containers" patchStrategy:"merge" patchMergeKey:"name"`
}

func (r *CarbonRightSizingRecommender) PreRecommend(ctx *framework.RecommendationContext) error {
	return nil
}

func (r *CarbonRightSizingRecommender) Recommend(ctx *framework.RecommendationContext) error {
	efficiencyList := ctx.InputValue(keyEnergyEfficiency)
	cpuUsageList := ctx.InputValue(keyCPUUsage)
	memUsageList := ctx.InputValue(keyMemUsage)

	if len(cpuUsageList) == 0 || len(memUsageList) == 0 {
		return fmt.Errorf("missing CPU or memory usage data for right-sizing")
	}

	var newContainers []corev1.Container
	var oldContainers []corev1.Container
	var descriptions []string

	for _, c := range ctx.PodTemplate.Spec.Containers {
		effRatio := r.getEfficiencyForPod(efficiencyList)

		if effRatio >= r.energyEfficiencyTarget {
			klog.Infof("%s: container %s efficiency %.4f >= target %.4f, skipping",
				r.Name(), c.Name, effRatio, r.energyEfficiencyTarget)
			continue
		}

		recCPU := r.computePercentile(cpuUsageList, r.cpuPercentile)
		recMem := r.computePercentile(memUsageList, r.memoryPercentile)

		if recCPU <= 0 || recMem <= 0 {
			klog.Warningf("%s: container %s computed zero recommendation (cpu=%.4f, mem=%.4f), skipping",
				r.Name(), c.Name, recCPU, recMem)
			continue
		}

		cpuQuantity := resource.NewMilliQuantity(int64(math.Ceil(recCPU*1000)), resource.DecimalSI)
		memQuantity := resource.NewQuantity(int64(math.Ceil(recMem)), resource.BinarySI)

		cpuLimit, memLimit := r.adjustLimits(c, cpuQuantity, memQuantity)

		newContainerSpec := corev1.Container{
			Name: c.Name,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    *cpuQuantity,
					corev1.ResourceMemory: *memQuantity,
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    cpuLimit,
					corev1.ResourceMemory: memLimit,
				},
			},
		}

		oldContainerSpec := corev1.Container{
			Name: c.Name,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    c.Resources.Requests[corev1.ResourceCPU],
					corev1.ResourceMemory: c.Resources.Requests[corev1.ResourceMemory],
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    c.Resources.Limits[corev1.ResourceCPU],
					corev1.ResourceMemory: c.Resources.Limits[corev1.ResourceMemory],
				},
			},
		}

		newContainers = append(newContainers, newContainerSpec)
		oldContainers = append(oldContainers, oldContainerSpec)

		desc := fmt.Sprintf("Right-size: %s, current CPU req %s -> %s, mem req %s -> %s",
			c.Name,
			c.Resources.Requests.Cpu().String(), cpuQuantity.String(),
			c.Resources.Requests.Memory().String(), memQuantity.String())
		descriptions = append(descriptions, desc)
		klog.Infof("%s: %s", r.Name(), desc)
	}

	if len(newContainers) == 0 {
		ctx.Recommendation.Status.Action = "None"
		ctx.Recommendation.Status.Description = "No containers require right-sizing"
		return nil
	}

	ctx.Recommendation.Status.Action = "Patch"
	ctx.Recommendation.Status.Description = strings.Join(descriptions, "; ")

	var newPatch PatchResource
	newPatch.Spec.Template.Spec.Containers = newContainers
	newPatchBytes, err := json.Marshal(newPatch)
	if err != nil {
		return fmt.Errorf("failed to encode recommended manifest: %v", err)
	}

	var oldPatch PatchResource
	oldPatch.Spec.Template.Spec.Containers = oldContainers
	oldPatchBytes, err := json.Marshal(oldPatch)
	if err != nil {
		return fmt.Errorf("failed to encode current manifest: %v", err)
	}

	ctx.Recommendation.Status.RecommendedInfo = string(newPatchBytes)
	ctx.Recommendation.Status.CurrentInfo = string(oldPatchBytes)

	return nil
}

func (r *CarbonRightSizingRecommender) getEfficiencyForPod(efficiencyList []*common.TimeSeries) float64 {
	if len(efficiencyList) == 0 {
		return 0
	}

	if len(efficiencyList[0].Samples) == 0 {
		return 0
	}
	return efficiencyList[0].Samples[0].Value
}

func (r *CarbonRightSizingRecommender) computePercentile(tsList []*common.TimeSeries, p float64) float64 {
	var values []float64
	for _, ts := range tsList {
		for _, s := range ts.Samples {
			if s.Value > 0 {
				values = append(values, s.Value)
			}
		}
	}
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)

	idx := int(math.Ceil(p*float64(len(values)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(values) {
		idx = len(values) - 1
	}
	return values[idx]
}

func (r *CarbonRightSizingRecommender) adjustLimits(
	c corev1.Container,
	newCPUReq *resource.Quantity,
	newMemReq *resource.Quantity,
) (resource.Quantity, resource.Quantity) {
	cpuLimit := adjustSingleLimit(
		c.Resources.Requests[corev1.ResourceCPU],
		c.Resources.Limits[corev1.ResourceCPU],
		*newCPUReq,
	)
	memLimit := adjustSingleLimit(
		c.Resources.Requests[corev1.ResourceMemory],
		c.Resources.Limits[corev1.ResourceMemory],
		*newMemReq,
	)
	return cpuLimit, memLimit
}

func adjustSingleLimit(currentReq, currentLimit, newReq resource.Quantity) resource.Quantity {
	if currentLimit.IsZero() {
		return newReq.DeepCopy()
	}

	if newReq.Cmp(currentLimit) <= 0 {
		return currentLimit.DeepCopy()
	}

	if currentReq.IsZero() {
		return newReq.DeepCopy()
	}

	ratio := float64(currentReq.MilliValue()) / float64(currentLimit.MilliValue())
	if ratio <= 0 {
		return newReq.DeepCopy()
	}
	newLimitValue := float64(newReq.MilliValue()) / ratio
	return *resource.NewMilliQuantity(int64(math.Ceil(newLimitValue)), currentLimit.Format)
}

func (r *CarbonRightSizingRecommender) Policy(ctx *framework.RecommendationContext) error {
	return nil
}
