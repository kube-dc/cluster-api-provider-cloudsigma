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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	infrav1 "github.com/kube-dc/cluster-api-provider-cloudsigma/api/v1beta1"
	"github.com/kube-dc/cluster-api-provider-cloudsigma/pkg/cloud"
)

const (
	CloudSigmaMachineFinalizer = "cloudsigmamachine.infrastructure.cluster.x-k8s.io"
)

// CloudSigmaMachineReconciler reconciles a CloudSigmaMachine object
type CloudSigmaMachineReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	CloudSigmaUsername string
	CloudSigmaPassword string
	CloudSigmaRegion   string
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

	// Initialize the cloud client
	cloudClient, err := cloud.NewClient(r.CloudSigmaUsername, r.CloudSigmaPassword, r.CloudSigmaRegion)
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
		log.V(4).Info("Checking existing server", "instanceID", cloudSigmaMachine.Status.InstanceID)

		// Verify server still exists in CloudSigma
		server, err = cloudClient.GetServer(ctx, cloudSigmaMachine.Status.InstanceID)
		if err != nil {
			log.Error(err, "Failed to get server")
			return ctrl.Result{}, errors.Wrap(err, "failed to get server")
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
		log.Info("Creating new CloudSigma server")

		// Get bootstrap data
		bootstrapData, err := r.getBootstrapData(ctx, machine)
		if err != nil {
			log.Info("Bootstrap data not ready yet")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		// Create server
		serverSpec := cloud.ServerSpec{
			Name:          cloudSigmaMachine.Name,
			CPU:           cloudSigmaMachine.Spec.CPU,
			Memory:        cloudSigmaMachine.Spec.Memory,
			Disks:         cloudSigmaMachine.Spec.Disks,
			NICs:          cloudSigmaMachine.Spec.NICs,
			Tags:          cloudSigmaMachine.Spec.Tags,
			Meta:          cloudSigmaMachine.Spec.Meta,
			BootstrapData: bootstrapData,
		}

		server, err = cloudClient.CreateServer(ctx, serverSpec)
		if err != nil {
			log.Error(err, "Failed to create server")
			conditions.MarkFalse(cloudSigmaMachine, infrav1.ServerReadyCondition, infrav1.ServerCreateFailedReason, clusterv1.ConditionSeverityError, err.Error())
			return ctrl.Result{}, errors.Wrap(err, "failed to create server")
		}

		log.Info("Server created successfully", "instanceID", server.UUID)

		// Update status first (this is critical to prevent duplicates)
		cloudSigmaMachine.Status.InstanceID = server.UUID
		cloudSigmaMachine.Status.InstanceState = server.Status
		if err := r.Status().Update(ctx, cloudSigmaMachine); err != nil {
			// If status update fails, we might create duplicate servers on retry
			// But at least we've recorded the server UUID in logs
			log.Error(err, "Failed to update status with instance ID", "instanceID", server.UUID)
			return ctrl.Result{}, errors.Wrap(err, "failed to update machine status")
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

	// Server exists, update its state
	if server != nil {
		cloudSigmaMachine.Status.InstanceState = server.Status

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
		}
	}

	return ctrl.Result{}, nil
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

				// Requeue to wait for server to reach stopped state
				log.Info("Server stop initiated, requeuing to wait for stopped state", "instanceID", cloudSigmaMachine.Status.InstanceID)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}

			// Server is stopped or stopping, safe to delete
			if server.Status == "stopping" {
				// Still stopping, wait a bit more
				log.Info("Server is stopping, requeuing", "instanceID", cloudSigmaMachine.Status.InstanceID)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}

			// Server is stopped, delete it
			if err := cloudClient.DeleteServer(ctx, cloudSigmaMachine.Status.InstanceID); err != nil {
				log.Error(err, "Failed to delete server", "instanceID", cloudSigmaMachine.Status.InstanceID)
				return ctrl.Result{}, errors.Wrap(err, "failed to delete server")
			}
			log.Info("Server deleted successfully", "instanceID", cloudSigmaMachine.Status.InstanceID)
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
		Complete(r)
}
