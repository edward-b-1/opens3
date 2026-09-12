package s3api

import (
	"encoding/xml"
	"net/url"
	"sort"
	"time"

	"gitlab.com/Birdsall/opens3/internal/meta"
)

const s3NS = "http://s3.amazonaws.com/doc/2006-03-01/"

func unescapeQuery(s string) (string, error) { return url.QueryUnescape(s) }

func iso8601(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// --- common --------------------------------------------------------------

type xmlOwner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName,omitempty"`
}

type xmlTag struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value"`
}

type xmlTagging struct {
	XMLName xml.Name `xml:"Tagging"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	TagSet  struct {
		Tags []xmlTag `xml:"Tag"`
	} `xml:"TagSet"`
}

func tagsToXML(tags []meta.Tag) *xmlTagging {
	t := &xmlTagging{Xmlns: s3NS}
	for _, tg := range tags {
		t.TagSet.Tags = append(t.TagSet.Tags, xmlTag{tg.Key, tg.Value})
	}
	sort.Slice(t.TagSet.Tags, func(i, j int) bool { return t.TagSet.Tags[i].Key < t.TagSet.Tags[j].Key })
	if t.TagSet.Tags == nil {
		t.TagSet.Tags = []xmlTag{}
	}
	return t
}

func tagsFromXML(t *xmlTagging) []meta.Tag {
	var out []meta.Tag
	for _, tg := range t.TagSet.Tags {
		out = append(out, meta.Tag{Key: tg.Key, Value: tg.Value})
	}
	return out
}

// --- buckets -------------------------------------------------------------

type xmlListAllMyBucketsResult struct {
	XMLName xml.Name `xml:"ListAllMyBucketsResult"`
	Xmlns   string   `xml:"xmlns,attr"`
	Owner   xmlOwner `xml:"Owner"`
	Buckets struct {
		Bucket []xmlBucketEntry `xml:"Bucket"`
	} `xml:"Buckets"`
	ContinuationToken string `xml:"ContinuationToken,omitempty"`
	Prefix            string `xml:"Prefix,omitempty"`
}

type xmlBucketEntry struct {
	Name         string `xml:"Name"`
	CreationDate string `xml:"CreationDate"`
	BucketRegion string `xml:"BucketRegion,omitempty"`
}

type xmlCreateBucketConfiguration struct {
	XMLName            xml.Name `xml:"CreateBucketConfiguration"`
	LocationConstraint string   `xml:"LocationConstraint"`
	Location           *struct {
		Type string `xml:"Type"`
		Name string `xml:"Name"`
	} `xml:"Location"`
	Bucket *struct {
		DataRedundancy string `xml:"DataRedundancy"`
		Type           string `xml:"Type"`
	} `xml:"Bucket"`
}

type xmlLocationConstraint struct {
	XMLName  xml.Name `xml:"LocationConstraint"`
	Xmlns    string   `xml:"xmlns,attr"`
	Location string   `xml:",chardata"`
}

type xmlVersioningConfiguration struct {
	XMLName   xml.Name `xml:"VersioningConfiguration"`
	Xmlns     string   `xml:"xmlns,attr,omitempty"`
	Status    string   `xml:"Status,omitempty"`
	MfaDelete string   `xml:"MfaDelete,omitempty"`
}

type xmlPolicyStatus struct {
	XMLName  xml.Name `xml:"PolicyStatus"`
	Xmlns    string   `xml:"xmlns,attr"`
	IsPublic bool     `xml:"IsPublic"`
}

type xmlPublicAccessBlock struct {
	XMLName               xml.Name `xml:"PublicAccessBlockConfiguration"`
	Xmlns                 string   `xml:"xmlns,attr,omitempty"`
	BlockPublicAcls       bool     `xml:"BlockPublicAcls"`
	IgnorePublicAcls      bool     `xml:"IgnorePublicAcls"`
	BlockPublicPolicy     bool     `xml:"BlockPublicPolicy"`
	RestrictPublicBuckets bool     `xml:"RestrictPublicBuckets"`
}

type xmlOwnershipControls struct {
	XMLName xml.Name `xml:"OwnershipControls"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	Rules   []struct {
		ObjectOwnership string `xml:"ObjectOwnership"`
	} `xml:"Rule"`
}

type xmlServerSideEncryptionConfiguration struct {
	XMLName xml.Name `xml:"ServerSideEncryptionConfiguration"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	Rules   []struct {
		ApplyServerSideEncryptionByDefault *struct {
			SSEAlgorithm   string `xml:"SSEAlgorithm"`
			KMSMasterKeyID string `xml:"KMSMasterKeyID,omitempty"`
		} `xml:"ApplyServerSideEncryptionByDefault"`
		BucketKeyEnabled *bool `xml:"BucketKeyEnabled"`
	} `xml:"Rule"`
}

type xmlObjectLockConfiguration struct {
	XMLName           xml.Name `xml:"ObjectLockConfiguration"`
	Xmlns             string   `xml:"xmlns,attr,omitempty"`
	ObjectLockEnabled string   `xml:"ObjectLockEnabled,omitempty"`
	Rule              *struct {
		DefaultRetention struct {
			Mode  string `xml:"Mode"`
			Days  int    `xml:"Days,omitempty"`
			Years int    `xml:"Years,omitempty"`
		} `xml:"DefaultRetention"`
	} `xml:"Rule"`
}

type xmlAccessControlPolicy struct {
	XMLName           xml.Name `xml:"AccessControlPolicy"`
	Xmlns             string   `xml:"xmlns,attr,omitempty"`
	Owner             xmlOwner `xml:"Owner"`
	AccessControlList struct {
		Grants []xmlGrant `xml:"Grant"`
	} `xml:"AccessControlList"`
}

type xmlGrant struct {
	Grantee    xmlGrantee `xml:"Grantee"`
	Permission string     `xml:"Permission"`
}

type xmlGrantee struct {
	XMLName      xml.Name `xml:"Grantee"`
	XsiNS        string   `xml:"xmlns:xsi,attr,omitempty"`
	Type         string   `xml:"xsi:type,attr"`
	ID           string   `xml:"ID,omitempty"`
	DisplayName  string   `xml:"DisplayName,omitempty"`
	URI          string   `xml:"URI,omitempty"`
	EmailAddress string   `xml:"EmailAddress,omitempty"`
}

// xmlGranteeIn is the lenient decoding form (xsi:type attribute namespace).
type xmlAccessControlPolicyIn struct {
	XMLName           xml.Name `xml:"AccessControlPolicy"`
	Owner             xmlOwner `xml:"Owner"`
	AccessControlList struct {
		Grants []struct {
			Grantee struct {
				Type         string `xml:"type,attr"`
				ID           string `xml:"ID"`
				DisplayName  string `xml:"DisplayName"`
				URI          string `xml:"URI"`
				EmailAddress string `xml:"EmailAddress"`
			} `xml:"Grantee"`
			Permission string `xml:"Permission"`
		} `xml:"Grant"`
	} `xml:"AccessControlList"`
}

func aclToXML(a *meta.ACL) *xmlAccessControlPolicy {
	out := &xmlAccessControlPolicy{Xmlns: s3NS, Owner: xmlOwner{ID: a.Owner, DisplayName: a.OwnerDisplay}}
	// Group grants are listed before user grants (the order S3 clients
	// and the Ceph conformance suite expect).
	grants := make([]meta.Grant, 0, len(a.Grants))
	for _, g := range a.Grants {
		if g.GranteeType == "Group" {
			grants = append(grants, g)
		}
	}
	for _, g := range a.Grants {
		if g.GranteeType != "Group" {
			grants = append(grants, g)
		}
	}
	for _, g := range grants {
		ge := xmlGrantee{XsiNS: "http://www.w3.org/2001/XMLSchema-instance", Type: g.GranteeType}
		if g.GranteeType == "Group" {
			ge.URI = g.Grantee
		} else {
			ge.ID = g.Grantee
			ge.DisplayName = g.DisplayName
		}
		out.AccessControlList.Grants = append(out.AccessControlList.Grants, xmlGrant{Grantee: ge, Permission: g.Permission})
	}
	if out.AccessControlList.Grants == nil {
		out.AccessControlList.Grants = []xmlGrant{}
	}
	return out
}

// --- listing -------------------------------------------------------------

type xmlListBucketResult struct {
	XMLName               xml.Name          `xml:"ListBucketResult"`
	Xmlns                 string            `xml:"xmlns,attr"`
	Name                  string            `xml:"Name"`
	Prefix                string            `xml:"Prefix"`
	Marker                *string           `xml:"Marker"`
	NextMarker            string            `xml:"NextMarker,omitempty"`
	StartAfter            string            `xml:"StartAfter,omitempty"`
	ContinuationToken     *string           `xml:"ContinuationToken"`
	NextContinuationToken string            `xml:"NextContinuationToken,omitempty"`
	KeyCount              *int              `xml:"KeyCount"`
	MaxKeys               int               `xml:"MaxKeys"`
	Delimiter             string            `xml:"Delimiter,omitempty"`
	EncodingType          string            `xml:"EncodingType,omitempty"`
	IsTruncated           bool              `xml:"IsTruncated"`
	Contents              []xmlObjectEntry  `xml:"Contents"`
	CommonPrefixes        []xmlCommonPrefix `xml:"CommonPrefixes"`
}

type xmlObjectEntry struct {
	Key               string    `xml:"Key"`
	LastModified      string    `xml:"LastModified"`
	ETag              string    `xml:"ETag"`
	ChecksumAlgorithm string    `xml:"ChecksumAlgorithm,omitempty"`
	ChecksumType      string    `xml:"ChecksumType,omitempty"`
	Size              int64     `xml:"Size"`
	Owner             *xmlOwner `xml:"Owner,omitempty"`
	StorageClass      string    `xml:"StorageClass"`
	RestoreStatus     *struct {
		IsRestoreInProgress bool   `xml:"IsRestoreInProgress"`
		RestoreExpiryDate   string `xml:"RestoreExpiryDate,omitempty"`
	} `xml:"RestoreStatus,omitempty"`
}

type xmlCommonPrefix struct {
	Prefix string `xml:"Prefix"`
}

type xmlListVersionsResult struct {
	XMLName             xml.Name `xml:"ListVersionsResult"`
	Xmlns               string   `xml:"xmlns,attr"`
	Name                string   `xml:"Name"`
	Prefix              string   `xml:"Prefix"`
	KeyMarker           string   `xml:"KeyMarker"`
	VersionIdMarker     string   `xml:"VersionIdMarker"`
	NextKeyMarker       string   `xml:"NextKeyMarker,omitempty"`
	NextVersionIdMarker string   `xml:"NextVersionIdMarker,omitempty"`
	MaxKeys             int      `xml:"MaxKeys"`
	Delimiter           string   `xml:"Delimiter,omitempty"`
	EncodingType        string   `xml:"EncodingType,omitempty"`
	IsTruncated         bool     `xml:"IsTruncated"`
	Entries             []any    `xml:",any"`
}

type xmlVersionEntry struct {
	XMLName           xml.Name `xml:"Version"`
	Key               string   `xml:"Key"`
	VersionId         string   `xml:"VersionId"`
	IsLatest          bool     `xml:"IsLatest"`
	LastModified      string   `xml:"LastModified"`
	ETag              string   `xml:"ETag"`
	ChecksumAlgorithm string   `xml:"ChecksumAlgorithm,omitempty"`
	ChecksumType      string   `xml:"ChecksumType,omitempty"`
	Size              int64    `xml:"Size"`
	Owner             xmlOwner `xml:"Owner"`
	StorageClass      string   `xml:"StorageClass"`
}

type xmlDeleteMarkerEntry struct {
	XMLName      xml.Name `xml:"DeleteMarker"`
	Key          string   `xml:"Key"`
	VersionId    string   `xml:"VersionId"`
	IsLatest     bool     `xml:"IsLatest"`
	LastModified string   `xml:"LastModified"`
	Owner        xmlOwner `xml:"Owner"`
}

type xmlCommonPrefixEntry struct {
	XMLName xml.Name `xml:"CommonPrefixes"`
	Prefix  string   `xml:"Prefix"`
}

// --- delete objects ------------------------------------------------------

type xmlDelete struct {
	XMLName xml.Name `xml:"Delete"`
	Quiet   bool     `xml:"Quiet"`
	Objects []struct {
		Key              string `xml:"Key"`
		VersionId        string `xml:"VersionId"`
		ETag             string `xml:"ETag"`
		Size             *int64 `xml:"Size"`
		LastModifiedTime string `xml:"LastModifiedTime"`
	} `xml:"Object"`
}

type xmlDeleteResult struct {
	XMLName xml.Name           `xml:"DeleteResult"`
	Xmlns   string             `xml:"xmlns,attr"`
	Deleted []xmlDeletedObject `xml:"Deleted"`
	Errors  []xmlDeleteError   `xml:"Error"`
}

type xmlDeletedObject struct {
	Key                   string `xml:"Key"`
	VersionId             string `xml:"VersionId,omitempty"`
	DeleteMarker          bool   `xml:"DeleteMarker,omitempty"`
	DeleteMarkerVersionId string `xml:"DeleteMarkerVersionId,omitempty"`
}

type xmlDeleteError struct {
	Key       string `xml:"Key"`
	VersionId string `xml:"VersionId,omitempty"`
	Code      string `xml:"Code"`
	Message   string `xml:"Message"`
}

// --- multipart -----------------------------------------------------------

type xmlInitiateMultipartUploadResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Xmlns    string   `xml:"xmlns,attr"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadId string   `xml:"UploadId"`
}

type xmlCompleteMultipartUpload struct {
	XMLName xml.Name `xml:"CompleteMultipartUpload"`
	Parts   []struct {
		PartNumber        int    `xml:"PartNumber"`
		ETag              string `xml:"ETag"`
		ChecksumCRC32     string `xml:"ChecksumCRC32"`
		ChecksumCRC32C    string `xml:"ChecksumCRC32C"`
		ChecksumSHA1      string `xml:"ChecksumSHA1"`
		ChecksumSHA256    string `xml:"ChecksumSHA256"`
		ChecksumCRC64NVME string `xml:"ChecksumCRC64NVME"`
	} `xml:"Part"`
}

type xmlCompleteMultipartUploadResult struct {
	XMLName           xml.Name `xml:"CompleteMultipartUploadResult"`
	Xmlns             string   `xml:"xmlns,attr"`
	Location          string   `xml:"Location"`
	Bucket            string   `xml:"Bucket"`
	Key               string   `xml:"Key"`
	ETag              string   `xml:"ETag"`
	ChecksumCRC32     string   `xml:"ChecksumCRC32,omitempty"`
	ChecksumCRC32C    string   `xml:"ChecksumCRC32C,omitempty"`
	ChecksumSHA1      string   `xml:"ChecksumSHA1,omitempty"`
	ChecksumSHA256    string   `xml:"ChecksumSHA256,omitempty"`
	ChecksumCRC64NVME string   `xml:"ChecksumCRC64NVME,omitempty"`
	ChecksumType      string   `xml:"ChecksumType,omitempty"`
}

type xmlListPartsResult struct {
	XMLName              xml.Name  `xml:"ListPartsResult"`
	Xmlns                string    `xml:"xmlns,attr"`
	Bucket               string    `xml:"Bucket"`
	Key                  string    `xml:"Key"`
	UploadId             string    `xml:"UploadId"`
	PartNumberMarker     int       `xml:"PartNumberMarker"`
	NextPartNumberMarker int       `xml:"NextPartNumberMarker"`
	MaxParts             int       `xml:"MaxParts"`
	IsTruncated          bool      `xml:"IsTruncated"`
	Parts                []xmlPart `xml:"Part"`
	Initiator            xmlOwner  `xml:"Initiator"`
	Owner                xmlOwner  `xml:"Owner"`
	StorageClass         string    `xml:"StorageClass"`
	ChecksumAlgorithm    string    `xml:"ChecksumAlgorithm,omitempty"`
	ChecksumType         string    `xml:"ChecksumType,omitempty"`
}

type xmlPart struct {
	PartNumber        int    `xml:"PartNumber"`
	LastModified      string `xml:"LastModified,omitempty"`
	ETag              string `xml:"ETag"`
	Size              int64  `xml:"Size"`
	ChecksumCRC32     string `xml:"ChecksumCRC32,omitempty"`
	ChecksumCRC32C    string `xml:"ChecksumCRC32C,omitempty"`
	ChecksumSHA1      string `xml:"ChecksumSHA1,omitempty"`
	ChecksumSHA256    string `xml:"ChecksumSHA256,omitempty"`
	ChecksumCRC64NVME string `xml:"ChecksumCRC64NVME,omitempty"`
}

type xmlListMultipartUploadsResult struct {
	XMLName            xml.Name          `xml:"ListMultipartUploadsResult"`
	Xmlns              string            `xml:"xmlns,attr"`
	Bucket             string            `xml:"Bucket"`
	KeyMarker          string            `xml:"KeyMarker"`
	UploadIdMarker     string            `xml:"UploadIdMarker"`
	NextKeyMarker      string            `xml:"NextKeyMarker,omitempty"`
	NextUploadIdMarker string            `xml:"NextUploadIdMarker,omitempty"`
	Prefix             string            `xml:"Prefix"`
	Delimiter          string            `xml:"Delimiter,omitempty"`
	MaxUploads         int               `xml:"MaxUploads"`
	IsTruncated        bool              `xml:"IsTruncated"`
	EncodingType       string            `xml:"EncodingType,omitempty"`
	Uploads            []xmlUpload       `xml:"Upload"`
	CommonPrefixes     []xmlCommonPrefix `xml:"CommonPrefixes"`
}

type xmlUpload struct {
	Key               string   `xml:"Key"`
	UploadId          string   `xml:"UploadId"`
	Initiator         xmlOwner `xml:"Initiator"`
	Owner             xmlOwner `xml:"Owner"`
	StorageClass      string   `xml:"StorageClass"`
	Initiated         string   `xml:"Initiated"`
	ChecksumAlgorithm string   `xml:"ChecksumAlgorithm,omitempty"`
	ChecksumType      string   `xml:"ChecksumType,omitempty"`
}

// --- copy ----------------------------------------------------------------

type xmlCopyObjectResult struct {
	XMLName           xml.Name `xml:"CopyObjectResult"`
	Xmlns             string   `xml:"xmlns,attr"`
	ETag              string   `xml:"ETag"`
	LastModified      string   `xml:"LastModified"`
	ChecksumType      string   `xml:"ChecksumType,omitempty"`
	ChecksumCRC32     string   `xml:"ChecksumCRC32,omitempty"`
	ChecksumCRC32C    string   `xml:"ChecksumCRC32C,omitempty"`
	ChecksumSHA1      string   `xml:"ChecksumSHA1,omitempty"`
	ChecksumSHA256    string   `xml:"ChecksumSHA256,omitempty"`
	ChecksumCRC64NVME string   `xml:"ChecksumCRC64NVME,omitempty"`
}

type xmlCopyPartResult struct {
	XMLName           xml.Name `xml:"CopyPartResult"`
	Xmlns             string   `xml:"xmlns,attr"`
	ETag              string   `xml:"ETag"`
	LastModified      string   `xml:"LastModified"`
	ChecksumCRC32     string   `xml:"ChecksumCRC32,omitempty"`
	ChecksumCRC32C    string   `xml:"ChecksumCRC32C,omitempty"`
	ChecksumSHA1      string   `xml:"ChecksumSHA1,omitempty"`
	ChecksumSHA256    string   `xml:"ChecksumSHA256,omitempty"`
	ChecksumCRC64NVME string   `xml:"ChecksumCRC64NVME,omitempty"`
}

// --- object lock ---------------------------------------------------------

type xmlRetention struct {
	XMLName         xml.Name `xml:"Retention"`
	Xmlns           string   `xml:"xmlns,attr,omitempty"`
	Mode            string   `xml:"Mode"`
	RetainUntilDate string   `xml:"RetainUntilDate"`
}

type xmlLegalHold struct {
	XMLName xml.Name `xml:"LegalHold"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	Status  string   `xml:"Status"`
}

// --- attributes ----------------------------------------------------------

type xmlGetObjectAttributesResponse struct {
	XMLName  xml.Name `xml:"GetObjectAttributesResponse"`
	Xmlns    string   `xml:"xmlns,attr"`
	ETag     string   `xml:"ETag,omitempty"`
	Checksum *struct {
		ChecksumCRC32     string `xml:"ChecksumCRC32,omitempty"`
		ChecksumCRC32C    string `xml:"ChecksumCRC32C,omitempty"`
		ChecksumSHA1      string `xml:"ChecksumSHA1,omitempty"`
		ChecksumSHA256    string `xml:"ChecksumSHA256,omitempty"`
		ChecksumCRC64NVME string `xml:"ChecksumCRC64NVME,omitempty"`
		ChecksumType      string `xml:"ChecksumType,omitempty"`
	} `xml:"Checksum,omitempty"`
	ObjectParts *struct {
		IsTruncated          bool `xml:"IsTruncated"`
		MaxParts             int  `xml:"MaxParts"`
		NextPartNumberMarker int  `xml:"NextPartNumberMarker,omitempty"`
		PartNumberMarker     int  `xml:"PartNumberMarker"`
		Parts                []struct {
			ChecksumCRC32     string `xml:"ChecksumCRC32,omitempty"`
			ChecksumCRC32C    string `xml:"ChecksumCRC32C,omitempty"`
			ChecksumSHA1      string `xml:"ChecksumSHA1,omitempty"`
			ChecksumSHA256    string `xml:"ChecksumSHA256,omitempty"`
			ChecksumCRC64NVME string `xml:"ChecksumCRC64NVME,omitempty"`
			PartNumber        int    `xml:"PartNumber"`
			Size              int64  `xml:"Size"`
		} `xml:"Part"`
		TotalPartsCount int `xml:"PartsCount"`
	} `xml:"ObjectParts,omitempty"`
	StorageClass string `xml:"StorageClass,omitempty"`
	ObjectSize   *int64 `xml:"ObjectSize,omitempty"`
}

// --- CORS ----------------------------------------------------------------

type xmlCORSConfiguration struct {
	XMLName xml.Name      `xml:"CORSConfiguration"`
	Xmlns   string        `xml:"xmlns,attr,omitempty"`
	Rules   []xmlCORSRule `xml:"CORSRule"`
}

type xmlCORSRule struct {
	ID             string   `xml:"ID,omitempty"`
	AllowedOrigins []string `xml:"AllowedOrigin"`
	AllowedMethods []string `xml:"AllowedMethod"`
	AllowedHeaders []string `xml:"AllowedHeader,omitempty"`
	ExposeHeaders  []string `xml:"ExposeHeader,omitempty"`
	MaxAgeSeconds  int      `xml:"MaxAgeSeconds,omitempty"`
}

// --- notification --------------------------------------------------------

type xmlNotificationConfiguration struct {
	XMLName     xml.Name                `xml:"NotificationConfiguration"`
	Xmlns       string                  `xml:"xmlns,attr,omitempty"`
	Topics      []xmlNotificationTarget `xml:"TopicConfiguration"`
	Queues      []xmlNotificationTarget `xml:"QueueConfiguration"`
	Lambdas     []xmlNotificationTarget `xml:"CloudFunctionConfiguration"`
	EventBridge *struct{}               `xml:"EventBridgeConfiguration"`
}

type xmlNotificationTarget struct {
	ID     string   `xml:"Id,omitempty"`
	Topic  string   `xml:"Topic,omitempty"`
	Queue  string   `xml:"Queue,omitempty"`
	Lambda string   `xml:"CloudFunction,omitempty"`
	Events []string `xml:"Event"`
	Filter *struct {
		S3Key struct {
			Rules []struct {
				Name  string `xml:"Name"`
				Value string `xml:"Value"`
			} `xml:"FilterRule"`
		} `xml:"S3Key"`
	} `xml:"Filter"`
}

// --- website -------------------------------------------------------------

type xmlWebsiteConfiguration struct {
	XMLName       xml.Name `xml:"WebsiteConfiguration"`
	Xmlns         string   `xml:"xmlns,attr,omitempty"`
	IndexDocument *struct {
		Suffix string `xml:"Suffix"`
	} `xml:"IndexDocument"`
	ErrorDocument *struct {
		Key string `xml:"Key"`
	} `xml:"ErrorDocument"`
	RedirectAllRequestsTo *struct {
		HostName string `xml:"HostName"`
		Protocol string `xml:"Protocol,omitempty"`
	} `xml:"RedirectAllRequestsTo"`
	RoutingRules *struct {
		Rules []struct {
			Condition *struct {
				KeyPrefixEquals             string `xml:"KeyPrefixEquals,omitempty"`
				HttpErrorCodeReturnedEquals string `xml:"HttpErrorCodeReturnedEquals,omitempty"`
			} `xml:"Condition"`
			Redirect struct {
				HostName             string `xml:"HostName,omitempty"`
				Protocol             string `xml:"Protocol,omitempty"`
				ReplaceKeyPrefixWith string `xml:"ReplaceKeyPrefixWith,omitempty"`
				ReplaceKeyWith       string `xml:"ReplaceKeyWith,omitempty"`
				HttpRedirectCode     string `xml:"HttpRedirectCode,omitempty"`
			} `xml:"Redirect"`
		} `xml:"RoutingRule"`
	} `xml:"RoutingRules"`
}

// --- STS -----------------------------------------------------------------

type xmlAssumeRoleResponse struct {
	XMLName xml.Name `xml:"AssumeRoleResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Result  struct {
		Credentials struct {
			AccessKeyId     string `xml:"AccessKeyId"`
			SecretAccessKey string `xml:"SecretAccessKey"`
			SessionToken    string `xml:"SessionToken"`
			Expiration      string `xml:"Expiration"`
		} `xml:"Credentials"`
		AssumedRoleUser struct {
			Arn           string `xml:"Arn"`
			AssumedRoleId string `xml:"AssumedRoleId"`
		} `xml:"AssumedRoleUser"`
	} `xml:"AssumeRoleResult"`
	ResponseMetadata struct {
		RequestId string `xml:"RequestId"`
	} `xml:"ResponseMetadata"`
}

type xmlSTSError struct {
	XMLName xml.Name `xml:"ErrorResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Error   struct {
		Type    string `xml:"Type"`
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
	RequestId string `xml:"RequestId"`
}

// --- POST policy -----------------------------------------------------------

type xmlPostResponse struct {
	XMLName  xml.Name `xml:"PostResponse"`
	Location string   `xml:"Location"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
}

// --- restore ---------------------------------------------------------------

type xmlRestoreRequest struct {
	XMLName              xml.Name `xml:"RestoreRequest"`
	Days                 int      `xml:"Days"`
	Type                 string   `xml:"Type,omitempty"`
	Tier                 string   `xml:"Tier,omitempty"`
	GlacierJobParameters *struct {
		Tier string `xml:"Tier"`
	} `xml:"GlacierJobParameters"`
}
