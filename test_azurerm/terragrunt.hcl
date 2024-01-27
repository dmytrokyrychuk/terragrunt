remote_state {
    backend = "azurerm"
    generate = {
        path = "backend.tf"
        if_exists = "overwrite_terragrunt"
    }
    config = {
        subscription_id = "b4f7ead0-14f6-453a-b41b-e4c8df68890d"
        resource_group_name = "dmytro-terragrunt-statefiles"
        resource_group_location = "North Europe"
        storage_account_name = "dmytro8gsd97yg"
        storage_account_location = "North Europe"
        container_name = "statefiles"
        key = "${path_relative_to_include()}/terraform.tfstate"
    }
}