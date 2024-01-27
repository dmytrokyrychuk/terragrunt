package remote

import (
	"context"
	stderrors "errors"
	"reflect"
	"strconv"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/gruntwork-io/go-commons/errors"
	"github.com/gruntwork-io/terragrunt/options"

	"github.com/gruntwork-io/terragrunt/util"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/mitchellh/mapstructure"
)

type ExtendedRemoteStateConfigAzureRM struct {
	remoteStateConfigAzureRM RemoteStateConfigAzureRM

	// Location is only used during creation of the resource group, location cannot be updated
	ResourceGroupLocation string `mapstructure:"resource_group_location"`
	// Defaults to the resource group location if not specified
	StorageAccountLocation string `mapstructure:"storage_account_location"`

	SkipResourceGroupCreation  bool `mapstructure:"skip_resource_group_creation"`
	SkipStorageAccountCreation bool `mapstructure:"skip_storage_account_creation"`
	SkipContainerCreation      bool `mapstructure:"skip_container_creation"`
}

var terragruntAzureRMOnlyConfigs = []string{
	"resource_group_location",
	"storage_account_location",
	"skip_resource_group_creation",
	"skip_storage_account_creation",
	"skip_container_creation",
}

type RemoteStateConfigAzureRM struct {
	TenantID           string `mapstructure:"tenant_id"`
	SubscriptionID     string `mapstructure:"subscription_id"`
	ResourceGroupName  string `mapstructure:"resource_group_name"`
	StorageAccountName string `mapstructure:"storage_account_name"`
	ContainerName      string `mapstructure:"container_name"`
	Key                string `mapstructure:"key"`
}

type AzureRMInitializer struct{}

// Retuns true if:
//
// 1. Any of the existing backend settings are different from the current config
// 2. The configured resource group does not exist
// 3. The configured storage account does not exist
// 4. The configured container does not exist
func (azurermInitializer AzureRMInitializer) NeedsInitialization(remoteState *RemoteState, existingBackend *TerraformBackend, terragruntOptions *options.TerragruntOptions) (bool, error) {
	if !azurermConfigValuesEqual(remoteState.Config, existingBackend, terragruntOptions) {
		return true, nil
	}

	config, err := parseAzureRMConfig(remoteState.Config)
	if err != nil {
		return false, err
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return false, err
	}
	ctx := context.Background()

	resourcesClientFactory, err := armresources.NewClientFactory(config.SubscriptionID, cred, nil)
	if err != nil {
		return false, err
	}

	resourceGroupClient := resourcesClientFactory.NewResourceGroupsClient()
	resourceGroupExistenceResponse, err := resourceGroupClient.CheckExistence(ctx, config.ResourceGroupName, nil)
	if err != nil {
		return false, err
	}
	if !resourceGroupExistenceResponse.Success {
		return true, nil
	}

	// authorizer, err := auth.NewAuthorizerFromEnvironment()
	// if err != nil {
	// 	return false, err
	// }

	// TODO: if resource group doesn't exist, return true

	// TODO: if storage account doesn't exist, return true

	// TODO: if container doesn't exist, return true

	return true, nil // FIXME: return true for testing to trigger init every time
	return false, nil
}

func azurermConfigValuesEqual(config map[string]interface{}, existingBackend *TerraformBackend, terragruntOptions *options.TerragruntOptions) bool {
	if existingBackend == nil {
		return len(config) == 0
	}

	if existingBackend.Type != "azurerm" {
		terragruntOptions.Logger.Debugf("Backend type has changed from azurerm to %s", existingBackend.Type)
		return false
	}

	if len(config) == 0 && len(existingBackend.Config) == 0 {
		return true
	}

	// If other keys in config are bools, DeepEqual also will consider the maps to be different.
	for key, value := range existingBackend.Config {
		if util.KindOf(existingBackend.Config[key]) == reflect.String && util.KindOf(config[key]) == reflect.Bool {
			if convertedValue, err := strconv.ParseBool(value.(string)); err == nil {
				existingBackend.Config[key] = convertedValue
			}
		}
	}

	// Construct a new map excluding custom AzureRM labels that are only used in Terragrunt config and not in Terraform's backend
	comparisonConfig := make(map[string]interface{})
	for key, value := range config {
		comparisonConfig[key] = value
	}

	for _, key := range terragruntAzureRMOnlyConfigs {
		delete(comparisonConfig, key)
	}

	if !terraformStateConfigEqual(existingBackend.Config, comparisonConfig) {
		terragruntOptions.Logger.Debugf(("Backend config changed from %s to %s"), existingBackend.Config, config)
		return false
	}

	return true
}

func (azurermInitializer AzureRMInitializer) Initialize(remoteState *RemoteState, terragruntOptions *options.TerragruntOptions) error {
	azurermConfigExtended, err := parseExtendedAzureRMConfig(remoteState.Config)
	if err != nil {
		return err
	}

	var azurermConfig = azurermConfigExtended.remoteStateConfigAzureRM

	// ensure that only one goroutine can initialize the storage account
	return stateAccessLock.StateBucketUpdate(azurermConfig.StorageAccountName, func() error {
		cred, err := azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			return err
		}
		ctx := context.Background()

		resourcesClientFactory, err := armresources.NewClientFactory(azurermConfig.SubscriptionID, cred, nil)
		if err != nil {
			return err
		}

		var resourceGroup *armresources.ResourceGroup
		if !azurermConfigExtended.SkipResourceGroupCreation {
			if resourceGroup, err = createResourceGroupIfNeeded(ctx, azurermConfig.ResourceGroupName, azurermConfigExtended.ResourceGroupLocation, resourcesClientFactory); err != nil {
				return err
			}
		} else {
			if resourceGroup, err = getResourceGroupByName(ctx, azurermConfig.ResourceGroupName, resourcesClientFactory); err != nil {
				return err
			}
		}

		storageClientFactory, err := armstorage.NewClientFactory(azurermConfig.SubscriptionID, cred, nil)
		if err != nil {
			return err
		}

		if !azurermConfigExtended.SkipStorageAccountCreation {
			location := azurermConfigExtended.StorageAccountLocation
			if location == "" {
				location = *resourceGroup.Location
			}
			if err := createStorageAccountIfNeeded(ctx, azurermConfig.ResourceGroupName, azurermConfig.StorageAccountName, location, storageClientFactory); err != nil {
				return err
			}
		}

		if !azurermConfigExtended.SkipContainerCreation {
			if err := createBlobContainerIfNeeded(ctx, azurermConfig.ResourceGroupName, azurermConfig.StorageAccountName, azurermConfig.ContainerName, storageClientFactory); err != nil {
				return err
			}
		}

		return nil
	})
}

func parseAzureRMConfig(config map[string]interface{}) (*RemoteStateConfigAzureRM, error) {
	var azurermConfig RemoteStateConfigAzureRM
	if err := mapstructure.Decode(config, &azurermConfig); err != nil {
		return nil, errors.WithStackTrace(err)
	}

	return &azurermConfig, nil
}

func parseExtendedAzureRMConfig(config map[string]interface{}) (*ExtendedRemoteStateConfigAzureRM, error) {
	var azurermConfig RemoteStateConfigAzureRM
	var extendedConfig ExtendedRemoteStateConfigAzureRM

	if err := mapstructure.Decode(config, &azurermConfig); err != nil {
		return nil, errors.WithStackTrace(err)
	}

	if err := mapstructure.Decode(config, &extendedConfig); err != nil {
		return nil, errors.WithStackTrace(err)
	}

	extendedConfig.remoteStateConfigAzureRM = azurermConfig
	return &extendedConfig, nil
}

func createResourceGroupIfNeeded(ctx context.Context, resourceGroupName string, resourceGroupLocation string, resourcesClientFactory *armresources.ClientFactory) (*armresources.ResourceGroup, error) {
	resourceGroupClient := resourcesClientFactory.NewResourceGroupsClient()
	resourceGroupResponse, err := resourceGroupClient.CreateOrUpdate(ctx, resourceGroupName, armresources.ResourceGroup{
		Location: &resourceGroupLocation,
	}, nil)
	if err != nil {
		return nil, err
	}

	return &resourceGroupResponse.ResourceGroup, nil
}

func getResourceGroupByName(ctx context.Context, resourceGroupName string, resourcesClientFactory *armresources.ClientFactory) (*armresources.ResourceGroup, error) {
	resourceGroupClient := resourcesClientFactory.NewResourceGroupsClient()
	resourceGroupResponse, err := resourceGroupClient.Get(ctx, resourceGroupName, nil)
	if err != nil {
		return nil, err
	}

	return &resourceGroupResponse.ResourceGroup, nil
}

func createStorageAccountIfNeeded(ctx context.Context, resourceGroupName string, storageAccountName string, location string, storageClientFactory *armstorage.ClientFactory) error {
	storageAccountsClient := storageClientFactory.NewAccountsClient()

	checkResponse, err := storageAccountsClient.CheckNameAvailability(ctx, armstorage.AccountCheckNameAvailabilityParameters{
		Name: &storageAccountName,
	}, nil)
	if err != nil {
		return err
	}

	// If name is available, then the storage account doesn't exist, and we create it here
	if *checkResponse.NameAvailable {
		pollerResponse, err := storageAccountsClient.BeginCreate(ctx, resourceGroupName, storageAccountName, armstorage.AccountCreateParameters{
			Kind:     to.Ptr(armstorage.KindStorageV2),
			Location: &location,
			SKU: &armstorage.SKU{
				Name: to.Ptr(armstorage.SKUNameStandardLRS),
			},
		}, nil)
		if err != nil {
			return err
		}

		_, err = pollerResponse.PollUntilDone(ctx, nil)
		if err != nil {
			return err
		}
	}

	return nil
}

func createBlobContainerIfNeeded(ctx context.Context, resourceGroupName string, storageAccountName string, containerName string, storageClientFactory *armstorage.ClientFactory) error {
	blobContainerClient := storageClientFactory.NewBlobContainersClient()

	var containerExists = true
	_, err := blobContainerClient.Get(ctx, resourceGroupName, storageAccountName, containerName, nil)
	if err != nil {
		var responseErr *azcore.ResponseError
		if !stderrors.As(err, &responseErr) {
			return err
		}

		if responseErr.StatusCode == 404 {
			containerExists = false
		}
	}

	if !containerExists {
		_, err := blobContainerClient.Create(ctx, resourceGroupName, storageAccountName, containerName, armstorage.BlobContainer{
			ContainerProperties: &armstorage.ContainerProperties{
				PublicAccess: to.Ptr(armstorage.PublicAccessNone),
			},
		}, nil)
		if err != nil {
			return err
		}
	}
	return nil
}

func (azurermInitializer AzureRMInitializer) GetTerraformInitArgs(config map[string]interface{}) map[string]interface{} {
	var filteredConfig = make(map[string]interface{})

	for key, val := range config {
		if util.ListContainsElement(terragruntAzureRMOnlyConfigs, key) {
			continue
		}

		filteredConfig[key] = val
	}

	return filteredConfig
}
