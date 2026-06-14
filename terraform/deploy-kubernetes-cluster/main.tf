resource "azurerm_resource_group" "Demo_Carbon" {
    name        = "demoCarbonrg"
    location    = var.region 
}

resource "azurerm_kubernetes_cluster" "aks" {
    name                = "demo-carbon-aks"
    location            = azurerm_resource_group.Demo_Carbon.location
    resource_group_name = azurerm_resource_group.Demo_Carbon.name
    dns_prefix          = "demo-carbon-aks"

    default_node_pool {
        name       = "default"
        node_count = 2
        vm_size    = "Standard_B2s_v2"
    }

    identity {
        type = "SystemAssigned"
    }
}

output "Resource_group_name"{
    value = azurerm_resource_group.Demo_Carbon.name

}

output "K8s_aks_name"{
    value = azurerm_kubernetes_cluster.aks.name
}