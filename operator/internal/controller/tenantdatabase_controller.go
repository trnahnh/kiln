package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1 "github.com/trnahnh/kiln/operator/api/v1"
)

type TenantDatabaseReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.internal,resources=tenantdatabases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.internal,resources=tenantdatabases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.internal,resources=tenantdatabases/finalizers,verbs=update

func (r *TenantDatabaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

func (r *TenantDatabaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1.TenantDatabase{}).
		Named("tenantdatabase").
		Complete(r)
}
