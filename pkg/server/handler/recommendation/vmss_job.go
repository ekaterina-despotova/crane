package recommendation

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/gocrane/crane/pkg/providerid"
)

const (
	azCredentialsSecret = "az-vmss-credentials"
	azCLIImage = "mcr.microsoft.com/azure-cli:latest"
	craneSystemNamespace = "crane-system"
	jobTTLSeconds int32 = 300
	jobBackoffLimit int32 = 2
	jobActiveDeadlineSeconds int64 = 300
)


const vmssDeleteScript = `set -euo pipefail
echo "Logging in to Azure with service principal..."
az login --service-principal \
  -u "$AZURE_CLIENT_ID" -p "$AZURE_CLIENT_SECRET" --tenant "$AZURE_TENANT_ID" >/dev/null
az account set --subscription "$AZURE_SUBSCRIPTION_ID"
echo "Deleting VMSS instance: rg=%s vmss=%s instance=%s"
az vmss delete-instances \
  --resource-group "%s" \
  --name "%s" \
  --instance-ids "%s"
echo "VMSS instance %s deleted."
`


func createVMSSDeleteJob(ctx context.Context, clientset kubernetes.Interface, inst *providerid.AzureVMSSInstance, nodeName string) (string, error) {
	script := fmt.Sprintf(vmssDeleteScript,
		inst.ResourceGroup, inst.VMSSName, inst.InstanceID,
		inst.ResourceGroup, inst.VMSSName, inst.InstanceID,
		inst.InstanceID,
	)

	jobName := fmt.Sprintf("vmss-drain-%s-%d", sanitizeName(nodeName), time.Now().Unix())
	ttl := jobTTLSeconds
	backoff := jobBackoffLimit
	deadline := jobActiveDeadlineSeconds

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: craneSystemNamespace,
			Labels: map[string]string{
				"app":                        "craned",
				"carbon.crane.io/action":     "vmss-delete-instance",
				"carbon.crane.io/node":       sanitizeName(nodeName),
				"carbon.crane.io/vmss":       sanitizeName(inst.VMSSName),
				"carbon.crane.io/instanceId": inst.InstanceID,
			},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   &deadline,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "az-vmss-delete",
							Image:   azCLIImage,
							Command: []string{"/bin/sh", "-c", script},
							EnvFrom: []corev1.EnvFromSource{
								{
									SecretRef: &corev1.SecretEnvSource{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: azCredentialsSecret,
										},
									},
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("50m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("250m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
						},
					},
				},
			},
		},
	}

	created, err := clientset.BatchV1().Jobs(craneSystemNamespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to create VMSS delete Job: %w", err)
	}
	klog.Infof("vmss: created delete Job %s/%s for node %s (%s)",
		craneSystemNamespace, created.Name, nodeName, inst.String())
	return created.Name, nil
}

func sanitizeName(s string) string {
	const max = 40
	if len(s) > max {
		s = s[:max]
	}
	return s
}
