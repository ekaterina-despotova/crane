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
	drainTimeout      = 120 * time.Second
	drainPollInterval = 5 * time.Second
)

var systemDrainNamespaces = map[string]bool{
	"kube-system": true,
}

func isSystemDrainNamespace(ns string) bool {
	return systemDrainNamespaces[ns]
}


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
	result := &drainResult{Cordoned: true}
	evicted := map[string]bool{}

	deadline := time.Now().Add(drainTimeout)
	for {
		podList, err := clientset.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
			FieldSelector: "spec.nodeName=" + nodeName,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list pods on node %s: %w", nodeName, err)
		}

		var blocking []corev1.Pod
		var systemLeft []corev1.Pod
		for i := range podList.Items {
			pod := podList.Items[i]
			if skipDrainReason(&pod) != "" {
				continue
			}
			if isSystemDrainNamespace(pod.Namespace) {
				systemLeft = append(systemLeft, pod)
				continue
			}
			blocking = append(blocking, pod)
		}


		for _, pod := range append(append([]corev1.Pod{}, blocking...), systemLeft...) {
			key := pod.Namespace + "/" + pod.Name
			if evicted[key] {
				continue
			}
			if err := evictPod(ctx, clientset, &pod); err != nil {
				klog.Warningf("drain: eviction of %s deferred: %v", key, err)
				continue
			}
			evicted[key] = true
			klog.Infof("drain: evicted %s from node %s", key, nodeName)
		}

		if len(blocking) == 0 {
			for i := range systemLeft {
				result.SkippedPods = append(result.SkippedPods,
					fmt.Sprintf("%s/%s (system pod, will be removed with the node)", systemLeft[i].Namespace, systemLeft[i].Name))
			}
			for k := range evicted {
				result.EvictedPods = append(result.EvictedPods, k)
			}
			result.RemainingPods = nil
			return result, nil
		}

		if time.Now().After(deadline) {
			for i := range blocking {
				result.RemainingPods = append(result.RemainingPods,
					fmt.Sprintf("%s/%s", blocking[i].Namespace, blocking[i].Name))
			}
			for k := range evicted {
				result.EvictedPods = append(result.EvictedPods, k)
			}
			klog.Warningf("drain: timed out after %s with %d blocking pod(s) still on node %s",
				drainTimeout, len(blocking), nodeName)
			return result, nil
		}

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(drainPollInterval):
		}
	}
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
