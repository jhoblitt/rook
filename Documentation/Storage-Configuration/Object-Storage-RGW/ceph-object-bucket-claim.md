---
title: Bucket Claim
---

Rook supports the creation of new buckets and access to existing buckets via two custom resources:

* an `Object Bucket Claim (OBC)` is custom resource which requests a bucket (new or existing) and is described by a Custom Resource Definition (CRD) shown below.
* an `Object Bucket (OB)` is a custom resource automatically generated when a bucket is provisioned. It is a global resource, typically not visible to non-admin users, and contains information specific to the bucket. It is described by an OB CRD, also shown below.

An OBC references a storage class which is created by an administrator. The storage class defines whether the bucket requested is a new bucket or an existing bucket. It also defines the bucket retention policy.
Users request a new or existing bucket by creating an OBC which is shown below. The ceph provisioner detects the OBC and creates a new bucket or grants access to an existing bucket, depending the storage class referenced in the OBC. It also generates a Secret which provides credentials to access the bucket, and a ConfigMap which contains the bucket's endpoint. Application pods consume the information in the Secret and ConfigMap to access the bucket. Please note that to make provisioner watch the cluster namespace only you need to set `ROOK_OBC_WATCH_OPERATOR_NAMESPACE` to `true` in the operator manifest, otherwise it watches all namespaces.

The OBC provisioner name found in the storage class by default includes the operator namespace as a prefix. A custom prefix can be applied by the operator setting in the `rook-ceph-operator-config` configmap: `ROOK_OBC_PROVISIONER_NAME_PREFIX`.

!!! Note
    Changing the prefix is not supported on existing clusters. This may impact the function of existing OBCs.

## Example

### OBC Custom Resource

```yaml
apiVersion: objectbucket.io/v1alpha1
kind: ObjectBucketClaim
metadata:
  name: ceph-bucket [1]
  namespace: rook-ceph [2]
spec:
  bucketName: [3]
  generateBucketName: photo-booth [4]
  storageClassName: rook-ceph-bucket [5]
  additionalConfig: [6]
    maxObjects: "1000"
    maxSize: "2G"
    bucketMaxObjects: "3000"
    bucketMaxSize: "4G"
    bucketPolicy: |
      {
          "Version": "2012-10-17",
          "Statement": [
              {
                  "Effect": "Allow",
                  "Principal": {
                      "AWS": "arn:aws:iam:::user/bar"
                  },
                  "Action": [
                      "s3:GetObject",
                      "s3:ListBucket",
                      "s3:GetBucketLocation"
                  ],
                  "Resource": [
                      "arn:aws:s3:::bucket-policy-test/*"
                  ]
              }
          ]
      }
    bucketLifecycle: |
      {
        "Rules": [
          {
            "ID": "AbortIncompleteMultipartUploads",
            "Status": "Enabled",
            "Prefix": "",
            "AbortIncompleteMultipartUpload": {
              "DaysAfterInitiation": 1
            }
          },
          {
            "ID": "ExpireAfter30Days",
            "Status": "Enabled",
            "Prefix": "",
            "Expiration": {
              "Days": 30
            }
          }
        ]
      }
    bucketOwner: "rgw-user"
```

1. `name` of the `ObjectBucketClaim`. This name becomes the name of the Secret and ConfigMap.
2. `namespace`(optional) of the `ObjectBucketClaim`, which is also the namespace of the ConfigMap and Secret.
3. `bucketName` name of the `bucket`.
**Not** recommended for new buckets since names must be unique within
an entire object store.
4. `generateBucketName` value becomes the prefix for a randomly generated name, if supplied then `bucketName` must be empty.
If both `bucketName` and `generateBucketName` are supplied then `BucketName` has precedence and `GenerateBucketName` is ignored.
If both `bucketName` and `generateBucketName` are blank or omitted then the storage class is expected to contain the name of an _existing_ bucket. It's an error if all three bucket related names are blank or omitted.
5. `storageClassName` which defines the StorageClass which contains the names of the bucket provisioner, the object-store and specifies the bucket retention policy.
6. `additionalConfig` is an optional list of key-value pairs used to define attributes specific to the bucket being provisioned by this OBC. This information is typically tuned to a particular bucket provisioner and may limit application portability. Options supported:

    * `maxObjects`: The maximum number of objects in the bucket as a quota on the user account automatically created for the bucket.
    * `maxSize`: The maximum size of the bucket as a quota on the user account automatically created for the bucket. Please note minimum recommended value is 4K.
    * `bucketMaxObjects`: (disabled by default) The maximum number of objects in the bucket as an individual bucket quota. This is useful when the bucket is shared among multiple users.
    * `bucketMaxSize`: (disabled by default) The maximum size of the bucket as an individual bucket quota.
    * `bucketPolicy`: (disabled by default) A raw JSON format string that defines an AWS S3 format the bucket policy. If set, the policy string will override any existing policy set on the bucket and any default bucket policy that the bucket provisioner potentially would have automatically generated.
    * `bucketLifecycle`: (disabled by default) A raw JSON format string that defines an AWS S3 format bucket lifecycle configuration. Note that the rules must be sorted by `ID` in order to be idempotent.
    * `bucketOwner`: (disabled by default)  The name of a pre-existing ceph rgw user account that will own the bucket. A `CephObjectStoreUser` resource may be used to create an ceph rgw user account. If the bucket already exists and is owned by a different user, the bucket will be re-linked to the specified user.

Several OBC `additionalConfig` fields are disabled by default. Default-disabled additional config
fields may be risky for administrators to allow users control over, and they should be enabled only
with caution.

The default allowed fields are `maxObjects` and `maxSize`. These are designed to fit into the OBC
framework's original design goals. Other fields can be allowed but exert control outside of OBC's
original design goals and should be considered risky. At best, users may be able to break their own
OBCs in unexpected ways. At worst, users may brick the whole S3 object store for all users
(`bucketPolicy` in particular). Administrators should take care to enable features only when they
are personally willing to take on the risks.

OBC `additionalConfig` fields can be enabled and disabled using the `rook-ceph-operator-config`
configmap value `ROOK_OBC_ALLOW_ADDITIONAL_CONFIG_FIELDS`.

### Strict bucketOwner mode (experimental)

By default, any OBC allowed to set `bucketOwner` can name an arbitrary rgw user, and the OBC's
credentials secret receives that user's S3 keys. That effectively lets anyone who can create an OBC
read the S3 credentials of any rgw user on the object store.

Setting `ROOK_OBC_STRICT_BUCKET_OWNER: "true"` in the `rook-ceph-operator-config` configmap
enables a strict mode that changes OBC provisioning behavior:

* Every OBC must set `additionalConfig.bucketOwner` (the field is implicitly allowed, without an
    entry in `ROOK_OBC_ALLOW_ADDITIONAL_CONFIG_FIELDS`). The provisioner no longer generates
    per-OBC rgw users.
* `bucketOwner` must be the name of a `CephObjectStoreUser` in the same namespace as the OBC, and
    that user must belong to the same object store as the OBC's storage class. For an OBC in a
    namespace other than the Ceph cluster namespace, the `CephObjectStoreUser` must set
    `spec.clusterNamespace`.
* OBC credentials secrets never contain S3 keys. Applications read credentials from the
    referenced `CephObjectStoreUser`'s own secret (`rook-ceph-object-user-<store>-<user>`) instead.
    The OBC configmap still provides the bucket endpoint. (The OBC secret object itself still
    exists, but empty: the bucket provisioning library unconditionally creates it.)
* S3 keys handed out before strict mode was enabled are revoked as OBCs are reconciled: a
    conforming OBC's secret is deleted and recreated without keys, and a violating OBC's secret is
    deleted outright.
* When an OBC created before strict mode transitions to an explicit `bucketOwner`, its bucket is
    relinked to that owner and the rgw user the provisioner had generated for the OBC is removed,
    revoking that user's keys. (Exception: for brownfield OBCs — pre-existing buckets named by the
    storage class — the previously generated user is not removed automatically and should be
    cleaned up manually.)
* An OBC that violates the policy fails provisioning.
* Deleting a `CephObjectStoreUser` is blocked while any OBC references it via `bucketOwner`;
    delete the referencing OBCs first.

Enabling strict mode on a cluster with existing OBCs breaks every OBC that does not conform, and
removes the S3 keys from all OBC credentials secrets as the OBCs are reconciled. Before enabling
it, create a `CephObjectStoreUser` for each OBC's owner in the OBC's namespace, set
`additionalConfig.bucketOwner` on every OBC, and switch applications to consume the
`CephObjectStoreUser` secret.

### OBC Custom Resource after Bucket Provisioning

```yaml
apiVersion: objectbucket.io/v1alpha1
kind: ObjectBucketClaim
metadata:
  creationTimestamp: "2019-10-18T09:54:01Z"
  generation: 2
  name: ceph-bucket
  namespace: default [1]
  resourceVersion: "559491"
spec:
  ObjectBucketName: obc-default-ceph-bucket [2]
  additionalConfig: null
  bucketName: photo-booth-c1178d61-1517-431f-8408-ec4c9fa50bee [3]
  storageClassName: rook-ceph-bucket [4]
status:
  phase: Bound [5]
```

1. `namespace` where OBC got created.
2. `ObjectBucketName` generated OB name created using name space and OBC name.
3. the generated (in this case), unique `bucket name` for the new bucket.
4. name of the storage class from OBC got created.
5. phases of bucket creation:
    * _Pending_: the operator is processing the request.
    * _Bound_: the operator finished processing the request and linked the OBC and OB
    * _Released_: the OB has been deleted, leaving the OBC unclaimed but unavailable.
    * _Failed_: not currently set.

### App Pod

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: app-pod
  namespace: dev-user
spec:
  containers:
  - name: mycontainer
    image: redis
    envFrom: [1]
    - configMapRef:
        name: ceph-bucket [2]
    - secretRef:
        name: ceph-bucket [3]
```

1. use `env:` if mapping of the defined key names to the env var names used by the app is needed.
2. makes available to the pod as env variables: `BUCKET_HOST`, `BUCKET_PORT`, `BUCKET_NAME`
3. makes available to the pod as env variables: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`

### StorageClass

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: rook-ceph-bucket
  labels:
    aws-s3/object [1]
provisioner: rook-ceph.ceph.rook.io/bucket [2]
parameters: [3]
  objectStoreName: my-store
  objectStoreNamespace: rook-ceph
  bucketName: ceph-bucket [4]
reclaimPolicy: Delete [5]
```

1. `label`(optional) here associates this `StorageClass` to a specific provisioner.
2. `provisioner` responsible for handling `OBCs` referencing this `StorageClass`.
3. **all** `parameter` required.
4. `bucketName` is required for access to existing buckets but is omitted when provisioning new buckets.
    Unlike greenfield provisioning, the brownfield bucket name appears in the `StorageClass`, not the `OBC`.
5. rook-ceph provisioner decides how to treat the `reclaimPolicy` when an `OBC` is deleted for the bucket. See explanation as [specified in Kubernetes](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#retain)

    * _Delete_ = physically delete the bucket.
    * _Retain_ = do not physically delete the bucket.
