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

	"github.com/pkg/errors"
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
	CloudSigmaClusterFinalizer = "cloudsigmacluster.infrastructure.cluster.x-k8s.io"
)

// CloudSigmaClusterReconciler reconciles a CloudSigmaCluster object
type CloudSigmaClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	CloudSigmaUsername string
	CloudSigmaPassword string
	CloudSigmaRegion   string
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=cloudsigmaclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=cloudsigmaclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=cloudsigmaclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status,verbs=get;list;watch

func (r *CloudSigmaClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// Fetch the CloudSigmaCluster instance
	cloudSigmaCluster := &infrav1.CloudSigmaCluster{}
	if err := r.Get(ctx, req.NamespacedName, cloudSigmaCluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Fetch the Cluster
	cluster, err := util.GetOwnerCluster(ctx, r.Client, cloudSigmaCluster.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cluster == nil {
		log.Info("Waiting for Cluster Controller to set OwnerRef on CloudSigmaCluster")
		return ctrl.Result{}, nil
	}

	log = log.WithValues("cluster", cluster.Name)

	// Return early if the object or Cluster is paused
	if annotations.IsPaused(cluster, cloudSigmaCluster) {
		log.Info("CloudSigmaCluster or linked Cluster is marked as paused. Won't reconcile")
		return ctrl.Result{}, nil
	}

	// Initialize the cloud client
	cloudClient, err := cloud.NewClient(r.CloudSigmaUsername, r.CloudSigmaPassword, r.CloudSigmaRegion)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to create CloudSigma client")
	}

	// Handle deleted clusters
	if !cloudSigmaCluster.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, cloudClient, cloudSigmaCluster)
	}

	// Handle non-deleted clusters
	return r.reconcileNormal(ctx, cloudClient, cluster, cloudSigmaCluster)
}

func (r *CloudSigmaClusterReconciler) reconcileNormal(
	ctx context.Context,
	cloudClient *cloud.Client,
	cluster *clusterv1.Cluster,
	cloudSigmaCluster *infrav1.CloudSigmaCluster,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(cloudSigmaCluster, CloudSigmaClusterFinalizer) {
		controllerutil.AddFinalizer(cloudSigmaCluster, CloudSigmaClusterFinalizer)
		if err := r.Update(ctx, cloudSigmaCluster); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to add finalizer")
		}
	}

	// Reconcile VLAN if specified
	if cloudSigmaCluster.Spec.VLAN != nil {
		if err := r.reconcileVLAN(ctx, cloudClient, cloudSigmaCluster); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to reconcile VLAN")
		}
	}

	// Mark cluster as ready
	cloudSigmaCluster.Status.Ready = true
	conditions.MarkTrue(cloudSigmaCluster, infrav1.NetworkReadyCondition)

	if err := r.Status().Update(ctx, cloudSigmaCluster); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to update status")
	}

	log.Info("CloudSigmaCluster is ready")
	return ctrl.Result{}, nil
}

func (r *CloudSigmaClusterReconciler) reconcileDelete(
	ctx context.Context,
	cloudClient *cloud.Client,
	cloudSigmaCluster *infrav1.CloudSigmaCluster,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// TODO: Clean up VLAN if it was created by us
	log.Info("Cleaning up CloudSigma resources")

	// Remove finalizer
	controllerutil.RemoveFinalizer(cloudSigmaCluster, CloudSigmaClusterFinalizer)
	if err := r.Update(ctx, cloudSigmaCluster); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to remove finalizer")
	}

	return ctrl.Result{}, nil
}

func (r *CloudSigmaClusterReconciler) reconcileVLAN(
	ctx context.Context,
	cloudClient *cloud.Client,
	cloudSigmaCluster *infrav1.CloudSigmaCluster,
) error {
	log := ctrl.LoggerFrom(ctx)

	// If UUID is provided, verify it exists
	if cloudSigmaCluster.Spec.VLAN.UUID != "" {
		vlan, err := cloudClient.GetVLAN(ctx, cloudSigmaCluster.Spec.VLAN.UUID)
		if err != nil {
			return errors.Wrap(err, "failed to get VLAN")
		}
		if vlan == nil {
			return errors.New("specified VLAN does not exist")
		}

		log.Info("Using existing VLAN", "uuid", vlan.UUID)

		// Update status
		if cloudSigmaCluster.Status.Network == nil {
			cloudSigmaCluster.Status.Network = &infrav1.NetworkStatus{}
		}
		cloudSigmaCluster.Status.Network.VLANUUID = vlan.UUID

		return nil
	}

	// TODO: Create new VLAN if name and CIDR are provided
	log.V(4).Info("VLAN configuration not provided, skipping")
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CloudSigmaClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.CloudSigmaCluster{}).
		WithEventFilter(predicates.ResourceNotPaused(ctrl.LoggerFrom(context.Background()))).
		Complete(r)
}
