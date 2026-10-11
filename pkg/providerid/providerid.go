package providerid

import (
	"fmt"
	"strings"
)

type AzureVMSSInstance struct {
	SubscriptionID string
	ResourceGroup string
	VMSSName string
	InstanceID string
}

const azureScheme = "azure://"

const (
	segSubscriptions = "subscriptions"
	segResourceGroup = "resourcegroups"
	segProviders     = "providers"
	segProviderNS    = "microsoft.compute"
	segVMSS          = "virtualmachinescalesets"
	segVMs           = "virtualmachines"
)


func ParseAzureProviderID(providerID string) (*AzureVMSSInstance, error) {
	if strings.TrimSpace(providerID) == "" {
		return nil, fmt.Errorf("providerID is empty: the node has no spec.providerID")
	}

	if !strings.HasPrefix(providerID, azureScheme) {
		return nil, fmt.Errorf("providerID %q is not an Azure provider (expected prefix %q); only AKS VMSS nodes are supported", providerID, azureScheme)
	}


	path := strings.TrimPrefix(providerID, azureScheme)
	path = strings.TrimLeft(path, "/")

	segments := strings.Split(path, "/")
	if len(segments) < 10 {
		return nil, fmt.Errorf("providerID %q is not a VMSS instance path (expected .../virtualMachineScaleSets/<name>/virtualMachines/<id>)", providerID)
	}


	if err := expectKey(segments, 0, segSubscriptions, providerID); err != nil {
		return nil, err
	}
	if err := expectKey(segments, 2, segResourceGroup, providerID); err != nil {
		return nil, err
	}
	if err := expectKey(segments, 4, segProviders, providerID); err != nil {
		return nil, err
	}
	if err := expectKey(segments, 5, segProviderNS, providerID); err != nil {
		return nil, err
	}
	if err := expectKey(segments, 6, segVMSS, providerID); err != nil {
		return nil, err
	}
	if err := expectKey(segments, 8, segVMs, providerID); err != nil {
		return nil, err
	}

	inst := &AzureVMSSInstance{
		SubscriptionID: segments[1],
		ResourceGroup:  segments[3],
		VMSSName:       segments[7],
		InstanceID:     segments[9],
	}

	if inst.SubscriptionID == "" || inst.ResourceGroup == "" || inst.VMSSName == "" || inst.InstanceID == "" {
		return nil, fmt.Errorf("providerID %q is missing one of subscription/resourceGroup/vmss/instanceID", providerID)
	}

	return inst, nil
}

func expectKey(segments []string, idx int, want, providerID string) error {
	if idx >= len(segments) {
		return fmt.Errorf("providerID %q is truncated: expected %q at segment %d", providerID, want, idx)
	}
	if !strings.EqualFold(segments[idx], want) {
		return fmt.Errorf("providerID %q has unexpected segment %q at position %d (expected %q)", providerID, segments[idx], idx, want)
	}
	return nil
}

func (a *AzureVMSSInstance) String() string {
	return fmt.Sprintf("vmss=%s instance=%s rg=%s sub=%s", a.VMSSName, a.InstanceID, a.ResourceGroup, a.SubscriptionID)
}
