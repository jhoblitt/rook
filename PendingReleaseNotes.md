# v1.21 Pending Release Notes

## Breaking Changes

- Helm OCI chart tags no longer include the `v` prefix (e.g., `1.21.0` instead of `v1.21.0`). Update any scripts or tooling that reference the chart by tag.

## Features

- RBD QoS (Quality of Service) support via `VolumeAttributesClass` using the krbd mounter with cgroup v2 `io.max` enforcement. See the [RBD QoS documentation](Documentation/Storage-Configuration/Block-Storage-RBD/rbd-qos.md) for details.
- Object: buckets provisioned via ObjectBucketClaim can request a bucket location/placement and a default storage class with the `locationConstraint` and `bucketStorageClass` StorageClass parameters or OBC `additionalConfig` fields (the OBC fields are disabled by default). The values are passed to RGW as the S3 CreateBucket `LocationConstraint` and the `X-Amz-Storage-Class` header.
