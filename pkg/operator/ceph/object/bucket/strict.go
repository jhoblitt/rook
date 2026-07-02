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
	"slices"

	"github.com/ceph/go-ceph/rgw/admin"
	bktv1alpha1 "github.com/kube-object-storage/lib-bucket-provisioner/pkg/apis/objectbucket.io/v1alpha1"
	apibkt "github.com/kube-object-storage/lib-bucket-provisioner/pkg/provisioner/api"
	"github.com/pkg/errors"
	opcontroller "github.com/rook/rook/pkg/operator/ceph/controller"
	"github.com/rook/rook/pkg/util/log"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const bucketProvisionerFinalizer = apibkt.Domain + "/finalizer"

// enforceStrictBucketOwner applies the ROOK_OBC_STRICT_BUCKET_OWNER policy:
// every OBC must set additionalConfig.bucketOwner to the name of a
// CephObjectStoreUser in the OBC's own namespace that belongs to the OBC's
// object store. In strict mode no OBC credentials secret may exist, so any
// secret left over from provisioning done before the mode was enabled is
// deleted whether the OBC conforms or not. Errors that may be transient
// (e.g. API server failures) fail the reconcile without touching the secret.
func (p *Provisioner) enforceStrictBucketOwner(bucket *bucket) error {
	if !opcontroller.ObcStrictBucketOwner() {
		return nil
	}

	obc := bucket.options.ObjectBucketClaim

	bucketOwner := bucket.additionalConfig.bucketOwner
	if bucketOwner == nil {
		p.deleteCredentialsSecret(obc)
		return errors.Errorf("strict bucketOwner mode: OBC %q/%q must set additionalConfig.bucketOwner to the name of a CephObjectStoreUser in namespace %q", obc.Namespace, obc.Name, obc.Namespace)
	}

	user, err := p.context.RookClientset.CephV1().CephObjectStoreUsers(obc.Namespace).Get(p.clusterInfo.Context, *bucketOwner, metav1.GetOptions{})
	if err != nil {
		if kerrors.IsNotFound(err) {
			p.deleteCredentialsSecret(obc)
			return errors.Errorf("strict bucketOwner mode: bucketOwner %q of OBC %q/%q does not reference a CephObjectStoreUser in namespace %q", *bucketOwner, obc.Namespace, obc.Name, obc.Namespace)
		}
		return errors.Wrapf(err, "failed to get CephObjectStoreUser %q/%q referenced by OBC %q", obc.Namespace, *bucketOwner, obc.Name)
	}

	if !user.GetDeletionTimestamp().IsZero() {
		return errors.Errorf("strict bucketOwner mode: CephObjectStoreUser %q/%q referenced by OBC %q is being deleted", user.Namespace, user.Name, obc.Name)
	}

	if user.Spec.Store != p.objectStoreName {
		p.deleteCredentialsSecret(obc)
		return errors.Errorf("strict bucketOwner mode: CephObjectStoreUser %q/%q belongs to object store %q, but OBC %q uses object store %q", user.Namespace, user.Name, user.Spec.Store, obc.Name, p.objectStoreName)
	}

	userClusterNamespace := user.Spec.ClusterNamespace
	if userClusterNamespace == "" {
		userClusterNamespace = user.Namespace
	}
	if userClusterNamespace != p.clusterInfo.Namespace {
		p.deleteCredentialsSecret(obc)
		return errors.Errorf("strict bucketOwner mode: CephObjectStoreUser %q/%q targets Ceph cluster namespace %q, but OBC %q uses a CephObjectStore in namespace %q", user.Namespace, user.Name, userClusterNamespace, obc.Name, p.clusterInfo.Namespace)
	}

	// the OBC conforms; remove any credentials secret left over from
	// provisioning done before strict mode was enabled
	p.deleteCredentialsSecret(obc)

	return nil
}

// deleteTransitionedGeneratedUser removes the rgw user the provisioner
// generated for the OBC before strict bucketOwner mode took effect. Relinking
// the bucket to the explicit bucketOwner orphans the generated user with
// valid S3 keys, and once the OB records the new owner nothing else remembers
// the generated user's name. The name is deterministic, so this runs on every
// reconcile after ownership is settled rather than only at relink time; a
// removal that fails is retried by the next reconcile.
func (p *Provisioner) deleteTransitionedGeneratedUser(bucket *bucket) error {
	if !opcontroller.ObcStrictBucketOwner() {
		return nil
	}

	obc := bucket.options.ObjectBucketClaim
	generatedUser := p.genUserName(obc)
	// an explicit bucketOwner may deliberately adopt the generated user
	if generatedUser == p.cephUserName {
		return nil
	}

	err := p.adminOpsClient.RemoveUser(p.clusterInfo.Context, admin.User{ID: generatedUser})
	if err != nil {
		if errors.Is(err, admin.ErrNoSuchUser) {
			return nil
		}
		return errors.Wrapf(err, "failed to remove provisioner-generated user %q orphaned by OBC %q/%q transitioning to bucketOwner %q", generatedUser, obc.Namespace, obc.Name, p.cephUserName)
	}

	log.NamedInfo(p.objectContext.NsName(), logger, "removed provisioner-generated user %q orphaned by OBC %q/%q transitioning to bucketOwner %q", generatedUser, obc.Namespace, obc.Name, p.cephUserName)
	return nil
}

// deleteCredentialsSecret removes the credentials secret lib-bucket created for
// the OBC, revoking S3 keys handed out before strict mode was enabled. For an
// OBC that fails the strict policy the reconcile fails, so lib-bucket does not
// recreate the secret; for a conforming OBC lib-bucket recreates it after a
// successful Provision/Grant, but with no keys in it (see
// composeObjectBucket). Best-effort: a failed deletion is retried on the next
// reconcile.
func (p *Provisioner) deleteCredentialsSecret(obc *bktv1alpha1.ObjectBucketClaim) {
	nsName := p.objectContext.NsName()

	// lib-bucket names the credentials secret after the OBC
	secrets := p.context.Clientset.CoreV1().Secrets(obc.Namespace)
	secret, err := secrets.Get(p.clusterInfo.Context, obc.Name, metav1.GetOptions{})
	if err != nil {
		if !kerrors.IsNotFound(err) {
			log.NamedWarning(nsName, logger, "failed to get credentials secret of OBC %q/%q. %v", obc.Namespace, obc.Name, err)
		}
		return
	}

	ownedByOBC := slices.ContainsFunc(secret.OwnerReferences, func(ref metav1.OwnerReference) bool {
		return ref.UID == obc.UID
	})
	if !ownedByOBC {
		log.NamedWarning(nsName, logger, "not deleting secret %q/%q: it is not owned by OBC %q", secret.Namespace, secret.Name, obc.Name)
		return
	}

	finalizers := slices.DeleteFunc(slices.Clone(secret.Finalizers), func(f string) bool {
		return f == bucketProvisionerFinalizer
	})
	if len(finalizers) != len(secret.Finalizers) {
		secret.Finalizers = finalizers
		if secret, err = secrets.Update(p.clusterInfo.Context, secret, metav1.UpdateOptions{}); err != nil {
			log.NamedWarning(nsName, logger, "failed to remove finalizer from credentials secret of OBC %q/%q. %v", obc.Namespace, obc.Name, err)
			return
		}
	}

	if err := secrets.Delete(p.clusterInfo.Context, secret.Name, metav1.DeleteOptions{}); err != nil && !kerrors.IsNotFound(err) {
		log.NamedWarning(nsName, logger, "failed to delete credentials secret of OBC %q/%q. %v", obc.Namespace, obc.Name, err)
		return
	}
	log.NamedInfo(nsName, logger, "deleted credentials secret of OBC %q/%q", obc.Namespace, obc.Name)
}
