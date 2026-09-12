// Command apicoverage generates docs/API-COVERAGE.md: the list of Amazon S3
// data-plane operations with whether OpenS3 routes them. Run with
// `go run ./tools/apicoverage`.
package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// awsOps is the Amazon S3 (2006-03-01) operation list as of September 2026
// (docs/SURVEY.md section 3, 1.5), grouped for the report.
var awsOps = map[string][]string{
	"Objects": {"PutObject", "GetObject", "HeadObject", "DeleteObject", "DeleteObjects", "CopyObject", "RenameObject", "RestoreObject", "SelectObjectContent",
		"GetObjectAttributes", "GetObjectTorrent", "UpdateObjectEncryption", "WriteGetObjectResponse", "PostObject"},
	"Multipart": {"CreateMultipartUpload", "UploadPart", "UploadPartCopy", "CompleteMultipartUpload", "AbortMultipartUpload", "ListMultipartUploads", "ListParts"},
	"Listing":   {"ListObjects", "ListObjectsV2", "ListObjectVersions", "ListBuckets", "ListDirectoryBuckets"},
	"Object metadata, ACL, tags, lock": {"GetObjectAcl", "PutObjectAcl", "GetObjectTagging", "PutObjectTagging", "DeleteObjectTagging", "GetObjectLegalHold", "PutObjectLegalHold",
		"GetObjectRetention", "PutObjectRetention", "GetObjectLockConfiguration", "PutObjectLockConfiguration"},
	"Annotations (2026)": {"PutObjectAnnotation", "GetObjectAnnotation", "ListObjectAnnotations", "DeleteObjectAnnotation"},
	"Buckets":            {"CreateBucket", "DeleteBucket", "HeadBucket", "GetBucketLocation", "CreateSession"},
	"Bucket access and security": {"GetBucketAcl", "PutBucketAcl", "GetBucketPolicy", "PutBucketPolicy", "DeleteBucketPolicy", "GetBucketPolicyStatus", "GetPublicAccessBlock",
		"PutPublicAccessBlock", "DeletePublicAccessBlock", "GetBucketOwnershipControls", "PutBucketOwnershipControls", "DeleteBucketOwnershipControls", "GetBucketAbac", "PutBucketAbac",
		"GetBucketEncryption", "PutBucketEncryption", "DeleteBucketEncryption"},
	"Bucket configuration": {"GetBucketVersioning", "PutBucketVersioning", "GetBucketLifecycle", "PutBucketLifecycle", "GetBucketLifecycleConfiguration", "PutBucketLifecycleConfiguration",
		"DeleteBucketLifecycle", "GetBucketReplication", "PutBucketReplication", "DeleteBucketReplication", "GetBucketCors", "PutBucketCors", "DeleteBucketCors", "GetBucketWebsite",
		"PutBucketWebsite", "DeleteBucketWebsite", "GetBucketLogging", "PutBucketLogging", "GetBucketNotification", "PutBucketNotification", "GetBucketNotificationConfiguration",
		"PutBucketNotificationConfiguration", "GetBucketRequestPayment", "PutBucketRequestPayment", "GetBucketTagging", "PutBucketTagging", "DeleteBucketTagging",
		"GetBucketAccelerateConfiguration", "PutBucketAccelerateConfiguration"},
	"Analytics, metrics, inventory, tiering": {"GetBucketAnalyticsConfiguration", "PutBucketAnalyticsConfiguration", "DeleteBucketAnalyticsConfiguration", "ListBucketAnalyticsConfigurations",
		"GetBucketMetricsConfiguration", "PutBucketMetricsConfiguration", "DeleteBucketMetricsConfiguration", "ListBucketMetricsConfigurations", "GetBucketInventoryConfiguration",
		"PutBucketInventoryConfiguration", "DeleteBucketInventoryConfiguration", "ListBucketInventoryConfigurations", "GetBucketIntelligentTieringConfiguration",
		"PutBucketIntelligentTieringConfiguration", "DeleteBucketIntelligentTieringConfiguration", "ListBucketIntelligentTieringConfigurations"},
	"S3 Metadata": {"CreateBucketMetadataTableConfiguration", "GetBucketMetadataTableConfiguration", "DeleteBucketMetadataTableConfiguration", "CreateBucketMetadataConfiguration",
		"GetBucketMetadataConfiguration", "DeleteBucketMetadataConfiguration", "UpdateBucketMetadataJournalTableConfiguration", "UpdateBucketMetadataInventoryTableConfiguration",
		"UpdateBucketMetadataAnnotationTableConfiguration"},
}

// aliases maps AWS names to the route names used in router.go where they differ.
var aliases = map[string]string{
	"GetBucketLifecycle": "GetBucketLifecycleConfiguration", "PutBucketLifecycle": "PutBucketLifecycleConfiguration",
	"GetBucketNotification": "GetBucketNotificationConfiguration", "PutBucketNotification": "PutBucketNotificationConfiguration",
	"ListBucketAnalyticsConfigurations": "GetBucketAnalyticsConfiguration", "ListBucketMetricsConfigurations": "GetBucketMetricsConfiguration",
	"ListBucketInventoryConfigurations": "GetBucketInventoryConfiguration", "ListBucketIntelligentTieringConfigurations": "GetBucketIntelligentTieringConfiguration",
}

// notes explain partial or stub implementations.
var notes = map[string]string{
	"SelectObjectContent":                      "returns NotImplemented; S3 Select is closed to new AWS customers since 2024 (phase 3 at most)",
	"GetObjectTorrent":                         "returns NotImplemented; BitTorrent delivery is retired on AWS",
	"RestoreObject":                            "accepted for archive storage classes; data is never actually archived, so restore completes immediately",
	"GetBucketLifecycle":                       "legacy name; served by the *Configuration handler",
	"PutBucketLifecycle":                       "legacy name; served by the *Configuration handler",
	"GetBucketNotification":                    "legacy name; served by the *Configuration handler",
	"PutBucketNotification":                    "legacy name; served by the *Configuration handler",
	"GetBucketAccelerateConfiguration":         "stored and returned; no acceleration semantics",
	"PutBucketAccelerateConfiguration":         "stored and returned; no acceleration semantics",
	"GetBucketRequestPayment":                  "stored and returned; requester-pays billing is not applicable",
	"PutBucketRequestPayment":                  "stored and returned; requester-pays billing is not applicable",
	"GetBucketLogging":                         "stored and returned; log delivery to a bucket is phase 2",
	"PutBucketLogging":                         "stored and returned; log delivery to a bucket is phase 2",
	"GetBucketReplication":                     "stored and validated; the replication worker is phase 2",
	"PutBucketReplication":                     "stored and validated; the replication worker is phase 2",
	"GetBucketWebsite":                         "stored and returned; website endpoint serving is phase 2",
	"PutBucketWebsite":                         "stored and returned; website endpoint serving is phase 2",
	"GetBucketInventoryConfiguration":          "stored and returned; report generation is phase 2",
	"PutBucketInventoryConfiguration":          "stored and returned; report generation is phase 2",
	"GetBucketAnalyticsConfiguration":          "stored and returned",
	"PutBucketAnalyticsConfiguration":          "stored and returned",
	"GetBucketMetricsConfiguration":            "stored and returned",
	"PutBucketMetricsConfiguration":            "stored and returned",
	"GetBucketIntelligentTieringConfiguration": "stored and returned; no tiering semantics",
	"PutBucketIntelligentTieringConfiguration": "stored and returned; no tiering semantics",
}

func main() {
	src, err := os.ReadFile("internal/s3api/router.go")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	re := regexp.MustCompile(`(?:bucketOp|objectOp|name:)\s*\(?"([A-Za-z0-9]+)"`)
	routed := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		routed[m[1]] = true
	}
	var b strings.Builder
	b.WriteString("# S3 API coverage\n\nGenerated by `go run ./tools/apicoverage` from the router table. Status: **yes** = routed and implemented, **partial** = accepted with the noted limitation, **no** = returns NotImplemented or is not routed. Behavioural conformance is measured separately in `CONFORMANCE.md`.\n\n")
	groups := make([]string, 0, len(awsOps))
	for g := range awsOps {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	total, yes, partial := 0, 0, 0
	for _, g := range groups {
		b.WriteString("## " + g + "\n\n| Operation | Status | Notes |\n|---|---|---|\n")
		for _, op := range awsOps[g] {
			total++
			name := op
			if a, ok := aliases[op]; ok {
				name = a
			}
			status := "no"
			if routed[name] {
				status = "yes"
				if n := notes[op]; strings.Contains(n, "NotImplemented") {
					status = "no"
				} else if n != "" {
					status = "partial"
				}
			}
			switch status {
			case "yes":
				yes++
			case "partial":
				partial++
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", op, status, notes[op])
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "**Summary:** %d of %d operations implemented (%d fully, %d with noted limitations).\n", yes+partial, total, yes, partial)
	if err := os.WriteFile("docs/API-COVERAGE.md", []byte(b.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%d/%d operations (%d full, %d partial)\n", yes+partial, total, yes, partial)
}
