  ## Findings

  1. High — UploadPartCopy still has the original source-authorization race.
     internal/s3api/multipart.go:120 evaluates source bucket conditions using the destination bucket and never records
     WithExpectedSource. internal/object/copy.go:89 then rereads the source without checking sourceExpected. An
     authorized source can therefore be replaced before copying, potentially exposing the replacement.

  2. High — bucket-incarnation binding still misses write paths.
     Browser POST returns before attaching the expectation at internal/s3api/auth.go:170, multipart initiation commits
     without rechecking the bucket at internal/object/multipart.go:76, and bucket deletion still uses getLiveBucket
     rather than liveBucket at internal/object/service.go:282. Requests authorized against a deleted bucket can
     consequently affect a newly created bucket with the same name.

  3. High — bucket ACL WRITE still overgrants tagging and upload attributes.
     internal/iam/authz.go:206 still maps bucket WRITE to PutObjectTagging and DeleteObjectTagging, while
     internal/s3api/helpers.go:476 permits ACL-granted uploads to set ACLs and tags without the corresponding policy
     actions. AWS maps bucket WRITE to s3:PutObject, while uploads containing ACLs or tags conditionally require
     s3:PutObjectAcl or s3:PutObjectTagging. AWS ACL mapping
     (https://docs.aws.amazon.com/AmazonS3/latest/userguide/acl-overview.html), AWS operation permissions
     (https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-with-s3-policy-actions.html).

  4. Medium — DeleteObjectTagging does not load or bind the object.
     Unlike PutObjectTagging, its route at internal/s3api/router.go:256 does not set needsObject. Therefore
     s3:ExistingObjectTag conditions are evaluated without the tags, and the expectation check at
     internal/object/tagging.go:36 receives no expected sequence. This can bypass tag-based denies or modify a
     replacement object. AWS service authorization reference
     (https://docs.aws.amazon.com/service-authorization/latest/reference/list_amazons3.html).

  5. Medium — CopyObject now requires an extra, non-AWS source permission.
     internal/s3api/object.go:341 requires s3:GetObjectTagging when copying tags, and the test encodes that requirement
     at tests/integration/security_test.go:228. AWS documents GetObject/GetObjectVersion for the source and
     PutObjectTagging for the destination—not GetObjectTagging on the source. AWS operation permissions
     (https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-with-s3-policy-actions.html).
