package recommendation

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	analysisapi "github.com/gocrane/api/analysis/v1alpha1"

	"github.com/gocrane/crane/pkg/providerid"
	"github.com/gocrane/crane/pkg/recommendation/recommender"
	"github.com/gocrane/crane/pkg/server/ginwrapper"
)


const drainNodeMinSurvivors = 1


type drainNodeResponse struct {
	Node     string          `json:"node"`
	Cordoned bool            `json:"cordoned"`
	Drain    *drainResult    `json:"drain"`
	Instance *instanceInfo   `json:"instance"`
	JobName  string          `json:"jobName"`
	Message  string          `json:"message"`
}

type instanceInfo struct {
	VMSS       string `json:"vmss"`
	InstanceID string `json:"instanceId"`
	RG         string `json:"resourceGroup"`
}


func (h *Handler) DrainNode(c *gin.Context) {
	ctx := context.TODO()


	rec := &analysisapi.Recommendation{}
	if err := h.client.Get(ctx, types.NamespacedName{
		Namespace: c.Param("namespace"),
		Name:      c.Param("recommendationName"),
	}, rec); err != nil {
		ginwrapper.WriteResponse(c, err, nil)
		return
	}

	if string(rec.Spec.Type) != recommender.CarbonIdleResourceRecommender {
		ginwrapper.WriteResponse(c, fmt.Errorf(
			"drain-node only supports %s recommendations, got %s",
			recommender.CarbonIdleResourceRecommender, rec.Spec.Type), nil)
		return
	}
	if rec.Spec.TargetRef.Kind != "Node" {
		ginwrapper.WriteResponse(c, fmt.Errorf(
			"drain-node only supports Node targets, got %s", rec.Spec.TargetRef.Kind), nil)
		return
	}
	if rec.Status.Action != "Delete" {
		ginwrapper.WriteResponse(c, fmt.Errorf(
			"recommendation action is %q, expected \"Delete\" — node is not a drain candidate",
			rec.Status.Action), nil)
		return
	}

	nodeName := rec.Spec.TargetRef.Name

	node := &corev1.Node{}
	if err := h.client.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		ginwrapper.WriteResponse(c, fmt.Errorf("failed to read node %s: %w", nodeName, err), nil)
		return
	}
	inst, err := providerid.ParseAzureProviderID(node.Spec.ProviderID)
	if err != nil {
		ginwrapper.WriteResponse(c, fmt.Errorf(
			"cannot determine VMSS instance for node %s: %w", nodeName, err), nil)
		return
	}

	if err := h.checkSurvivorFloor(ctx, nodeName); err != nil {
		ginwrapper.WriteResponse(c, err, nil)
		return
	}

	resp := &drainNodeResponse{
		Node: nodeName,
		Instance: &instanceInfo{
			VMSS:       inst.VMSSName,
			InstanceID: inst.InstanceID,
			RG:         inst.ResourceGroup,
		},
	}


	if err := cordonNode(ctx, h.clientset, nodeName); err != nil {
		ginwrapper.WriteResponse(c, err, nil)
		return
	}
	resp.Cordoned = true

	drain, err := drainNode(ctx, h.clientset, nodeName)
	if err != nil {
		ginwrapper.WriteResponse(c, fmt.Errorf("drain failed for node %s: %w", nodeName, err), nil)
		return
	}
	resp.Drain = drain

	if len(drain.RemainingPods) > 0 {
		resp.Message = fmt.Sprintf(
			"node cordoned and partially drained, but %d pod(s) could not be evicted (likely PodDisruptionBudget); VMSS instance NOT deleted. Resolve and retry.",
			len(drain.RemainingPods))
		klog.Warningf("drain-node: %s", resp.Message)
		ginwrapper.WriteResponse(c, nil, resp)
		return
	}

	jobName, err := createVMSSDeleteJob(ctx, h.clientset, inst, nodeName)
	if err != nil {
		resp.Message = "node drained but failed to launch VMSS delete Job: " + err.Error()
		ginwrapper.WriteResponse(c, fmt.Errorf(resp.Message), resp)
		return
	}
	resp.JobName = jobName
	resp.Message = fmt.Sprintf(
		"node %s cordoned, drained (%d pods evicted), and VMSS instance %s scheduled for deletion via Job %s",
		nodeName, len(drain.EvictedPods), inst.InstanceID, jobName)

	klog.Infof("drain-node: %s", resp.Message)
	ginwrapper.WriteResponse(c, nil, resp)
}

func (h *Handler) checkSurvivorFloor(ctx context.Context, nodeName string) error {
	nodeList := &corev1.NodeList{}
	if err := h.client.List(ctx, nodeList); err != nil {
		return fmt.Errorf("failed to list nodes for safety check: %w", err)
	}

	survivors := 0
	for i := range nodeList.Items {
		n := &nodeList.Items[i]
		if n.Name == nodeName {
			continue
		}
		if n.Spec.Unschedulable {
			continue
		}
		if !nodeReady(n) {
			continue
		}
		survivors++
	}

	if survivors < drainNodeMinSurvivors {
		return fmt.Errorf(
			"refusing to drain node %s: would leave %d Ready schedulable node(s), below the floor of %d",
			nodeName, survivors, drainNodeMinSurvivors)
	}
	return nil
}

func nodeReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

