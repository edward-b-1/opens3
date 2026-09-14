package notify

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/edward-b-1/opens3/internal/object"
)

// Message is the JSON document delivered to a target: the AWS S3 event
// notification schema (one or more Records).
type Message struct {
	Records []Record `json:"Records"`
}

// Record is one S3 event record (eventVersion 2.1). Other packages
// (replication, audit, admin) may reuse it.
type Record struct {
	EventVersion      string            `json:"eventVersion"`
	EventSource       string            `json:"eventSource"`
	AWSRegion         string            `json:"awsRegion"`
	EventTime         string            `json:"eventTime"`
	EventName         string            `json:"eventName"`
	UserIdentity      Identity          `json:"userIdentity"`
	RequestParameters RequestParameters `json:"requestParameters"`
	ResponseElements  map[string]string `json:"responseElements"`
	S3                S3Entity          `json:"s3"`
}

// Identity is a principal reference.
type Identity struct {
	PrincipalID string `json:"principalId"`
}

// RequestParameters carries the caller's address.
type RequestParameters struct {
	SourceIPAddress string `json:"sourceIPAddress"`
}

// S3Entity is the "s3" member of a record.
type S3Entity struct {
	SchemaVersion   string       `json:"s3SchemaVersion"`
	ConfigurationID string       `json:"configurationId"`
	Bucket          BucketEntity `json:"bucket"`
	Object          ObjectEntity `json:"object"`
}

// BucketEntity describes the bucket.
type BucketEntity struct {
	Name          string   `json:"name"`
	OwnerIdentity Identity `json:"ownerIdentity"`
	ARN           string   `json:"arn"`
}

// ObjectEntity describes the object version.
type ObjectEntity struct {
	Key          string            `json:"key"`
	Size         int64             `json:"size"`
	ETag         string            `json:"eTag"`
	VersionID    string            `json:"versionId"`
	Sequencer    string            `json:"sequencer"`
	ContentType  string            `json:"contentType"`
	UserMetadata map[string]string `json:"userMetadata"`
}

// RecordOptions carry request context the object.Event does not have.
type RecordOptions struct {
	ConfigurationID string
	Region          string // fallback when the bucket record has none
	SourceIP        string
	RequestID       string // generated when empty
	HostID          string // x-amz-id-2; generated when empty
}

// BuildRecord converts an object.Event into an S3 event record.
func BuildRecord(e object.Event, opt RecordOptions) Record {
	region := opt.Region
	bucketName := ""
	owner := ""
	if e.Bucket != nil {
		bucketName = e.Bucket.Name
		owner = e.Bucket.Owner
		if e.Bucket.Region != "" {
			region = e.Bucket.Region
		}
	}
	if region == "" {
		region = "us-east-1"
	}
	t := e.Time
	if t.IsZero() {
		t = time.Now()
	}
	key := e.Key
	obj := ObjectEntity{Key: EncodeKey(key), VersionID: e.VersionID}
	if e.Object != nil {
		if key == "" {
			obj.Key = EncodeKey(e.Object.Key)
		}
		if bucketName == "" {
			bucketName = e.Object.Bucket
		}
		obj.Size = e.Object.Size
		obj.ETag = e.Object.ETag
		if obj.VersionID == "" {
			obj.VersionID = e.Object.VersionID
		}
		obj.Sequencer = fmt.Sprintf("%016X", e.Object.Seq)
		obj.ContentType = e.Object.ContentType
		obj.UserMetadata = userMetadata(e.Object.UserMeta)
	}
	if obj.UserMetadata == nil {
		obj.UserMetadata = map[string]string{}
	}
	reqID := opt.RequestID
	if reqID == "" {
		reqID = randomHex(8)
	}
	hostID := opt.HostID
	if hostID == "" {
		hostID = randomBase64(24)
	}
	return Record{
		EventVersion:      "2.1",
		EventSource:       "aws:s3",
		AWSRegion:         region,
		EventTime:         t.UTC().Format("2006-01-02T15:04:05.000Z"),
		EventName:         strings.TrimPrefix(e.Name, "s3:"),
		UserIdentity:      Identity{PrincipalID: e.Actor.CanonicalID},
		RequestParameters: RequestParameters{SourceIPAddress: opt.SourceIP},
		ResponseElements:  map[string]string{"x-amz-request-id": reqID, "x-amz-id-2": hostID},
		S3: S3Entity{
			SchemaVersion:   "1.0",
			ConfigurationID: opt.ConfigurationID,
			Bucket:          BucketEntity{Name: bucketName, OwnerIdentity: Identity{PrincipalID: owner}, ARN: "arn:aws:s3:::" + bucketName},
			Object:          obj,
		},
	}
}

// userMetadata renders user metadata the way MinIO does: header form
// ("X-Amz-Meta-Name") so consumers can reuse their MinIO parsers.
func userMetadata(m map[string]string) map[string]string {
	if len(m) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out["X-Amz-Meta-"+canonicalHeaderKey(k)] = v
	}
	return out
}

func canonicalHeaderKey(k string) string {
	parts := strings.Split(k, "-")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
		}
	}
	return strings.Join(parts, "-")
}

// EncodeKey URL-encodes an object key the way S3 does in event records:
// query-string escaping (space becomes "+") with "/" left intact.
func EncodeKey(key string) string {
	return strings.ReplaceAll(url.QueryEscape(key), "%2F", "/")
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}

func randomBase64(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}
