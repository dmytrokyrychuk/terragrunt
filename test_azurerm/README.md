Test by running 

```
go run /workspaces/terragrunt/main.go [plan|apply|...]
```

**Caution**: `terragrunt plan` will trigger the remote backend initialization
(will create resource group, storage account and container).
