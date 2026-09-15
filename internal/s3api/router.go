package s3api

import (
	"net/http"
	"net/url"
	"strings"
)

// operation describes a routed API operation.
type operation struct {
	name    string // AWS operation name, e.g. GetObject
	action  string // IAM action, e.g. s3:GetObject ("" = no authorisation beyond auth)
	handler func(s *Server, c *reqCtx) error
	// level: 0 service, 1 bucket, 2 object
	level int
	// needsObject: load object metadata before authorisation (for ACLs and
	// tag-based conditions).
	needsObject bool
}

func has(q url.Values, k string) bool { _, ok := q[k]; return ok }

// route selects the operation from method, target and query subresources.
func route(c *reqCtx) *operation {
	q := c.r.URL.Query()
	m := c.r.Method
	if m == http.MethodOptions {
		return &operation{name: "PreflightOptions"}
	}
	if c.bucket == "" {
		if m == http.MethodGet {
			return &operation{name: "ListBuckets", action: "s3:ListAllMyBuckets", handler: (*Server).listBuckets}
		}
		if m == http.MethodPost && (has(q, "Action") || strings.HasPrefix(c.r.Header.Get("Content-Type"), "application/x-www-form-urlencoded")) {
			return &operation{name: "AWSQuery", handler: (*Server).awsQuery}
		}
		return nil
	}
	if c.key == "" {
		return routeBucket(c, m, q)
	}
	return routeObject(c, m, q)
}

func bucketOp(name, action string, h func(*Server, *reqCtx) error) *operation {
	return &operation{name: name, action: action, handler: h, level: 1}
}

func objectOp(name, action string, h func(*Server, *reqCtx) error) *operation {
	return &operation{name: name, action: action, handler: h, level: 2}
}

// unsupportedSubresources are query subresources of newer S3 features
// (directory buckets, S3 Metadata, ABAC, annotations, rename, in-place
// re-encryption) that OpenS3 does not implement. They must be recognised so
// that e.g. "DELETE /bucket?metadataConfiguration" is answered with
// NotImplemented instead of falling through to DeleteBucket, and
// "PUT /bucket/key?renameObject" does not become a PutObject.
var unsupportedSubresources = map[string]string{
	// bucket level
	"session":                 "CreateSession",
	"abac":                    "BucketAbac",
	"metadataConfiguration":   "BucketMetadataConfiguration",
	"metadataTable":           "BucketMetadataTableConfiguration",
	"metadataInventoryTable":  "UpdateBucketMetadataInventoryTableConfiguration",
	"metadataJournalTable":    "UpdateBucketMetadataJournalTableConfiguration",
	"metadataAnnotationTable": "UpdateBucketMetadataAnnotationTableConfiguration",
	// object level
	"renameObject": "RenameObject",
	"annotation":   "ObjectAnnotation",
	"encryption":   "UpdateObjectEncryption", // "?encryption" on a bucket is routed above
}

// unsupportedOp answers an authenticated request for a recognised but
// unimplemented subresource with NotImplemented.
func unsupportedOp(name string, level int) *operation {
	return &operation{name: name, level: level, handler: func(*Server, *reqCtx) error {
		return errNotImplemented(name + " is not supported")
	}}
}

func routeUnsupported(q url.Values, level int, skip ...string) *operation {
	for sub, name := range unsupportedSubresources {
		if !has(q, sub) {
			continue
		}
		skipped := false
		for _, sk := range skip {
			if sk == sub {
				skipped = true
			}
		}
		if !skipped {
			return unsupportedOp(name, level)
		}
	}
	return nil
}

func routeBucket(c *reqCtx, m string, q url.Values) *operation {
	if op := routeUnsupported(q, 1, "encryption"); op != nil {
		return op
	}
	type sub struct {
		q      string
		get    *operation
		put    *operation
		delete *operation
	}
	subs := []sub{
		{"versioning", bucketOp("GetBucketVersioning", "s3:GetBucketVersioning", (*Server).getBucketVersioning), bucketOp("PutBucketVersioning", "s3:PutBucketVersioning", (*Server).putBucketVersioning), nil},
		{"tagging", bucketOp("GetBucketTagging", "s3:GetBucketTagging", (*Server).getBucketTagging), bucketOp("PutBucketTagging", "s3:PutBucketTagging", (*Server).putBucketTagging), bucketOp("DeleteBucketTagging", "s3:PutBucketTagging", (*Server).deleteBucketTagging)},
		{"policy", bucketOp("GetBucketPolicy", "s3:GetBucketPolicy", (*Server).getBucketPolicy), bucketOp("PutBucketPolicy", "s3:PutBucketPolicy", (*Server).putBucketPolicy), bucketOp("DeleteBucketPolicy", "s3:DeleteBucketPolicy", (*Server).deleteBucketPolicy)},
		{"policyStatus", bucketOp("GetBucketPolicyStatus", "s3:GetBucketPolicyStatus", (*Server).getBucketPolicyStatus), nil, nil},
		{"cors", bucketOp("GetBucketCors", "s3:GetBucketCORS", (*Server).getBucketCORS), bucketOp("PutBucketCors", "s3:PutBucketCORS", (*Server).putBucketCORS), bucketOp("DeleteBucketCors", "s3:PutBucketCORS", (*Server).deleteBucketCORS)},
		{"lifecycle", bucketOp("GetBucketLifecycleConfiguration", "s3:GetLifecycleConfiguration", (*Server).getBucketLifecycle), bucketOp("PutBucketLifecycleConfiguration", "s3:PutLifecycleConfiguration", (*Server).putBucketLifecycle), bucketOp("DeleteBucketLifecycle", "s3:PutLifecycleConfiguration", (*Server).deleteBucketLifecycle)},
		{"encryption", bucketOp("GetBucketEncryption", "s3:GetEncryptionConfiguration", (*Server).getBucketEncryption), bucketOp("PutBucketEncryption", "s3:PutEncryptionConfiguration", (*Server).putBucketEncryption), bucketOp("DeleteBucketEncryption", "s3:PutEncryptionConfiguration", (*Server).deleteBucketEncryption)},
		{"object-lock", bucketOp("GetObjectLockConfiguration", "s3:GetBucketObjectLockConfiguration", (*Server).getObjectLockConfig), bucketOp("PutObjectLockConfiguration", "s3:PutBucketObjectLockConfiguration", (*Server).putObjectLockConfig), nil},
		{"acl", bucketOp("GetBucketAcl", "s3:GetBucketAcl", (*Server).getBucketACL), bucketOp("PutBucketAcl", "s3:PutBucketAcl", (*Server).putBucketACL), nil},
		{"notification", bucketOp("GetBucketNotificationConfiguration", "s3:GetBucketNotification", (*Server).getBucketNotification), bucketOp("PutBucketNotificationConfiguration", "s3:PutBucketNotification", (*Server).putBucketNotification), nil},
		{"website", bucketOp("GetBucketWebsite", "s3:GetBucketWebsite", (*Server).getBucketWebsite), bucketOp("PutBucketWebsite", "s3:PutBucketWebsite", (*Server).putBucketWebsite), bucketOp("DeleteBucketWebsite", "s3:DeleteBucketWebsite", (*Server).deleteBucketWebsite)},
		{"logging", bucketOp("GetBucketLogging", "s3:GetBucketLogging", (*Server).getBucketLogging), bucketOp("PutBucketLogging", "s3:PutBucketLogging", (*Server).putBucketLogging), nil},
		{"replication", bucketOp("GetBucketReplication", "s3:GetReplicationConfiguration", (*Server).getBucketReplication), bucketOp("PutBucketReplication", "s3:PutReplicationConfiguration", (*Server).putBucketReplication), bucketOp("DeleteBucketReplication", "s3:PutReplicationConfiguration", (*Server).deleteBucketReplication)},
		{"publicAccessBlock", bucketOp("GetPublicAccessBlock", "s3:GetBucketPublicAccessBlock", (*Server).getPublicAccessBlock), bucketOp("PutPublicAccessBlock", "s3:PutBucketPublicAccessBlock", (*Server).putPublicAccessBlock), bucketOp("DeletePublicAccessBlock", "s3:PutBucketPublicAccessBlock", (*Server).deletePublicAccessBlock)},
		{"ownershipControls", bucketOp("GetBucketOwnershipControls", "s3:GetBucketOwnershipControls", (*Server).getOwnershipControls), bucketOp("PutBucketOwnershipControls", "s3:PutBucketOwnershipControls", (*Server).putOwnershipControls), bucketOp("DeleteBucketOwnershipControls", "s3:PutBucketOwnershipControls", (*Server).deleteOwnershipControls)},
		{"accelerate", bucketOp("GetBucketAccelerateConfiguration", "s3:GetAccelerateConfiguration", (*Server).getBucketAccelerate), bucketOp("PutBucketAccelerateConfiguration", "s3:PutAccelerateConfiguration", (*Server).putBucketAccelerate), nil},
		{"requestPayment", bucketOp("GetBucketRequestPayment", "s3:GetBucketRequestPayment", (*Server).getBucketRequestPayment), bucketOp("PutBucketRequestPayment", "s3:PutBucketRequestPayment", (*Server).putBucketRequestPayment), nil},
		{"metrics", bucketOp("GetBucketMetricsConfiguration", "s3:GetMetricsConfiguration", (*Server).getNamedConfig), bucketOp("PutBucketMetricsConfiguration", "s3:PutMetricsConfiguration", (*Server).putNamedConfig), bucketOp("DeleteBucketMetricsConfiguration", "s3:PutMetricsConfiguration", (*Server).deleteNamedConfig)},
		{"analytics", bucketOp("GetBucketAnalyticsConfiguration", "s3:GetAnalyticsConfiguration", (*Server).getNamedConfig), bucketOp("PutBucketAnalyticsConfiguration", "s3:PutAnalyticsConfiguration", (*Server).putNamedConfig), bucketOp("DeleteBucketAnalyticsConfiguration", "s3:PutAnalyticsConfiguration", (*Server).deleteNamedConfig)},
		{"inventory", bucketOp("GetBucketInventoryConfiguration", "s3:GetInventoryConfiguration", (*Server).getNamedConfig), bucketOp("PutBucketInventoryConfiguration", "s3:PutInventoryConfiguration", (*Server).putNamedConfig), bucketOp("DeleteBucketInventoryConfiguration", "s3:PutInventoryConfiguration", (*Server).deleteNamedConfig)},
		{"intelligent-tiering", bucketOp("GetBucketIntelligentTieringConfiguration", "s3:GetIntelligentTieringConfiguration", (*Server).getNamedConfig), bucketOp("PutBucketIntelligentTieringConfiguration", "s3:PutIntelligentTieringConfiguration", (*Server).putNamedConfig), bucketOp("DeleteBucketIntelligentTieringConfiguration", "s3:PutIntelligentTieringConfiguration", (*Server).deleteNamedConfig)},
		{"location", bucketOp("GetBucketLocation", "s3:GetBucketLocation", (*Server).getBucketLocation), nil, nil},
		{"uploads", bucketOp("ListMultipartUploads", "s3:ListBucketMultipartUploads", (*Server).listMultipartUploads), nil, nil},
		{"versions", bucketOp("ListObjectVersions", "s3:ListBucketVersions", (*Server).listObjectVersions), nil, nil},
	}
	for _, sb := range subs {
		if !has(q, sb.q) {
			continue
		}
		switch m {
		case http.MethodGet:
			if sb.get != nil {
				return sb.get
			}
		case http.MethodPut:
			if sb.put != nil {
				return sb.put
			}
		case http.MethodDelete:
			if sb.delete != nil {
				return sb.delete
			}
		}
		return &operation{name: "Unsupported", handler: func(*Server, *reqCtx) error { return errMethodNotAllowed() }, level: 1}
	}
	switch m {
	case http.MethodGet:
		if q.Get("list-type") == "2" {
			return bucketOp("ListObjectsV2", "s3:ListBucket", (*Server).listObjectsV2)
		}
		return bucketOp("ListObjects", "s3:ListBucket", (*Server).listObjectsV1)
	case http.MethodPut:
		return bucketOp("CreateBucket", "s3:CreateBucket", (*Server).createBucket)
	case http.MethodDelete:
		return bucketOp("DeleteBucket", "s3:DeleteBucket", (*Server).deleteBucket)
	case http.MethodHead:
		return bucketOp("HeadBucket", "s3:ListBucket", (*Server).headBucket)
	case http.MethodPost:
		if has(q, "delete") {
			return bucketOp("DeleteObjects", "s3:DeleteObject", (*Server).deleteObjects)
		}
		return &operation{name: "PostObject", action: "s3:PutObject", handler: (*Server).postObject, level: 1}
	}
	return nil
}

func routeObject(c *reqCtx, m string, q url.Values) *operation {
	if op := routeUnsupported(q, 2); op != nil {
		return op
	}
	uploadID := has(q, "uploadId")
	switch m {
	case http.MethodGet:
		switch {
		case has(q, "tagging"):
			o := objectOp("GetObjectTagging", "s3:GetObjectTagging", (*Server).getObjectTagging)
			if has(q, "versionId") {
				o.action = "s3:GetObjectVersionTagging"
			}
			o.needsObject = true
			return o
		case has(q, "acl"):
			o := objectOp("GetObjectAcl", "s3:GetObjectAcl", (*Server).getObjectACL)
			if has(q, "versionId") {
				o.action = "s3:GetObjectVersionAcl"
			}
			o.needsObject = true
			return o
		case has(q, "retention"):
			return objectOp("GetObjectRetention", "s3:GetObjectRetention", (*Server).getObjectRetention)
		case has(q, "legal-hold"):
			return objectOp("GetObjectLegalHold", "s3:GetObjectLegalHold", (*Server).getObjectLegalHold)
		case has(q, "attributes"):
			o := objectOp("GetObjectAttributes", "s3:GetObjectAttributes", (*Server).getObjectAttributes)
			o.needsObject = true
			return o
		case uploadID:
			return objectOp("ListParts", "s3:ListMultipartUploadParts", (*Server).listParts)
		case has(q, "torrent"):
			return objectOp("GetObjectTorrent", "s3:GetObjectTorrent", func(*Server, *reqCtx) error { return errNotImplemented("GetObjectTorrent is not supported") })
		}
		o := objectOp("GetObject", "s3:GetObject", (*Server).getObject)
		if has(q, "versionId") {
			o.action = "s3:GetObjectVersion"
		}
		o.needsObject = true
		return o
	case http.MethodHead:
		o := objectOp("HeadObject", "s3:GetObject", (*Server).headObject)
		if has(q, "versionId") {
			o.action = "s3:GetObjectVersion"
		}
		o.needsObject = true
		return o
	case http.MethodPut:
		switch {
		case has(q, "tagging"):
			o := objectOp("PutObjectTagging", "s3:PutObjectTagging", (*Server).putObjectTagging)
			if has(q, "versionId") {
				o.action = "s3:PutObjectVersionTagging"
			}
			o.needsObject = true
			return o
		case has(q, "acl"):
			o := objectOp("PutObjectAcl", "s3:PutObjectAcl", (*Server).putObjectACL)
			if has(q, "versionId") {
				o.action = "s3:PutObjectVersionAcl"
			}
			o.needsObject = true
			return o
		case has(q, "retention"):
			return objectOp("PutObjectRetention", "s3:PutObjectRetention", (*Server).putObjectRetention)
		case has(q, "legal-hold"):
			return objectOp("PutObjectLegalHold", "s3:PutObjectLegalHold", (*Server).putObjectLegalHold)
		case uploadID && has(q, "partNumber"):
			if c.r.Header.Get("x-amz-copy-source") != "" {
				return objectOp("UploadPartCopy", "s3:PutObject", (*Server).uploadPartCopy)
			}
			return objectOp("UploadPart", "s3:PutObject", (*Server).uploadPart)
		case c.r.Header.Get("x-amz-copy-source") != "":
			o := objectOp("CopyObject", "s3:PutObject", (*Server).copyObject)
			o.needsObject = true // s3:ExistingObjectTag conditions apply to overwrites
			return o
		}
		o := objectOp("PutObject", "s3:PutObject", (*Server).putObject)
		o.needsObject = true
		return o
	case http.MethodDelete:
		switch {
		case has(q, "tagging"):
			o := objectOp("DeleteObjectTagging", "s3:DeleteObjectTagging", (*Server).deleteObjectTagging)
			if has(q, "versionId") {
				o.action = "s3:DeleteObjectVersionTagging"
			}
			o.needsObject = true
			return o
		case uploadID:
			return objectOp("AbortMultipartUpload", "s3:AbortMultipartUpload", (*Server).abortMultipartUpload)
		}
		o := objectOp("DeleteObject", "s3:DeleteObject", (*Server).deleteObject)
		o.needsObject = true // s3:ExistingObjectTag conditions apply to deletes
		if has(q, "versionId") {
			o.action = "s3:DeleteObjectVersion"
		}
		return o
	case http.MethodPost:
		switch {
		case has(q, "uploads"):
			return objectOp("CreateMultipartUpload", "s3:PutObject", (*Server).createMultipartUpload)
		case uploadID:
			return objectOp("CompleteMultipartUpload", "s3:PutObject", (*Server).completeMultipartUpload)
		case has(q, "restore"):
			return objectOp("RestoreObject", "s3:RestoreObject", (*Server).restoreObject)
		case has(q, "select"):
			return objectOp("SelectObjectContent", "s3:GetObject", func(*Server, *reqCtx) error {
				return errNotImplemented("SelectObjectContent is not implemented yet")
			})
		}
	}
	return nil
}
