terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }

  backend "azurerm" {
    storage_account_name = "terrytfstatestorageacc"
    container_name = "terrytfstatecontainer"
    key = "akstfstate.tfstate"
  } 
  required_version = ">= 1.1.0"
}

provider "azurerm" {
  subscription_id = var.subscription_id
  resource_provider_registrations = "none"
  features {}
}