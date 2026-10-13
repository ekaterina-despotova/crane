package carbonidle

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/gocrane/crane/pkg/recommendation/framework"
)

const defaultHeadroomFraction = 0.15

type nodeCapacity struct {
	name string

	allocatableCPUMilli int64
	allocatableMemBytes int64

	requestedCPUMilli int64
	requestedMemBytes int64

	reschedulableCPUMilli int64
	reschedulableMemBytes int64

	ready       bool
	schedulable bool
}

type consolidationDecision struct {
	Feasible      bool   `json:"feasible"`
	CandidateNode string `json:"candidateNode"`
	Reason        string `json:"reason"`

	HeadroomFraction float64 `json:"headroomFraction"`

	SchedulableNodes int `json:"schedulableNodes"`
	SurvivorNodes int `json:"survivorNodes"`

	MoveCPUMilli int64 `json:"moveCpuMilli"`
	MoveMemBytes int64 `json:"moveMemBytes"`

	RemainingAllocatableCPUMilli int64 `json:"remainingAllocatableCpuMilli"`
	RemainingAllocatableMemBytes int64 `json:"remainingAllocatableMemBytes"`
	RemainingRequestedCPUMilli   int64 `json:"remainingRequestedCpuMilli"`
	RemainingRequestedMemBytes   int64 `json:"remainingRequestedMemBytes"`

	UsableCPUMilli int64 `json:"usableCpuMilli"`
	UsableMemBytes int64 `json:"usableMemBytes"`

	ProjectedCPUMilli int64 `json:"projectedCpuMilli"`
	ProjectedMemBytes int64 `json:"projectedMemBytes"`

	FreedNodeWatts float64 `json:"freedNodeWatts"`

	IsBestCandidate    bool   `json:"isBestCandidate"`
	Rank               int    `json:"rank"`
	BetterCandidate    string `json:"betterCandidate,omitempty"`
	FeasibleCandidates int    `json:"feasibleCandidates"`
}

func (r *CarbonIdleResourceRecommender) evaluateConsolidation(ctx *framework.RecommendationContext) (*consolidationDecision, error) {
	candidateName := ctx.Recommendation.Spec.TargetRef.Name

	nodes, err := r.buildClusterCapacity(ctx)
	if err != nil {
		return nil, err
	}

	cand, ok := nodes[candidateName]
	if !ok {
		return nil, fmt.Errorf("candidate node %q not found in cluster capacity map", candidateName)
	}

	var survivors []*nodeCapacity
	schedulableTotal := 0
	for _, n := range nodes {
		if !(n.ready && n.schedulable) {
			continue
		}
		schedulableTotal++
		if n.name != candidateName {
			survivors = append(survivors, n)
		}
	}

	decision := &consolidationDecision{
		CandidateNode:    candidateName,
		HeadroomFraction: r.headroomFraction,
		SchedulableNodes: schedulableTotal,
		SurvivorNodes:    len(survivors),
		MoveCPUMilli:     cand.reschedulableCPUMilli,
		MoveMemBytes:     cand.reschedulableMemBytes,
		FreedNodeWatts:   r.pickFreedNodeWatts(ctx),
	}

	if schedulableTotal <= int(r.minNodes) {
		decision.Feasible = false
		decision.Reason = fmt.Sprintf(
			"cluster has %d schedulable node(s), at or below the configured floor of %d — refusing to consolidate",
			schedulableTotal, r.minNodes)
		return decision, nil
	}

	if len(survivors) == 0 {
		decision.Feasible = false
		decision.Reason = "no other Ready, schedulable node is available to absorb the candidate's workload"
		return decision, nil
	}

	var remAllocCPU, remAllocMem, remReqCPU, remReqMem int64
	for _, s := range survivors {
		remAllocCPU += s.allocatableCPUMilli
		remAllocMem += s.allocatableMemBytes
		remReqCPU += s.requestedCPUMilli
		remReqMem += s.requestedMemBytes
	}

	usableCPU := int64(float64(remAllocCPU) * (1.0 - r.headroomFraction))
	usableMem := int64(float64(remAllocMem) * (1.0 - r.headroomFraction))

	projCPU := remReqCPU + cand.reschedulableCPUMilli
	projMem := remReqMem + cand.reschedulableMemBytes

	decision.RemainingAllocatableCPUMilli = remAllocCPU
	decision.RemainingAllocatableMemBytes = remAllocMem
	decision.RemainingRequestedCPUMilli = remReqCPU
	decision.RemainingRequestedMemBytes = remReqMem
	decision.UsableCPUMilli = usableCPU
	decision.UsableMemBytes = usableMem
	decision.ProjectedCPUMilli = projCPU
	decision.ProjectedMemBytes = projMem

	cpuFits := projCPU <= usableCPU
	memFits := projMem <= usableMem

	if cpuFits && memFits {
		decision.Feasible = true

		r.rankCandidate(nodes, candidateName, decision)

		base := fmt.Sprintf(
			"draining node %s is feasible: the %d survivor node(s) can absorb its %dm CPU / %s of reschedulable requests "+
				"within a %.0f%% headroom reserve (projected CPU %dm of %dm usable; projected memory %s of %s usable)",
			candidateName, len(survivors),
			cand.reschedulableCPUMilli, humanizeBytes(cand.reschedulableMemBytes),
			r.headroomFraction*100,
			projCPU, usableCPU,
			humanizeBytes(projMem), humanizeBytes(usableMem))

		if decision.IsBestCandidate {
			if decision.FeasibleCandidates > 1 {
				decision.Reason = base + fmt.Sprintf(
					"; this is the most under-utilized of %d drainable node(s) — recommended to drain this one",
					decision.FeasibleCandidates)
			} else {
				decision.Reason = base
			}
		} else {
			decision.Reason = base + fmt.Sprintf(
				"; however node %s is more under-utilized and is the recommended drain target instead",
				decision.BetterCandidate)
		}
		return decision, nil
	}

	var failed []string
	if !cpuFits {
		failed = append(failed, fmt.Sprintf(
			"CPU: need %dm but only %dm usable after %.0f%% headroom (survivor allocatable %dm, already requested %dm)",
			projCPU, usableCPU, r.headroomFraction*100, remAllocCPU, remReqCPU))
	}
	if !memFits {
		failed = append(failed, fmt.Sprintf(
			"memory: need %s but only %s usable after %.0f%% headroom (survivor allocatable %s, already requested %s)",
			humanizeBytes(projMem), humanizeBytes(usableMem), r.headroomFraction*100,
			humanizeBytes(remAllocMem), humanizeBytes(remReqMem)))
	}
	decision.Feasible = false
	decision.Reason = fmt.Sprintf(
		"draining node %s is not feasible — survivors cannot fit the load: %s",
		candidateName, strings.Join(failed, "; "))
	return decision, nil
}

func (r *CarbonIdleResourceRecommender) rankCandidate(
	nodes map[string]*nodeCapacity, candidateName string, decision *consolidationDecision,
) {
	type cand struct {
		name    string
		cpu     int64
		mem     int64
	}
	var feasible []cand
	for name, n := range nodes {
		if !(n.ready && n.schedulable) {
			continue
		}
		if r.nodeDrainFeasible(nodes, name) {
			feasible = append(feasible, cand{name: name, cpu: n.reschedulableCPUMilli, mem: n.reschedulableMemBytes})
		}
	}

	decision.FeasibleCandidates = len(feasible)
	if len(feasible) == 0 {
		return
	}

	sort.Slice(feasible, func(i, j int) bool {
		if feasible[i].cpu != feasible[j].cpu {
			return feasible[i].cpu < feasible[j].cpu
		}
		if feasible[i].mem != feasible[j].mem {
			return feasible[i].mem < feasible[j].mem
		}
		return feasible[i].name < feasible[j].name
	})

	best := feasible[0].name
	for i, c := range feasible {
		if c.name == candidateName {
			decision.Rank = i + 1
			break
		}
	}
	decision.IsBestCandidate = (candidateName == best)
	if !decision.IsBestCandidate {
		decision.BetterCandidate = best
	}
}

func (r *CarbonIdleResourceRecommender) nodeDrainFeasible(nodes map[string]*nodeCapacity, nodeName string) bool {
	cand, ok := nodes[nodeName]
	if !ok || !cand.ready || !cand.schedulable {
		return false
	}

	schedulableTotal := 0
	var remAllocCPU, remAllocMem, remReqCPU, remReqMem int64
	for name, n := range nodes {
		if !(n.ready && n.schedulable) {
			continue
		}
		schedulableTotal++
		if name == nodeName {
			continue
		}
		remAllocCPU += n.allocatableCPUMilli
		remAllocMem += n.allocatableMemBytes
		remReqCPU += n.requestedCPUMilli
		remReqMem += n.requestedMemBytes
	}

	if schedulableTotal <= int(r.minNodes) {
		return false
	}
	if remAllocCPU == 0 { 
		return false
	}

	usableCPU := int64(float64(remAllocCPU) * (1.0 - r.headroomFraction))
	usableMem := int64(float64(remAllocMem) * (1.0 - r.headroomFraction))
	projCPU := remReqCPU + cand.reschedulableCPUMilli
	projMem := remReqMem + cand.reschedulableMemBytes
	return projCPU <= usableCPU && projMem <= usableMem
}

func (r *CarbonIdleResourceRecommender) buildClusterCapacity(ctx *framework.RecommendationContext) (map[string]*nodeCapacity, error) {
	nodeList := &corev1.NodeList{}
	if err := ctx.Client.List(context.TODO(), nodeList); err != nil {
		return nil, fmt.Errorf("failed to list nodes: %v", err)
	}

	nodes := make(map[string]*nodeCapacity, len(nodeList.Items))
	for i := range nodeList.Items {
		n := &nodeList.Items[i]
		nodes[n.Name] = &nodeCapacity{
			name:                n.Name,
			allocatableCPUMilli: n.Status.Allocatable.Cpu().MilliValue(),
			allocatableMemBytes: n.Status.Allocatable.Memory().Value(),
			ready:               isNodeReady(n),
			schedulable:         !n.Spec.Unschedulable,
		}
	}

	podList := &corev1.PodList{}
	if err := ctx.Client.List(context.TODO(), podList); err != nil {
		return nil, fmt.Errorf("failed to list pods: %v", err)
	}

	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Spec.NodeName == "" {
			continue 
		}
		nc, ok := nodes[pod.Spec.NodeName]
		if !ok {
			continue 
		}
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}

		cpu, mem := podRequests(pod)
		nc.requestedCPUMilli += cpu
		nc.requestedMemBytes += mem

		if podReschedulable(pod) {
			nc.reschedulableCPUMilli += cpu
			nc.reschedulableMemBytes += mem
		}
	}

	return nodes, nil
}

func podRequests(pod *corev1.Pod) (cpuMilli int64, memBytes int64) {
	for i := range pod.Spec.Containers {
		req := pod.Spec.Containers[i].Resources.Requests
		cpuMilli += req.Cpu().MilliValue()
		memBytes += req.Memory().Value()
	}

	var initCPU, initMem int64
	for i := range pod.Spec.InitContainers {
		req := pod.Spec.InitContainers[i].Resources.Requests
		if c := req.Cpu().MilliValue(); c > initCPU {
			initCPU = c
		}
		if m := req.Memory().Value(); m > initMem {
			initMem = m
		}
	}
	if initCPU > cpuMilli {
		cpuMilli = initCPU
	}
	if initMem > memBytes {
		memBytes = initMem
	}
	return cpuMilli, memBytes
}

func podReschedulable(pod *corev1.Pod) bool {
	if _, isMirror := pod.Annotations[corev1.MirrorPodAnnotationKey]; isMirror {
		return false
	}
	for _, ref := range pod.OwnerReferences {
		if ref.Controller != nil && *ref.Controller && ref.Kind == "DaemonSet" {
			return false
		}
	}
	return true
}

func isNodeReady(node *corev1.Node) bool {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (r *CarbonIdleResourceRecommender) pickFreedNodeWatts(ctx *framework.RecommendationContext) float64 {
	wattsList := ctx.InputValue(keyNodeCPUWatts)
	if len(wattsList) == 0 || len(wattsList[0].Samples) == 0 {
		return 0
	}
	return avgSamples(wattsList[0].Samples)
}

func humanizeBytes(b int64) string {
	return resource.NewQuantity(b, resource.BinarySI).String()
}
