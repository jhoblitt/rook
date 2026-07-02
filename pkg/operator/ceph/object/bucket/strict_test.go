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

package bucket

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/ceph/go-ceph/rgw/admin"
	bktv1alpha1 "github.com/kube-object-storage/lib-bucket-provisioner/pkg/apis/objectbucket.io/v1alpha1"
	apibkt "github.com/kube-object-storage/lib-bucket-provisioner/pkg/provisioner/api"
	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	rookclient "github.com/rook/rook/pkg/client/clientset/versioned/fake"
	"github.com/rook/rook/pkg/clusterd"
	"github.com/rook/rook/pkg/daemon/ceph/client"
	opcontroller "github.com/rook/rook/pkg/operator/ceph/controller"
	"github.com/rook/rook/pkg/operator/ceph/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

const (
	strictTestNamespace        = "app-ns"
	strictTestClusterNamespace = "rook-ceph"
	strictTestStore            = "test-store"
	strictTestOBCName          = "my-obc"
)

var strictTestOBCUID = types.UID("6e7c4d3f-3494-4dc1-90dc-58527fdf05d7")

func enableStrictBucketOwner(t *testing.T) {
	t.Setenv("ROOK_OBC_STRICT_BUCKET_OWNER", "true")
	opcontroller.SetObcStrictBucketOwner()
	t.Cleanup(func() {
		os.Unsetenv("ROOK_OBC_STRICT_BUCKET_OWNER")
		opcontroller.SetObcStrictBucketOwner()
	})
}

func strictTestOBC(bucketOwner *string) *bktv1alpha1.ObjectBucketClaim {
	obc := &bktv1alpha1.ObjectBucketClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      strictTestOBCName,
			Namespace: strictTestNamespace,
			UID:       strictTestOBCUID,
		},
	}
	if bucketOwner != nil {
		obc.Spec.AdditionalConfig = map[string]string{"bucketOwner": *bucketOwner}
	}
	return obc
}

func strictTestSecret(ownerUID types.UID) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:       strictTestOBCName,
			Namespace:  strictTestNamespace,
			Finalizers: []string{bucketProvisionerFinalizer},
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "objectbucket.io/v1alpha1", Kind: "ObjectBucketClaim", Name: strictTestOBCName, UID: ownerUID},
			},
		},
		StringData: map[string]string{"AWS_ACCESS_KEY_ID": "u", "AWS_SECRET_ACCESS_KEY": "p"},
	}
}

func strictTestUser(mutate func(*cephv1.CephObjectStoreUser)) *cephv1.CephObjectStoreUser {
	u := &cephv1.CephObjectStoreUser{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "owner-user",
			Namespace: strictTestNamespace,
		},
		Spec: cephv1.ObjectStoreUserSpec{
			Store:            strictTestStore,
			ClusterNamespace: strictTestClusterNamespace,
		},
	}
	if mutate != nil {
		mutate(u)
	}
	return u
}

func strictTestProvisioner(t *testing.T, rookObjs []runtime.Object, k8sObjs []runtime.Object) *Provisioner {
	clusterInfo := client.AdminTestClusterInfo(strictTestClusterNamespace)
	p := NewProvisioner(&clusterd.Context{
		RookClientset: rookclient.NewSimpleClientset(rookObjs...),
		Clientset:     k8sfake.NewSimpleClientset(k8sObjs...),
	}, clusterInfo)
	p.objectContext = object.NewContext(p.context, clusterInfo, strictTestStore)
	p.objectStoreName = strictTestStore
	return p
}

func strictTestBucket(p *Provisioner, bucketOwner *string) *bucket {
	return &bucket{
		provisioner:      p,
		options:          &apibkt.BucketOptions{ObjectBucketClaim: strictTestOBC(bucketOwner)},
		additionalConfig: &additionalConfigSpec{bucketOwner: bucketOwner},
	}
}

func assertSecretDeleted(t *testing.T, p *Provisioner, deleted bool) {
	t.Helper()
	_, err := p.context.Clientset.CoreV1().Secrets(strictTestNamespace).Get(p.clusterInfo.Context, strictTestOBCName, metav1.GetOptions{})
	if deleted {
		assert.True(t, kerrors.IsNotFound(err))
	} else {
		assert.NoError(t, err)
	}
}

func TestEnforceStrictBucketOwner(t *testing.T) {
	owner := "owner-user"

	t.Run("mode off allows OBC without bucketOwner", func(t *testing.T) {
		p := strictTestProvisioner(t, nil, []runtime.Object{strictTestSecret(strictTestOBCUID)})
		err := p.enforceStrictBucketOwner(strictTestBucket(p, nil))
		assert.NoError(t, err)
		assertSecretDeleted(t, p, false)
	})

	t.Run("no bucketOwner fails and deletes the secret", func(t *testing.T) {
		enableStrictBucketOwner(t)
		p := strictTestProvisioner(t, nil, []runtime.Object{strictTestSecret(strictTestOBCUID)})
		err := p.enforceStrictBucketOwner(strictTestBucket(p, nil))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must set additionalConfig.bucketOwner")
		assertSecretDeleted(t, p, true)
	})

	t.Run("missing CephObjectStoreUser fails and deletes the secret", func(t *testing.T) {
		enableStrictBucketOwner(t)
		p := strictTestProvisioner(t, nil, []runtime.Object{strictTestSecret(strictTestOBCUID)})
		err := p.enforceStrictBucketOwner(strictTestBucket(p, &owner))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not reference a CephObjectStoreUser")
		assertSecretDeleted(t, p, true)
	})

	t.Run("user of another store fails and deletes the secret", func(t *testing.T) {
		enableStrictBucketOwner(t)
		u := strictTestUser(func(u *cephv1.CephObjectStoreUser) { u.Spec.Store = "other-store" })
		p := strictTestProvisioner(t, []runtime.Object{u}, []runtime.Object{strictTestSecret(strictTestOBCUID)})
		err := p.enforceStrictBucketOwner(strictTestBucket(p, &owner))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "belongs to object store")
		assertSecretDeleted(t, p, true)
	})

	t.Run("user of another cluster namespace fails and deletes the secret", func(t *testing.T) {
		enableStrictBucketOwner(t)
		u := strictTestUser(func(u *cephv1.CephObjectStoreUser) { u.Spec.ClusterNamespace = "other-cluster" })
		p := strictTestProvisioner(t, []runtime.Object{u}, []runtime.Object{strictTestSecret(strictTestOBCUID)})
		err := p.enforceStrictBucketOwner(strictTestBucket(p, &owner))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "targets Ceph cluster namespace")
		assertSecretDeleted(t, p, true)
	})

	t.Run("user without clusterNamespace defaults to its own namespace", func(t *testing.T) {
		enableStrictBucketOwner(t)
		u := strictTestUser(func(u *cephv1.CephObjectStoreUser) { u.Spec.ClusterNamespace = "" })
		p := strictTestProvisioner(t, []runtime.Object{u}, []runtime.Object{strictTestSecret(strictTestOBCUID)})
		err := p.enforceStrictBucketOwner(strictTestBucket(p, &owner))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "targets Ceph cluster namespace")
		assertSecretDeleted(t, p, true)
	})

	t.Run("user being deleted fails without deleting the secret", func(t *testing.T) {
		enableStrictBucketOwner(t)
		now := metav1.Now()
		u := strictTestUser(func(u *cephv1.CephObjectStoreUser) {
			u.DeletionTimestamp = &now
			u.Finalizers = []string{"cephobjectstoreuser.ceph.rook.io"}
		})
		p := strictTestProvisioner(t, []runtime.Object{u}, []runtime.Object{strictTestSecret(strictTestOBCUID)})
		err := p.enforceStrictBucketOwner(strictTestBucket(p, &owner))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is being deleted")
		assertSecretDeleted(t, p, false)
	})

	t.Run("valid reference passes and deletes the stale secret", func(t *testing.T) {
		enableStrictBucketOwner(t)
		p := strictTestProvisioner(t, []runtime.Object{strictTestUser(nil)}, []runtime.Object{strictTestSecret(strictTestOBCUID)})
		err := p.enforceStrictBucketOwner(strictTestBucket(p, &owner))
		assert.NoError(t, err)
		assertSecretDeleted(t, p, true)
	})

	t.Run("valid reference passes without a stale secret", func(t *testing.T) {
		enableStrictBucketOwner(t)
		p := strictTestProvisioner(t, []runtime.Object{strictTestUser(nil)}, nil)
		err := p.enforceStrictBucketOwner(strictTestBucket(p, &owner))
		assert.NoError(t, err)
	})

	t.Run("secret not owned by the OBC is left alone", func(t *testing.T) {
		enableStrictBucketOwner(t)
		p := strictTestProvisioner(t, nil, []runtime.Object{strictTestSecret(types.UID("some-other-uid"))})
		err := p.enforceStrictBucketOwner(strictTestBucket(p, nil))
		require.Error(t, err)
		assertSecretDeleted(t, p, false)
	})
}

func TestComposeObjectBucketStrictBucketOwner(t *testing.T) {
	owner := "owner-user"

	newTestProvisioner := func(t *testing.T) (*Provisioner, *bucket) {
		p := strictTestProvisioner(t, nil, nil)
		p.accessKeyID = "AKID"
		p.secretAccessKey = "SECRET"
		p.cephUserName = owner
		p.bucketName = "bkt"
		p.storeDomainName = "s3.example.com"
		p.storePort = 443
		return p, strictTestBucket(p, &owner)
	}

	t.Run("mode off includes access keys", func(t *testing.T) {
		p, b := newTestProvisioner(t)
		ob := p.composeObjectBucket(b)
		require.NotNil(t, ob.Spec.Authentication.AccessKeys)
		assert.Equal(t, "AKID", ob.Spec.Authentication.AccessKeys.AccessKeyID)
		assert.Equal(t, "SECRET", ob.Spec.Authentication.AccessKeys.SecretAccessKey)
	})

	t.Run("strict mode withholds access keys", func(t *testing.T) {
		enableStrictBucketOwner(t)
		p, b := newTestProvisioner(t)
		ob := p.composeObjectBucket(b)
		require.NotNil(t, ob.Spec.Authentication)
		assert.Nil(t, ob.Spec.Authentication.AccessKeys)
		assert.Empty(t, ob.Spec.Authentication.ToMap())
	})
}

func TestDeleteTransitionedGeneratedUser(t *testing.T) {
	newAdminClient := func(t *testing.T, deleteStatus int, deleteBody string, deleteSeen *[]string) *admin.API {
		mockClient := &object.MockClient{
			MockDo: func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodDelete || req.URL.Path != userPath {
					panic(fmt.Sprintf("unexpected request: method %q path %q", req.Method, req.URL.Path))
				}
				*deleteSeen = append(*deleteSeen, req.URL.RawQuery)
				return &http.Response{
					StatusCode: deleteStatus,
					Body:       io.NopCloser(bytes.NewReader([]byte(deleteBody))),
				}, nil
			},
		}
		adminClient, err := admin.New("rgw.test", "accesskey", "secretkey", mockClient)
		require.NoError(t, err)
		return adminClient
	}

	newTestProvisioner := func(t *testing.T, deleteStatus int, deleteBody string, deleteSeen *[]string) (*Provisioner, *bucket) {
		owner := "owner-user"
		p := strictTestProvisioner(t, nil, nil)
		p.cephUserName = owner
		p.adminOpsClient = newAdminClient(t, deleteStatus, deleteBody, deleteSeen)
		return p, strictTestBucket(p, &owner)
	}

	t.Run("mode off does nothing", func(t *testing.T) {
		deleteSeen := []string{}
		p, b := newTestProvisioner(t, 200, `{}`, &deleteSeen)
		err := p.deleteTransitionedGeneratedUser(b)
		assert.NoError(t, err)
		assert.Empty(t, deleteSeen)
	})

	t.Run("generated user is removed", func(t *testing.T) {
		enableStrictBucketOwner(t)
		deleteSeen := []string{}
		p, b := newTestProvisioner(t, 200, `{}`, &deleteSeen)
		err := p.deleteTransitionedGeneratedUser(b)
		assert.NoError(t, err)
		require.Len(t, deleteSeen, 1)
		assert.Contains(t, deleteSeen[0], "uid=obc-"+strictTestNamespace+"-"+strictTestOBCName+"-"+string(strictTestOBCUID))
	})

	t.Run("missing generated user is not an error", func(t *testing.T) {
		enableStrictBucketOwner(t)
		deleteSeen := []string{}
		p, b := newTestProvisioner(t, 404, `{"Code":"NoSuchUser"}`, &deleteSeen)
		err := p.deleteTransitionedGeneratedUser(b)
		assert.NoError(t, err)
		assert.Len(t, deleteSeen, 1)
	})

	t.Run("removal failure is an error", func(t *testing.T) {
		enableStrictBucketOwner(t)
		deleteSeen := []string{}
		p, b := newTestProvisioner(t, 500, `{"Code":"InternalError"}`, &deleteSeen)
		err := p.deleteTransitionedGeneratedUser(b)
		assert.Error(t, err)
	})

	t.Run("bucketOwner adopting the generated user is left alone", func(t *testing.T) {
		enableStrictBucketOwner(t)
		deleteSeen := []string{}
		p, b := newTestProvisioner(t, 200, `{}`, &deleteSeen)
		p.cephUserName = p.genUserName(b.options.ObjectBucketClaim)
		err := p.deleteTransitionedGeneratedUser(b)
		assert.NoError(t, err)
		assert.Empty(t, deleteSeen)
	})
}

func TestAdditionalConfigSpecStrictBucketOwner(t *testing.T) {
	t.Run("strict mode implicitly allows bucketOwner", func(t *testing.T) {
		enableStrictBucketOwner(t)
		opcontroller.SetObcAllowAdditionalConfigFields()

		spec, err := additionalConfigSpecFromMap(map[string]string{"bucketOwner": "foo"})
		require.NoError(t, err)
		require.NotNil(t, spec.bucketOwner)
		assert.Equal(t, "foo", *spec.bucketOwner)
	})
}
