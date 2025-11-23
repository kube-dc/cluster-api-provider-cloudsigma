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

	// Check if server already exists
	if cloudSigmaMachine.Status.InstanceID != "" {
		log.V(4).Info("Server already exists", "instanceID", cloudSigmaMachine.Status.InstanceID)

		// Get server status
		server, err := cloudClient.GetServer(ctx, cloudSigmaMachine.Status.InstanceID)
		if err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to get server status")
		}

		if server == nil {
			log.Info("Server not found, will recreate", "instanceID", cloudSigmaMachine.Status.InstanceID)
			cloudSigmaMachine.Status.InstanceID = ""
			cloudSigmaMachine.Status.InstanceState = ""
		} else {
			// Update status
			return r.updateStatus(ctx, cloudSigmaMachine, server)
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

		server, err := cloudClient.CreateServer(ctx, serverSpec)
		if err != nil {
			log.Error(err, "Failed to create server")
			conditions.MarkFalse(cloudSigmaMachine, infrav1.ServerReadyCondition, infrav1.ServerCreateFailedReason, clusterv1.ConditionSeverityError, err.Error())
			return ctrl.Result{}, errors.Wrap(err, "failed to create server")
		}

		log.Info("Server created successfully", "instanceID", server.UUID)

		// Update status
		cloudSigmaMachine.Status.InstanceID = server.UUID
		cloudSigmaMachine.Status.InstanceState = server.Status

		// Set providerID
		providerID := fmt.Sprintf("cloudsigma://%s", server.UUID)
		cloudSigmaMachine.Spec.ProviderID = &providerID

		if err := r.Update(ctx, cloudSigmaMachine); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to update machine with providerID")
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

		if err := cloudClient.DeleteServer(ctx, cloudSigmaMachine.Status.InstanceID); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to delete server")
		}

		log.Info("Server deleted successfully", "instanceID", cloudSigmaMachine.Status.InstanceID)
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(cloudSigmaMachine, CloudSigmaMachineFinalizer)
	if err := r.Update(ctx, cloudSigmaMachine); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to remove finalizer")
	}

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
