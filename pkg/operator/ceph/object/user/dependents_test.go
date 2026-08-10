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
	"context"
	"testing"

	bktv1alpha1 "github.com/kube-object-storage/lib-bucket-provisioner/pkg/apis/objectbucket.io/v1alpha1"
	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	"github.com/rook/rook/pkg/clusterd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestReferencingOBCs(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cephv1.AddToScheme(scheme))
	require.NoError(t, bktv1alpha1.AddToScheme(scheme))

	user := &cephv1.CephObjectStoreUser{
		ObjectMeta: metav1.ObjectMeta{Name: "owner-user", Namespace: "app-ns"},
		Spec:       cephv1.ObjectStoreUserSpec{Store: "my-store", ClusterNamespace: "rook-ceph"},
	}

	newSC := func(name, store, storeNamespace string) *storagev1.StorageClass {
		sc := &storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Parameters: map[string]string{},
		}
		if store != "" {
			sc.Parameters["objectStoreName"] = store
		}
		if storeNamespace != "" {
			sc.Parameters["objectStoreNamespace"] = storeNamespace
		}
		return sc
	}

	newOBC := func(namespace, name, scName, bucketOwner string) *bktv1alpha1.ObjectBucketClaim {
		obc := &bktv1alpha1.ObjectBucketClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: bktv1alpha1.ObjectBucketClaimSpec{
				StorageClassName: scName,
			},
		}
		if bucketOwner != "" {
			obc.Spec.AdditionalConfig = map[string]string{"bucketOwner": bucketOwner}
		}
		return obc
	}

	newReconciler := func(scs []runtime.Object, obcs ...*bktv1alpha1.ObjectBucketClaim) *ReconcileObjectStoreUser {
		builder := fakeclient.NewClientBuilder().WithScheme(scheme)
		for _, obc := range obcs {
			builder = builder.WithObjects(obc)
		}
		return &ReconcileObjectStoreUser{
			client:           builder.Build(),
			context:          &clusterd.Context{Clientset: k8sfake.NewSimpleClientset(scs...)},
			opManagerContext: context.TODO(),
		}
	}

	matchingSC := newSC("bucket-sc", "my-store", "rook-ceph")

	t.Run("no OBCs", func(t *testing.T) {
		r := newReconciler([]runtime.Object{matchingSC})
		deps, err := r.referencingOBCs(user)
		require.NoError(t, err)
		assert.True(t, deps.Empty())
	})

	t.Run("OBC referencing the user on the same store", func(t *testing.T) {
		r := newReconciler([]runtime.Object{matchingSC}, newOBC("app-ns", "obc1", "bucket-sc", "owner-user"))
		deps, err := r.referencingOBCs(user)
		require.NoError(t, err)
		assert.Equal(t, []string{"app-ns/obc1"}, deps.OfKind("ObjectBucketClaims"))
	})

	t.Run("cross-namespace OBC still counts", func(t *testing.T) {
		r := newReconciler([]runtime.Object{matchingSC}, newOBC("other-ns", "obc2", "bucket-sc", "owner-user"))
		deps, err := r.referencingOBCs(user)
		require.NoError(t, err)
		assert.Equal(t, []string{"other-ns/obc2"}, deps.OfKind("ObjectBucketClaims"))
	})

	t.Run("OBC without bucketOwner does not count", func(t *testing.T) {
		r := newReconciler([]runtime.Object{matchingSC}, newOBC("app-ns", "obc3", "bucket-sc", ""))
		deps, err := r.referencingOBCs(user)
		require.NoError(t, err)
		assert.True(t, deps.Empty())
	})

	t.Run("OBC referencing another user does not count", func(t *testing.T) {
		r := newReconciler([]runtime.Object{matchingSC}, newOBC("app-ns", "obc4", "bucket-sc", "someone-else"))
		deps, err := r.referencingOBCs(user)
		require.NoError(t, err)
		assert.True(t, deps.Empty())
	})

	t.Run("OBC on another store does not count", func(t *testing.T) {
		otherSC := newSC("other-sc", "other-store", "rook-ceph")
		r := newReconciler([]runtime.Object{otherSC}, newOBC("app-ns", "obc5", "other-sc", "owner-user"))
		deps, err := r.referencingOBCs(user)
		require.NoError(t, err)
		assert.True(t, deps.Empty())
	})

	t.Run("OBC on another cluster namespace does not count", func(t *testing.T) {
		otherSC := newSC("other-cluster-sc", "my-store", "other-cluster")
		r := newReconciler([]runtime.Object{otherSC}, newOBC("app-ns", "obc6", "other-cluster-sc", "owner-user"))
		deps, err := r.referencingOBCs(user)
		require.NoError(t, err)
		assert.True(t, deps.Empty())
	})

	t.Run("storage class without namespace parameter counts on store match", func(t *testing.T) {
		nsLessSC := newSC("nsless-sc", "my-store", "")
		r := newReconciler([]runtime.Object{nsLessSC}, newOBC("app-ns", "obc7", "nsless-sc", "owner-user"))
		deps, err := r.referencingOBCs(user)
		require.NoError(t, err)
		assert.Equal(t, []string{"app-ns/obc7"}, deps.OfKind("ObjectBucketClaims"))
	})

	t.Run("missing storage class is an error", func(t *testing.T) {
		r := newReconciler(nil, newOBC("app-ns", "obc8", "no-such-sc", "owner-user"))
		_, err := r.referencingOBCs(user)
		assert.Error(t, err)
	})
}
