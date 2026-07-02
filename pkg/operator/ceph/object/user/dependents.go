/*
Copyright 2026 The Rook Authors. All rights reserved.

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

package objectuser

import (
	"fmt"

	bktv1alpha1 "github.com/kube-object-storage/lib-bucket-provisioner/pkg/apis/objectbucket.io/v1alpha1"
	"github.com/pkg/errors"
	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	"github.com/rook/rook/pkg/operator/ceph/object/bucket"
	"github.com/rook/rook/pkg/util/dependents"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// referencingOBCs returns the ObjectBucketClaims whose
// additionalConfig.bucketOwner names the given CephObjectStoreUser's rgw user
// on the same object store. OBCs are listed across all namespaces: strict
// bucketOwner mode only provisions same-namespace references, but OBCs
// provisioned before the mode was enabled may reference the user from other
// namespaces.
func (r *ReconcileObjectStoreUser) referencingOBCs(u *cephv1.CephObjectStoreUser) (*dependents.DependentList, error) {
	deps := dependents.NewDependentList()

	obcs := &bktv1alpha1.ObjectBucketClaimList{}
	if err := r.client.List(r.opManagerContext, obcs); err != nil {
		// without the OBC CRD no OBC can reference the user
		if meta.IsNoMatchError(err) {
			return deps, nil
		}
		return nil, errors.Wrap(err, "failed to list ObjectBucketClaims")
	}

	for i := range obcs.Items {
		obc := &obcs.Items[i]
		if obc.Spec.AdditionalConfig["bucketOwner"] != u.Name {
			continue
		}

		sc, err := r.context.Clientset.StorageV1().StorageClasses().Get(r.opManagerContext, obc.Spec.StorageClassName, metav1.GetOptions{})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get storage class %q of ObjectBucketClaim %q/%q", obc.Spec.StorageClassName, obc.Namespace, obc.Name)
		}
		if sc.Parameters[bucket.ObjectStoreName] != u.Spec.Store {
			continue
		}
		if ns := sc.Parameters[bucket.ObjectStoreNamespace]; ns != "" && ns != clusterStoreNamespace(u) {
			continue
		}

		deps.Add("ObjectBucketClaims", fmt.Sprintf("%s/%s", obc.Namespace, obc.Name))
	}

	return deps, nil
}
