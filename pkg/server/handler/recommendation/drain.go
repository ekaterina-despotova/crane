package recommendation

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

type drainResult struct {
	Cordoned     bool     `json:"cordoned"`
	EvictedPods  []string `json:"evictedPods"`
	SkippedPods  []string `json:"skippedPods"`
	RemainingPods []string `json:"remainingPods"`
}


const (
	drainTimeout      = 90 * time.Second
	drainPollInterval = 3 * time.Second
)


func cordonNode(ctx context.Context, clientset kubernetes.Interface, nodeName string) error {
	patch := []byte(`{"spec":{"unschedulable":true}}`)
	_, err := clientset.CoreV1().Nodes().Patch(ctx, nodeName, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to cordon node %s: %w", nodeName, err)
	}
	klog.Infof("drain: cordoned node %s", nodeName)
	return nil
}


func drainNode(ctx context.Context, clientset kubernetes.Interface, nodeName string) (*drainResult, error) {
	result := &drainResult{}

	podList, err := clientset.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods on node %s: %w", nodeName, err)
	}

	var toEvict []corev1.Pod
	for i := range podList.Items {
		pod := podList.Items[i]
		if reason := skipDrainReason(&pod); reason != "" {
			result.SkippedPods = append(result.SkippedPods, fmt.Sprintf("%s/%s (%s)", pod.Namespace, pod.Name, reason))
			continue
		}
		toEvict = append(toEvict, pod)
	}

	for i := range toEvict {
		pod := toEvict[i]
		if err := evictPod(ctx, clientset, &pod); err != nil {
			klog.Warningf("drain: eviction of %s/%s failed: %v", pod.Namespace, pod.Name, err)
			continue
		}
		result.EvictedPods = append(result.EvictedPods, fmt.Sprintf("%s/%s", pod.Namespace, pod.Name))
		klog.Infof("drain: evicted %s/%s from node %s", pod.Namespace, pod.Name, nodeName)
	}


	result.RemainingPods = waitForPodsGone(ctx, clientset, nodeName)
	return result, nil
}


func evictPod(ctx context.Context, clientset kubernetes.Interface, pod *corev1.Pod) error {
	eviction := &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
	}
	err := clientset.PolicyV1().Evictions(pod.Namespace).Evict(ctx, eviction)
	if apierrors.IsNotFound(err) {
		return nil 
	}
	return err
}


func waitForPodsGone(ctx context.Context, clientset kubernetes.Interface, nodeName string) []string {
	deadline := time.Now().Add(drainTimeout)
	for {
		remaining := listReschedulablePods(ctx, clientset, nodeName)
		if len(remaining) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			klog.Warningf("drain: timed out after %s with %d pod(s) still on node %s", drainTimeout, len(remaining), nodeName)
			return remaining
		}
		select {
		case <-ctx.Done():
			return remaining
		case <-time.After(drainPollInterval):
		}
	}
}


func listReschedulablePods(ctx context.Context, clientset kubernetes.Interface, nodeName string) []string {
	podList, err := clientset.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName,
	})
	if err != nil {
		klog.Warningf("drain: failed to re-list pods on node %s: %v", nodeName, err)
		return nil
	}
	var remaining []string
	for i := range podList.Items {
		pod := podList.Items[i]
		if skipDrainReason(&pod) != "" {
			continue
		}
		remaining = append(remaining, fmt.Sprintf("%s/%s", pod.Namespace, pod.Name))
	}
	return remaining
}


func skipDrainReason(pod *corev1.Pod) string {
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return "terminal"
	}
	if _, isMirror := pod.Annotations[corev1.MirrorPodAnnotationKey]; isMirror {
		return "mirror"
	}
	for _, ref := range pod.OwnerReferences {
		if ref.Controller != nil && *ref.Controller && ref.Kind == "DaemonSet" {
			return "daemonset"
		}
	}
	return ""
}
