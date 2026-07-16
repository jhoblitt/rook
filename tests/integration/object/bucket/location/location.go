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

// Package location covers OBC/StorageClass locationConstraint handling: bucket
// placement selection at greenfield creation, OBC-over-SC precedence, and the
// ignored-constraint logging for existing buckets.
package location

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ceph/go-ceph/rgw/admin"
	bktv1alpha1 "github.com/kube-object-storage/lib-bucket-provisioner/pkg/apis/objectbucket.io/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/rook/rook/tests/framework/utils"
	"github.com/rook/rook/tests/integration/object/util/fixture"
	"github.com/rook/rook/tests/integration/object/util/obc"
	"github.com/rook/rook/tests/integration/object/util/sharedstore"
	"github.com/rook/rook/tests/integration/object/util/wait4"
)

const Namespace = "test-bucketlocation"

func TestObjectBucketClaimLocationConstraint(t *testing.T, k8sh *utils.K8sHelper, store *sharedstore.Sharedstore) {
	var (
		defaultName = Namespace
		objectStore = store.ObjectStore()
		adminClient = store.AdminClient()

		// the zonegroup is named after the store; the RGW LocationConstraint
		// format is "<zonegroup>[:<placement-target>]"
		placementFQ = objectStore.Name + ":" + sharedstore.PlacementLocA

		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: defaultName,
			},
		}

		// no locationConstraint parameter
		scPlain = obc.StorageClass(defaultName+"-plain", objectStore)

		// carries an admin-default locationConstraint
		scLoc = obc.StorageClass(defaultName+"-loc", objectStore)

		// brownfield class granting access to obcScLoc's bucket; its
		// locationConstraint must be ignored
		scBrown = obc.StorageClass(defaultName+"-brown", objectStore)

		obcScLoc = bktv1alpha1.ObjectBucketClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      defaultName + "-sc-loc",
				Namespace: ns.Name,
			},
			Spec: bktv1alpha1.ObjectBucketClaimSpec{
				BucketName:       defaultName + "-sc-loc",
				StorageClassName: scLoc.Name,
			},
		}

		// the ":<placement-target>" short form selects a placement in the
		// local zonegroup
		obcObcLoc = bktv1alpha1.ObjectBucketClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      defaultName + "-obc-loc",
				Namespace: ns.Name,
			},
			Spec: bktv1alpha1.ObjectBucketClaimSpec{
				BucketName:       defaultName + "-obc-loc",
				StorageClassName: scPlain.Name,
				AdditionalConfig: map[string]string{
					"locationConstraint": ":" + sharedstore.PlacementLocA,
				},
			},
		}

		obcObcWins = bktv1alpha1.ObjectBucketClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      defaultName + "-obc-wins",
				Namespace: ns.Name,
			},
			Spec: bktv1alpha1.ObjectBucketClaimSpec{
				BucketName:       defaultName + "-obc-wins",
				StorageClassName: scLoc.Name,
				AdditionalConfig: map[string]string{
					"locationConstraint": objectStore.Name + ":default",
				},
			},
		}

		obcDefault = bktv1alpha1.ObjectBucketClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      defaultName + "-default",
				Namespace: ns.Name,
			},
			Spec: bktv1alpha1.ObjectBucketClaimSpec{
				BucketName:       defaultName + "-default",
				StorageClassName: scPlain.Name,
			},
		}

		// the "FOO" storage class is defined on the shared store's "default"
		// placement
		obcFoo = bktv1alpha1.ObjectBucketClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      defaultName + "-foo",
				Namespace: ns.Name,
			},
			Spec: bktv1alpha1.ObjectBucketClaimSpec{
				BucketName:       defaultName + "-foo",
				StorageClassName: scPlain.Name,
				AdditionalConfig: map[string]string{
					"locationConstraint": objectStore.Name + ":default",
					"bucketStorageClass": "FOO",
				},
			},
		}

		// brownfield: the bucket name comes from the StorageClass
		obcBrown = bktv1alpha1.ObjectBucketClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      defaultName + "-brown",
				Namespace: ns.Name,
			},
			Spec: bktv1alpha1.ObjectBucketClaimSpec{
				StorageClassName: scBrown.Name,
			},
		}

		obcBogus = bktv1alpha1.ObjectBucketClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      defaultName + "-bogus",
				Namespace: ns.Name,
			},
			Spec: bktv1alpha1.ObjectBucketClaimSpec{
				BucketName:       defaultName + "-bogus",
				StorageClassName: scPlain.Name,
				AdditionalConfig: map[string]string{
					"locationConstraint": "bogus:nowhere",
				},
			},
		}

		obcClient        = k8sh.BucketClientset.ObjectbucketV1alpha1().ObjectBucketClaims(ns.Name)
		operatorSelector = labels.SelectorFromSet(labels.Set{
			"app": "rook-ceph-operator",
		})
	)

	t.Run("OBC locationConstraint", func(t *testing.T) {
		ctx := t.Context()

		fixture.RequireNamespace(t, k8sh, ns)

		scLoc.Parameters["locationConstraint"] = placementFQ
		fixture.RequireStorageClass(t, k8sh, scLoc)
		fixture.RequireStorageClass(t, k8sh, scPlain)

		scBrown.Parameters["bucketName"] = obcScLoc.Spec.BucketName
		// the ":default" short form is distinct from the re-provision case's
		// requested value so the log assertions cannot match each other's
		// lines
		scBrown.Parameters["locationConstraint"] = ":default"
		fixture.RequireStorageClass(t, k8sh, scBrown)

		t.Run(fmt.Sprintf("greenfield bucket via SC parameter lands in placement %q", sharedstore.PlacementLocA), func(t *testing.T) {
			obc.RequireBound(ctx, t, k8sh, &obcScLoc)

			bucket, err := adminClient.GetBucketInfo(ctx, admin.Bucket{Bucket: obcScLoc.Spec.BucketName})
			require.NoError(t, err)
			assert.Equal(t, sharedstore.PlacementLocA, bucket.PlacementRule)
		})

		t.Run(fmt.Sprintf("greenfield bucket via OBC additionalConfig lands in placement %q", sharedstore.PlacementLocA), func(t *testing.T) {
			obc.RequireBound(ctx, t, k8sh, &obcObcLoc)

			bucket, err := adminClient.GetBucketInfo(ctx, admin.Bucket{Bucket: obcObcLoc.Spec.BucketName})
			require.NoError(t, err)
			assert.Equal(t, sharedstore.PlacementLocA, bucket.PlacementRule)
		})

		t.Run("OBC additionalConfig overrides the SC parameter", func(t *testing.T) {
			obc.RequireBound(ctx, t, k8sh, &obcObcWins)

			bucket, err := adminClient.GetBucketInfo(ctx, admin.Bucket{Bucket: obcObcWins.Spec.BucketName})
			require.NoError(t, err)
			assert.Equal(t, "default", bucket.PlacementRule)
		})

		t.Run("bucket without locationConstraint lands in the default placement", func(t *testing.T) {
			obc.RequireBound(ctx, t, k8sh, &obcDefault)

			bucket, err := adminClient.GetBucketInfo(ctx, admin.Bucket{Bucket: obcDefault.Spec.BucketName})
			require.NoError(t, err)
			assert.Equal(t, "default", bucket.PlacementRule)
		})

		t.Run("bucketStorageClass is recorded in the bucket placement rule", func(t *testing.T) {
			obc.RequireBound(ctx, t, k8sh, &obcFoo)

			bucket, err := adminClient.GetBucketInfo(ctx, admin.Bucket{Bucket: obcFoo.Spec.BucketName})
			require.NoError(t, err)
			assert.Equal(t, "default/FOO", bucket.PlacementRule)
		})

		t.Run("locationConstraint is ignored for an existing bucket on re-provision", func(t *testing.T) {
			requested := objectStore.Name + ":default"
			obc.Update(ctx, t, k8sh, ns.Name, obcScLoc.Name, func(live *bktv1alpha1.ObjectBucketClaim) {
				live.Spec.AdditionalConfig = map[string]string{"locationConstraint": requested}
			})

			wait4.RequirePodLog(ctx, t, k8sh, "object-ns-system", operatorSelector, wait4.TimeoutShort, func(line string) bool {
				return strings.Contains(line, "ignoring requested locationConstraint") &&
					strings.Contains(line, fmt.Sprintf("%q", requested)) &&
					strings.Contains(line, obcScLoc.Spec.BucketName)
			})

			liveObc, err := obcClient.Get(ctx, obcScLoc.Name, metav1.GetOptions{})
			require.NoError(t, err)
			assert.True(t, bktv1alpha1.ObjectBucketClaimStatusPhaseBound == liveObc.Status.Phase)

			bucket, err := adminClient.GetBucketInfo(ctx, admin.Bucket{Bucket: obcScLoc.Spec.BucketName})
			require.NoError(t, err)
			assert.Equal(t, sharedstore.PlacementLocA, bucket.PlacementRule)
		})

		t.Run("brownfield Grant ignores the SC locationConstraint", func(t *testing.T) {
			bucket, err := adminClient.GetBucketInfo(ctx, admin.Bucket{Bucket: obcScLoc.Spec.BucketName})
			require.NoError(t, err)

			// Grant manages the bucket policy with the OBC user's own S3
			// credentials, so a brownfield claim by a user without access to
			// the bucket cannot bind (GetBucketPolicy fails with AccessDenied);
			// claim the bucket as its owner instead
			obcBrown.Spec.AdditionalConfig = map[string]string{"bucketOwner": bucket.Owner}

			obc.RequireBound(ctx, t, k8sh, &obcBrown)

			wait4.RequirePodLog(ctx, t, k8sh, "object-ns-system", operatorSelector, wait4.TimeoutShort, func(line string) bool {
				return strings.Contains(line, "ignoring requested locationConstraint") &&
					strings.Contains(line, `":default"`) &&
					strings.Contains(line, obcScLoc.Spec.BucketName)
			})

			bucket, err = adminClient.GetBucketInfo(ctx, admin.Bucket{Bucket: obcScLoc.Spec.BucketName})
			require.NoError(t, err)
			assert.Equal(t, sharedstore.PlacementLocA, bucket.PlacementRule)
		})

		// "failure" means the obc remains in Pending state
		t.Run("invalid locationConstraint fails provisioning", func(t *testing.T) {
			_, err := obcClient.Create(ctx, &obcBogus, metav1.CreateOptions{})
			require.NoError(t, err)

			// RGW rejects the unknown zonegroup/placement with the
			// InvalidLocationConstraint api error code
			wait4.RequirePodLog(ctx, t, k8sh, "object-ns-system", operatorSelector, wait4.TimeoutShort, func(line string) bool {
				return strings.Contains(line, "InvalidLocationConstraint") &&
					strings.Contains(line, obcBogus.Spec.BucketName)
			})

			liveObc, err := obcClient.Get(ctx, obcBogus.Name, metav1.GetOptions{})
			require.NoError(t, err)
			assert.True(t, bktv1alpha1.ObjectBucketClaimStatusPhasePending == liveObc.Status.Phase)
		})

		// the brownfield obc goes first: deleting the greenfield obcScLoc
		// removes the shared bucket
		for _, name := range []string{obcBrown.Name, obcScLoc.Name, obcObcLoc.Name, obcObcWins.Name, obcDefault.Name, obcFoo.Name, obcBogus.Name} {
			t.Run(fmt.Sprintf("delete obc %q", name), func(t *testing.T) {
				obc.DeleteAndWait(ctx, t, k8sh, ns.Name, name)
			})
		}
	})
}
