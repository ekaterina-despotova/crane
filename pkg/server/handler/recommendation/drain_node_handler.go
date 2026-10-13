package recommendation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	analysisapi "github.com/gocrane/api/analysis/v1alpha1"

	"github.com/gocrane/crane/pkg/providerid"
	"github.com/gocrane/crane/pkg/recommendation/recommender"
	"github.com/gocrane/crane/pkg/server/ginwrapper"
)

const drainNodeMinSurvivors = 1

const (
	phaseCordoning = "cordoning"
	phaseDraining  = "draining"
	phaseDeleting  = "deleting"
	phaseCompleted = "completed"
	phaseFailed    = "failed"
)

type drainJob struct {
	Node     string        `json:"node"`
	Phase    string        `json:"phase"`
	Message  string        `json:"message"`
	JobName  string        `json:"jobName"`
	Instance *instanceInfo `json:"instance"`
	Drain    *drainResult  `json:"drain,omitempty"`
	Started  time.Time     `json:"started"`
}

type drainTracker struct {
	mu   sync.RWMutex
	jobs map[string]*drainJob
}

var tracker = &drainTracker{jobs: make(map[string]*drainJob)}

func (t *drainTracker) set(j *drainJob) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.jobs[j.Node] = j
}

func (t *drainTracker) update(node string, fn func(*drainJob)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if j, ok := t.jobs[node]; ok {
		fn(j)
	}
}

func (t *drainTracker) get(node string) *drainJob {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.jobs[node]
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

	if existing := tracker.get(nodeName); existing != nil &&
		existing.Phase != phaseCompleted && existing.Phase != phaseFailed {
		ginwrapper.WriteResponse(c, nil, existing)
		return
	}

	job := &drainJob{
		Node:    nodeName,
		Phase:   phaseCordoning,
		Message: "starting drain",
		Started: time.Now(),
		Instance: &instanceInfo{
			VMSS:       inst.VMSSName,
			InstanceID: inst.InstanceID,
			RG:         inst.ResourceGroup,
		},
	}
	tracker.set(job)

	go h.runDrain(context.Background(), nodeName, inst)

	c.JSON(202, gin.H{"data": job, "error": ""})
}

func (h *Handler) runDrain(ctx context.Context, nodeName string, inst *providerid.AzureVMSSInstance) {
	tracker.update(nodeName, func(j *drainJob) { j.Phase = phaseCordoning; j.Message = "cordoning node" })
	if err := cordonNode(ctx, h.clientset, nodeName); err != nil {
		tracker.update(nodeName, func(j *drainJob) {
			j.Phase = phaseFailed
			j.Message = "cordon failed: " + err.Error()
		})
		klog.Errorf("drain-node: cordon failed for %s: %v", nodeName, err)
		return
	}

	tracker.update(nodeName, func(j *drainJob) { j.Phase = phaseDraining; j.Message = "evicting pods" })
	drain, err := drainNode(ctx, h.clientset, nodeName)
	if err != nil {
		tracker.update(nodeName, func(j *drainJob) {
			j.Phase = phaseFailed
			j.Message = "drain failed: " + err.Error()
		})
		klog.Errorf("drain-node: drain failed for %s: %v", nodeName, err)
		return
	}
	tracker.update(nodeName, func(j *drainJob) { j.Drain = drain })

	if len(drain.RemainingPods) > 0 {
		msg := fmt.Sprintf(
			"node cordoned and partially drained, but %d pod(s) could not be evicted (likely PodDisruptionBudget); VMSS instance NOT deleted",
			len(drain.RemainingPods))
		tracker.update(nodeName, func(j *drainJob) { j.Phase = phaseFailed; j.Message = msg })
		klog.Warningf("drain-node: %s", msg)
		return
	}

	tracker.update(nodeName, func(j *drainJob) { j.Phase = phaseDeleting; j.Message = "deleting VMSS instance" })
	jobName, err := createVMSSDeleteJob(ctx, h.clientset, inst, nodeName)
	if err != nil {
		tracker.update(nodeName, func(j *drainJob) {
			j.Phase = phaseFailed
			j.Message = "failed to launch VMSS delete Job: " + err.Error()
		})
		klog.Errorf("drain-node: %v", err)
		return
	}
	tracker.update(nodeName, func(j *drainJob) {
		j.JobName = jobName
		j.Message = fmt.Sprintf("VMSS instance %s deletion in progress (Job %s)", inst.InstanceID, jobName)
	})
	klog.Infof("drain-node: node %s drained (%d evicted), delete Job %s running",
		nodeName, len(drain.EvictedPods), jobName)
}

func (h *Handler) DrainNodeStatus(c *gin.Context) {
	ctx := context.TODO()
	nodeName := c.Param("nodeName")

	job := tracker.get(nodeName)

	node := &corev1.Node{}
	nodeErr := h.client.Get(ctx, types.NamespacedName{Name: nodeName}, node)
	nodeGone := apierrors.IsNotFound(nodeErr)

	if job == nil {
		if nodeGone {
			ginwrapper.WriteResponse(c, nil, &drainJob{
				Node: nodeName, Phase: phaseCompleted,
				Message: "node no longer exists in the cluster",
			})
			return
		}
		ginwrapper.WriteResponse(c, nil, &drainJob{
			Node: nodeName, Phase: "unknown",
			Message: "no drain in progress for this node",
		})
		return
	}

	if job.Phase == phaseDeleting {
		if nodeGone {
			tracker.update(nodeName, func(j *drainJob) {
				j.Phase = phaseCompleted
				j.Message = "VMSS instance deleted; node removed from cluster"
			})
		} else if job.JobName != "" {
			if done, failed, jerr := h.jobTerminalState(ctx, job.JobName); jerr == nil {
				if failed {
					tracker.update(nodeName, func(j *drainJob) {
						j.Phase = phaseFailed
						j.Message = "VMSS delete Job failed; check Job logs"
					})
				} else if done {
					tracker.update(nodeName, func(j *drainJob) {
						j.Phase = phaseCompleted
						j.Message = "VMSS instance deleted"
					})
				}
			}
		}
	}

	ginwrapper.WriteResponse(c, nil, tracker.get(nodeName))
}

func (h *Handler) jobTerminalState(ctx context.Context, jobName string) (done bool, failed bool, err error) {
	j, err := h.clientset.BatchV1().Jobs(craneSystemNamespace).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return true, false, nil
		}
		return false, false, err
	}
	if j.Status.Succeeded > 0 {
		return true, false, nil
	}
	if j.Status.Failed > 0 {
		return false, true, nil
	}
	return false, false, nil
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
