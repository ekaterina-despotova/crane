package providerid

import "testing"

func TestParseAzureProviderID_Valid(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		want       AzureVMSSInstance
	}{
		{
			name:       "typical AKS node (triple slash)",
			providerID: "azure:///subscriptions/a9ec8477-bcbd-46d5-8abe-9cd2f0bf30d7/resourceGroups/MC_demoCarbonrg_demo-carbon-aks_swedencentral/providers/Microsoft.Compute/virtualMachineScaleSets/aks-nodepool1-12345678-vmss/virtualMachines/2",
			want: AzureVMSSInstance{
				SubscriptionID: "a9ec8477-bcbd-46d5-8abe-9cd2f0bf30d7",
				ResourceGroup:  "MC_demoCarbonrg_demo-carbon-aks_swedencentral",
				VMSSName:       "aks-nodepool1-12345678-vmss",
				InstanceID:     "2",
			},
		},
		{
			name:       "instance id 0",
			providerID: "azure:///subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachineScaleSets/vmss1/virtualMachines/0",
			want: AzureVMSSInstance{
				SubscriptionID: "sub1",
				ResourceGroup:  "rg1",
				VMSSName:       "vmss1",
				InstanceID:     "0",
			},
		},
		{
			name:       "case-insensitive segment keys",
			providerID: "azure:///Subscriptions/sub1/resourcegroups/rg1/Providers/microsoft.compute/VirtualMachineScaleSets/vmss1/VirtualMachines/5",
			want: AzureVMSSInstance{
				SubscriptionID: "sub1",
				ResourceGroup:  "rg1",
				VMSSName:       "vmss1",
				InstanceID:     "5",
			},
		},
		{
			name:       "no leading slash after scheme",
			providerID: "azure://subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachineScaleSets/vmss1/virtualMachines/11",
			want: AzureVMSSInstance{
				SubscriptionID: "sub1",
				ResourceGroup:  "rg1",
				VMSSName:       "vmss1",
				InstanceID:     "11",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAzureProviderID(tt.providerID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if *got != tt.want {
				t.Errorf("mismatch\n got: %+v\nwant: %+v", *got, tt.want)
			}
		})
	}
}

func TestParseAzureProviderID_Errors(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"not azure (aws)", "aws:///us-east-1a/i-0abc123"},
		{"not azure (gce)", "gce://project/zone/instance"},
		{"single-slash scheme is not azure://", "azure:/subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachineScaleSets/vmss1/virtualMachines/7"},
		{
			name:       "plain VM, not VMSS",
			providerID: "azure:///subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachines/myvm",
		},
		{
			name:       "truncated before instance id",
			providerID: "azure:///subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachineScaleSets/vmss1/virtualMachines",
		},
		{
			name:       "wrong provider namespace",
			providerID: "azure:///subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Network/virtualMachineScaleSets/vmss1/virtualMachines/0",
		},
		{
			name:       "missing instance id value",
			providerID: "azure:///subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachineScaleSets/vmss1/virtualMachines/",
		},
		{
			name:       "garbage",
			providerID: "azure:///this/is/not/a/valid/azure/resource/id/at/all/really",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAzureProviderID(tt.providerID)
			if err == nil {
				t.Fatalf("expected error, got nil (parsed %+v)", got)
			}
			if got != nil {
				t.Errorf("expected nil result on error, got %+v", got)
			}
		})
	}
}

func TestAzureVMSSInstance_String(t *testing.T) {
	inst := &AzureVMSSInstance{
		SubscriptionID: "sub1",
		ResourceGroup:  "rg1",
		VMSSName:       "vmss1",
		InstanceID:     "3",
	}
	want := "vmss=vmss1 instance=3 rg=rg1 sub=sub1"
	if got := inst.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
