/*
Copyright 2025 Kube-DC Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/cloudsigma/cloudsigma-sdk-go/cloudsigma"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	infrav1 "github.com/kube-dc/cluster-api-provider-cloudsigma/api/v1beta1"
	"github.com/kube-dc/cluster-api-provider-cloudsigma/pkg/auth"
	"github.com/kube-dc/cluster-api-provider-cloudsigma/pkg/cloud"
)

const (
	CloudSigmaMachineFinalizer = "cloudsigmamachine.infrastructure.cluster.x-k8s.io"
)

// CloudSigmaMachineReconciler reconciles a CloudSigmaMachine object
type CloudSigmaMachineReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Legacy credential-based authentication (deprecated when impersonation is enabled)
	CloudSigmaUsername string
	CloudSigmaPassword string
	CloudSigmaRegion   string

	// Impersonation-based authentication (preferred)
	// When set, the controller will use OAuth impersonation to create VMs in user accounts
	ImpersonationClient *auth.ImpersonationClient
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=cloudsigmamachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=cloudsigmamachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=cloudsigmamachines/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines;machines/status,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *CloudSigmaMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// Fetch the CloudSigmaMachine instance
	cloudSigmaMachine := &infrav1.CloudSigmaMachine{}
	if err := r.Get(ctx, req.NamespacedName, cloudSigmaMachine); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Fetch the Machine
	machine, err := util.GetOwnerMachine(ctx, r.Client, cloudSigmaMachine.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if machine == nil {
		log.Info("Waiting for Machine Controller to set OwnerRef on CloudSigmaMachine")
		return ctrl.Result{}, nil
	}

	log = log.WithValues("machine", machine.Name)

	// Fetch the Cluster
	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, machine.ObjectMeta)
	if err != nil {
		log.Info("Machine is missing cluster label or cluster does not exist")
		return ctrl.Result{}, nil
	}

	log = log.WithValues("cluster", cluster.Name)

	// Return early if the object or Cluster is paused
	if annotations.IsPaused(cluster, cloudSigmaMachine) {
		log.Info("CloudSigmaMachine or linked Cluster is marked as paused. Won't reconcile")
		return ctrl.Result{}, nil
	}

	// Fetch the CloudSigmaCluster to get user email for impersonation
	// Note: InfrastructureRef may point to KubevirtCluster (for Kamaji compatibility),
	// so we look up CloudSigmaCluster by the CAPI cluster name directly
	cloudSigmaCluster := &infrav1.CloudSigmaCluster{}
	cloudSigmaClusterKey := client.ObjectKey{
		Namespace: cloudSigmaMachine.Namespace,
		Name:      cluster.Name, // Use cluster name, not InfrastructureRef.Name
	}
	if err := r.Get(ctx, cloudSigmaClusterKey, cloudSigmaCluster); err != nil {
		// CloudSigmaCluster may not exist if this is a non-CloudSigma cluster
		// In that case, we'll fall back to legacy credentials
		log.Info("CloudSigmaCluster not found, will use legacy credentials if available", "clusterName", cluster.Name)
		cloudSigmaCluster = nil
	}

	// Initialize the cloud client (with or without impersonation)
	cloudClient, err := r.getCloudClient(ctx, cloudSigmaCluster)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to create CloudSigma client")
	}

	// Handle deleted machines
	if !cloudSigmaMachine.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, cloudClient, cloudSigmaMachine)
	}

	// Handle non-deleted machines
	return r.reconcileNormal(ctx, cloudClient, machine, cloudSigmaMachine)
}

// getCloudClient creates a CloudSigma client, using impersonation if configured
func (r *CloudSigmaMachineReconciler) getCloudClient(ctx context.Context, cloudSigmaCluster *infrav1.CloudSigmaCluster) (*cloud.Client, error) {
	log := ctrl.LoggerFrom(ctx)

	// Determine region - prefer cluster spec, fallback to controller default
	var region string
	if cloudSigmaCluster != nil {
		region = cloudSigmaCluster.Spec.Region
	}
	if region == "" {
		region = r.CloudSigmaRegion
	}

	// Get user email for impersonation (only if CloudSigmaCluster exists)
	var userEmail string
	if cloudSigmaCluster != nil {
		userEmail = r.getUserEmail(ctx, cloudSigmaCluster)
	}

	// Use impersonation if available and user email is provided
	if r.ImpersonationClient != nil && userEmail != "" {
		log.Info("Using impersonation mode", "userEmail", userEmail, "region", region)
		return cloud.NewClientWithImpersonation(ctx, r.ImpersonationClient, userEmail, region)
	}

	// Fallback to legacy credential-based authentication (only if explicitly enabled)
	if r.CloudSigmaUsername != "" && r.CloudSigmaPassword != "" {
		// Log why we're falling back to legacy mode for traceability
		fallbackReason := "unknown"
		if r.ImpersonationClient == nil {
			fallbackReason = "impersonation client not configured"
		} else if userEmail == "" {
			fallbackReason = "userEmail not set in CloudSigmaCluster"
		}
		log.Info("Using legacy credential mode (FALLBACK)", "region", region, "reason", fallbackReason, "username", r.CloudSigmaUsername)
		return cloud.NewClient(r.CloudSigmaUsername, r.CloudSigmaPassword, region)
	}

	// No valid authentication method available
	if r.ImpersonationClient != nil && userEmail == "" {
		return nil, fmt.Errorf("impersonation configured but userEmail not set in CloudSigmaCluster spec - ensure CloudSigmaCluster has spec.userEmail set")
	}
	return nil, fmt.Errorf("no CloudSigma authentication available: impersonation requires userEmail in CloudSigmaCluster, legacy credentials not enabled")
}

// getUserEmail extracts the user email from CloudSigmaCluster spec or referenced secret
func (r *CloudSigmaMachineReconciler) getUserEmail(ctx context.Context, cloudSigmaCluster *infrav1.CloudSigmaCluster) string {
	// Direct user email takes precedence
	if cloudSigmaCluster.Spec.UserEmail != "" {
		return cloudSigmaCluster.Spec.UserEmail
	}

	// Try to get from referenced secret
	if cloudSigmaCluster.Spec.UserRef != nil {
		secret := &corev1.Secret{}
		secretKey := client.ObjectKey{
			Namespace: cloudSigmaCluster.Spec.UserRef.Namespace,
			Name:      cloudSigmaCluster.Spec.UserRef.Name,
		}
		if secretKey.Namespace == "" {
			secretKey.Namespace = cloudSigmaCluster.Namespace
		}

		if err := r.Get(ctx, secretKey, secret); err == nil {
			if email, ok := secret.Data["userEmail"]; ok {
				return string(email)
			}
		}
	}

	return ""
}

func (r *CloudSigmaMachineReconciler) reconcileNormal(
	ctx context.Context,
	cloudClient *cloud.Client,
	machine *clusterv1.Machine,
	cloudSigmaMachine *infrav1.CloudSigmaMachine,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(cloudSigmaMachine, CloudSigmaMachineFinalizer) {
		controllerutil.AddFinalizer(cloudSigmaMachine, CloudSigmaMachineFinalizer)
		if err := r.Update(ctx, cloudSigmaMachine); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to add finalizer")
		}
	}

	// Check if server already exists (idempotency check)
	var server *cloudsigma.Server
	var err error
	if cloudSigmaMachine.Status.InstanceID != "" {
		log.V(4).Info("Checking existing server", 
			"instanceID", cloudSigmaMachine.Status.InstanceID,
			"impersonatedUser", cloudClient.ImpersonatedUser())

		// Verify server still exists in CloudSigma
		server, err = cloudClient.GetServer(ctx, cloudSigmaMachine.Status.InstanceID)
		if err != nil {
			// Check if this is a permission denied error (403)
			if cloud.IsPermissionDeniedError(err) {
				log.Error(err, "Cannot access server - likely owned by different user or orphaned",
					"instanceID", cloudSigmaMachine.Status.InstanceID,
					"impersonatedUser", cloudClient.ImpersonatedUser())

				// Try to find a server by name/metadata that we CAN access
				machineUID := string(cloudSigmaMachine.UID)
				existingServer, findErr := cloudClient.FindServerByNameOrMeta(ctx, cloudSigmaMachine.Name, machineUID)
				if findErr == nil && existingServer != nil {
					log.Info("Found accessible server with matching name/metadata, updating status",
						"oldInstanceID", cloudSigmaMachine.Status.InstanceID,
						"newInstanceID", existingServer.UUID,
						"impersonatedUser", cloudClient.ImpersonatedUser())
					cloudSigmaMachine.Status.InstanceID = existingServer.UUID
					cloudSigmaMachine.Status.InstanceState = existingServer.Status
					if updateErr := r.Status().Update(ctx, cloudSigmaMachine); updateErr != nil {
						log.Error(updateErr, "Failed to update status with found server")
						return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
					}
					server = existingServer
				} else {
					// No accessible server found - clear the orphaned instance ID to trigger recreation
					log.Info("No accessible server found - clearing orphaned instance ID to trigger recreation",
						"orphanedInstanceID", cloudSigmaMachine.Status.InstanceID,
						"impersonatedUser", cloudClient.ImpersonatedUser())
					cloudSigmaMachine.Status.InstanceID = ""
					cloudSigmaMachine.Status.InstanceState = ""
					if updateErr := r.Status().Update(ctx, cloudSigmaMachine); updateErr != nil {
						log.V(4).Info("Failed to clear orphaned status", "error", updateErr)
					}
					// Requeue to trigger creation
					return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
				}
			} else {
				log.Error(err, "Failed to get server", "instanceID", cloudSigmaMachine.Status.InstanceID)
				return ctrl.Result{}, errors.Wrap(err, "failed to get server")
			}
		}

		if server == nil {
			// Server was deleted externally, clear status to trigger recreation
			log.Info("Server no longer exists, will recreate", "instanceID", cloudSigmaMachine.Status.InstanceID)
			cloudSigmaMachine.Status.InstanceID = ""
			cloudSigmaMachine.Status.InstanceState = ""
			if err := r.Status().Update(ctx, cloudSigmaMachine); err != nil {
				log.V(4).Info("Failed to clear status", "error", err)
			}
		}
	}

	// Create server if it doesn't exist
	if cloudSigmaMachine.Status.InstanceID == "" {
		// Get machine UID for metadata-based identification
		machineUID := string(cloudSigmaMachine.UID)
		log.Info("Checking for existing server", "name", cloudSigmaMachine.Name, "machineUID", machineUID)

		// Check if server already exists by name or metadata (race condition protection)
		existingServer, err := cloudClient.FindServerByNameOrMeta(ctx, cloudSigmaMachine.Name, machineUID)
		if err != nil {
			log.Error(err, "Failed to check for existing server")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		if existingServer != nil {
			// Server already exists, update status and continue
			log.Info("Found existing server, updating status", "instanceID", existingServer.UUID, "name", cloudSigmaMachine.Name)
			cloudSigmaMachine.Status.InstanceID = existingServer.UUID
			cloudSigmaMachine.Status.InstanceState = existingServer.Status
			if err := r.Status().Update(ctx, cloudSigmaMachine); err != nil {
				log.Error(err, "Failed to update status with existing server", "instanceID", existingServer.UUID)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			// Also set providerID in spec (required for Machine to transition to Running)
			providerID := fmt.Sprintf("cloudsigma://%s", existingServer.UUID)
			cloudSigmaMachine.Spec.ProviderID = &providerID
			if err := r.Update(ctx, cloudSigmaMachine); err != nil {
				log.Error(err, "Failed to update spec with providerID for existing server", "instanceID", existingServer.UUID)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			server = existingServer
		} else {
			log.Info("No existing server found, creating new CloudSigma server", "name", cloudSigmaMachine.Name, "machineUID", machineUID)

			// Get bootstrap data
			bootstrapData, err := r.getBootstrapData(ctx, machine)
			if err != nil {
				log.Info("Bootstrap data not ready yet")
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}

			// Create server with machine-uid in metadata for identification
			meta := make(map[string]string)
			// Copy existing metadata
			for k, v := range cloudSigmaMachine.Spec.Meta {
				meta[k] = v
			}
			// Add machine-uid for duplicate detection
			meta["machine-uid"] = machineUID
			meta["cluster"] = cloudSigmaMachine.Labels["cluster.x-k8s.io/cluster-name"]
			meta["pool"] = cloudSigmaMachine.Labels["cluster.x-k8s.io/deployment-name"]

			serverSpec := cloud.ServerSpec{
				Name:          cloudSigmaMachine.Name,
				CPU:           cloudSigmaMachine.Spec.CPU,
				Memory:        cloudSigmaMachine.Spec.Memory,
				Disks:         cloudSigmaMachine.Spec.Disks,
				NICs:          cloudSigmaMachine.Spec.NICs,
				Tags:          cloudSigmaMachine.Spec.Tags,
				Meta:          meta,
				BootstrapData: bootstrapData,
			}

			server, err = cloudClient.CreateServer(ctx, serverSpec)
			if err != nil {
				log.Error(err, "Failed to create server")
				conditions.MarkFalse(cloudSigmaMachine, infrav1.ServerReadyCondition, infrav1.ServerCreateFailedReason, clusterv1.ConditionSeverityError, err.Error())
				return ctrl.Result{}, errors.Wrap(err, "failed to create server")
			}

			log.Info("Server created successfully", 
				"instanceID", server.UUID,
				"name", cloudSigmaMachine.Name,
				"impersonatedUser", cloudClient.ImpersonatedUser())

			// Update status first (this is critical to prevent duplicates)
			cloudSigmaMachine.Status.InstanceID = server.UUID
			cloudSigmaMachine.Status.InstanceState = server.Status
			if err := r.Status().Update(ctx, cloudSigmaMachine); err != nil {
				// If status update fails due to conflict, DON'T return error immediately
				// Delay requeue to give CloudSigma API time to propagate the server
				// so FindServerByNameOrMeta can find it on next reconcile
				log.Error(err, "Failed to update status with instance ID, will retry after delay", 
					"instanceID", server.UUID,
					"machineName", cloudSigmaMachine.Name,
					"impersonatedUser", cloudClient.ImpersonatedUser())
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}

			// Set providerID in spec (separate update)
			providerID := fmt.Sprintf("cloudsigma://%s", server.UUID)
			cloudSigmaMachine.Spec.ProviderID = &providerID
			if err := r.Update(ctx, cloudSigmaMachine); err != nil {
				// This is less critical - if it fails, we'll retry but won't create duplicates
				log.Error(err, "Failed to update spec with providerID", "instanceID", server.UUID)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}

			// Start server if not running
			if server.Status != "running" {
				log.Info("Starting server", "instanceID", server.UUID)
				if err := cloudClient.StartServer(ctx, server.UUID); err != nil {
					return ctrl.Result{}, errors.Wrap(err, "failed to start server")
				}
			}

			// Requeue to check status
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}

	// Server exists, update its state
	if server != nil {
		cloudSigmaMachine.Status.InstanceState = server.Status

		// Ensure providerID is set in spec (required for Machine to transition to Running)
		if cloudSigmaMachine.Spec.ProviderID == nil || *cloudSigmaMachine.Spec.ProviderID == "" {
			providerID := fmt.Sprintf("cloudsigma://%s", server.UUID)
			cloudSigmaMachine.Spec.ProviderID = &providerID
			if err := r.Update(ctx, cloudSigmaMachine); err != nil {
				log.Error(err, "Failed to set providerID in spec", "instanceID", server.UUID)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			log.Info("Set providerID in spec", "instanceID", server.UUID, "providerID", providerID)
		}

		// Extract and populate network addresses from CloudSigma API
		addresses, err := cloudClient.GetServerAddressesWithClient(ctx, server)
		if err != nil {
			log.Error(err, "Failed to get server addresses", "instanceID", server.UUID)
		} else if len(addresses) > 0 {
			cloudSigmaMachine.Status.Addresses = addresses
			log.Info("Populated server addresses", "instanceID", server.UUID, "addresses", addresses)
		} else if server.Status == "running" {
			log.V(2).Info("Server running but no addresses found yet", "instanceID", server.UUID)
		}

		if err := r.Status().Update(ctx, cloudSigmaMachine); err != nil {
			log.V(4).Info("Failed to update instance state", "error", err)
			// Don't fail on status update conflicts here
		}

		// Ensure server is running
		if server.Status == "stopped" {
			log.Info("Starting stopped server", "instanceID", server.UUID)
			if err := cloudClient.StartServer(ctx, server.UUID); err != nil {
				return ctrl.Result{}, errors.Wrap(err, "failed to start server")
			}
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		// Set ready condition when server is running and has addresses
		if server.Status == "running" {
			conditions.MarkTrue(cloudSigmaMachine, infrav1.ServerReadyCondition)
			cloudSigmaMachine.Status.Ready = true
			if err := r.Status().Update(ctx, cloudSigmaMachine); err != nil {
				log.V(4).Info("Failed to update ready status", "error", err)
			}

			// If server is running but no addresses yet, requeue to check again
			if len(addresses) == 0 {
				log.Info("Server running but waiting for IP address assignment", "instanceID", server.UUID)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
		} else {
			// Server is not running - mark as not ready
			conditions.MarkFalse(cloudSigmaMachine, infrav1.ServerReadyCondition,
				infrav1.ServerNotRunningReason, clusterv1.ConditionSeverityWarning,
				"Server status: %s", server.Status)
			cloudSigmaMachine.Status.Ready = false
			if err := r.Status().Update(ctx, cloudSigmaMachine); err != nil {
				log.V(4).Info("Failed to update ready status", "error", err)
			}
		}
	}

	// Always requeue to periodically check server status (every 60 seconds)
	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

func (r *CloudSigmaMachineReconciler) reconcileDelete(
	ctx context.Context,
	cloudClient *cloud.Client,
	cloudSigmaMachine *infrav1.CloudSigmaMachine,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	if cloudSigmaMachine.Status.InstanceID != "" {
		log.Info("Deleting server", "instanceID", cloudSigmaMachine.Status.InstanceID)

		// Check if server still exists in CloudSigma
		server, err := cloudClient.GetServer(ctx, cloudSigmaMachine.Status.InstanceID)
		if err != nil {
			log.Error(err, "Failed to get server for deletion", "instanceID", cloudSigmaMachine.Status.InstanceID)
			return ctrl.Result{}, errors.Wrap(err, "failed to get server for deletion")
		}

		if server == nil {
			// Server already deleted (externally or previously)
			log.Info("Server not found in CloudSigma, assuming already deleted", "instanceID", cloudSigmaMachine.Status.InstanceID)
		} else {
			// Check if server is running and stop it first
			// CloudSigma API requires servers to be stopped before deletion
			if server.Status == "running" || server.Status == "starting" {
				log.Info("Server is running, stopping before deletion",
					"instanceID", cloudSigmaMachine.Status.InstanceID,
					"status", server.Status)

				if err := cloudClient.StopServer(ctx, cloudSigmaMachine.Status.InstanceID); err != nil {
					log.Error(err, "Failed to stop server", "instanceID", cloudSigmaMachine.Status.InstanceID)
					return ctrl.Result{}, errors.Wrap(err, "failed to stop server")
				}

				log.Info("Server stop initiated, waiting for stopped state", "instanceID", cloudSigmaMachine.Status.InstanceID)
			}

			// Wait for server to stop (poll inline instead of requeue)
			// Max 2 minutes (12 * 10s) - after that, force delete anyway
			stoppedOrTimeout := false
			for i := 0; i < 12; i++ {
				server, err = cloudClient.GetServer(ctx, cloudSigmaMachine.Status.InstanceID)
				if err != nil {
					log.Error(err, "Failed to get server status during deletion", "instanceID", cloudSigmaMachine.Status.InstanceID)
					return ctrl.Result{}, errors.Wrap(err, "failed to get server status")
				}
				if server == nil {
					log.Info("Server no longer exists", "instanceID", cloudSigmaMachine.Status.InstanceID)
					stoppedOrTimeout = true
					break
				}
				if server.Status == "stopped" {
					stoppedOrTimeout = true
					break
				}
				log.Info("Waiting for server to stop", "instanceID", cloudSigmaMachine.Status.InstanceID, "status", server.Status)
				time.Sleep(10 * time.Second)
			}

			// If still not stopped after timeout, log warning and try force delete anyway
			if !stoppedOrTimeout && server != nil {
				log.Info("Server stuck in stopping state, attempting force delete after timeout",
					"instanceID", cloudSigmaMachine.Status.InstanceID,
					"status", server.Status)
			}

			// Delete the server if it still exists
			if server != nil {
				if err := cloudClient.DeleteServer(ctx, cloudSigmaMachine.Status.InstanceID); err != nil {
					// Check if server is already deleting or deleted or stopping - treat as success
					errMsg := err.Error()
					if strings.Contains(errMsg, "in state 'deleting'") ||
						strings.Contains(errMsg, "in state 'stopping'") ||
						strings.Contains(errMsg, "not found") ||
						strings.Contains(errMsg, "404") {
						log.Info("Server already deleting/stopping or deleted, proceeding to remove finalizer", "instanceID", cloudSigmaMachine.Status.InstanceID)
					} else {
						log.Error(err, "Failed to delete server", "instanceID", cloudSigmaMachine.Status.InstanceID)
						return ctrl.Result{}, errors.Wrap(err, "failed to delete server")
					}
				} else {
					log.Info("Server deleted successfully", "instanceID", cloudSigmaMachine.Status.InstanceID)
				}
			}
		}
	} else {
		log.Info("No instance ID set, nothing to delete")
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(cloudSigmaMachine, CloudSigmaMachineFinalizer)
	if err := r.Update(ctx, cloudSigmaMachine); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to remove finalizer")
	}

	log.Info("CloudSigmaMachine deletion completed")
	return ctrl.Result{}, nil
}

func (r *CloudSigmaMachineReconciler) updateStatus(
	ctx context.Context,
	cloudSigmaMachine *infrav1.CloudSigmaMachine,
	server interface{},
) (ctrl.Result, error) {
	// TODO: Parse server object and update addresses
	cloudSigmaMachine.Status.Ready = true
	conditions.MarkTrue(cloudSigmaMachine, infrav1.ServerReadyCondition)

	if err := r.Status().Update(ctx, cloudSigmaMachine); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to update status")
	}

	return ctrl.Result{}, nil
}

func (r *CloudSigmaMachineReconciler) getBootstrapData(ctx context.Context, machine *clusterv1.Machine) (string, error) {
	if machine.Spec.Bootstrap.DataSecretName == nil {
		return "", errors.New("bootstrap data secret is not set")
	}

	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: machine.Namespace, Name: *machine.Spec.Bootstrap.DataSecretName}
	if err := r.Get(ctx, key, secret); err != nil {
		return "", errors.Wrap(err, "failed to get bootstrap data secret")
	}

	data, ok := secret.Data["value"]
	if !ok {
		return "", errors.New("bootstrap data secret does not contain 'value' key")
	}

	// Base64 encode for cloud-init
	return base64.StdEncoding.EncodeToString(data), nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CloudSigmaMachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.CloudSigmaMachine{}).
		WithEventFilter(predicates.ResourceNotPaused(ctrl.LoggerFrom(context.Background()))).
		// Limit to 1 concurrent reconcile to prevent duplicate VM creation
		// due to race conditions with CloudSigma API eventual consistency
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}
