// Package s3err defines the S3 error codes, their HTTP status and default
// messages, matching Amazon S3's documented error responses.
package s3err

import (
	"fmt"
	"net/http"
)

// Code identifies an S3 error.
type Code string

// Error is an S3 API error. It carries the code, HTTP status and a message.
// Optional fields (Resource, BucketName, Key, extra elements) are rendered
// into the XML error body by the API layer.
type Error struct {
	Code       Code
	Status     int
	Message    string
	Resource   string
	BucketName string
	Key        string
	// Extra holds additional XML elements (e.g. Condition, Region) as
	// element-name → text.
	Extra map[string]string
	// Headers are added to the HTTP response (e.g. x-amz-bucket-region).
	Headers map[string]string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// Is allows errors.Is(err, s3err.New(Code...)) comparisons on Code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// WithMessage returns a copy of the error with a different message.
func (e *Error) WithMessage(format string, args ...any) *Error {
	c := *e
	c.Message = fmt.Sprintf(format, args...)
	return &c
}

// WithResource returns a copy with Resource set.
func (e *Error) WithResource(res string) *Error {
	c := *e
	c.Resource = res
	return &c
}

// WithExtra returns a copy with an extra XML element.
func (e *Error) WithExtra(k, v string) *Error {
	c := *e
	c.Extra = map[string]string{}
	for kk, vv := range e.Extra {
		c.Extra[kk] = vv
	}
	c.Extra[k] = v
	return &c
}

// WithHeader returns a copy with an extra response header.
func (e *Error) WithHeader(k, v string) *Error {
	c := *e
	c.Headers = map[string]string{}
	for kk, vv := range e.Headers {
		c.Headers[kk] = vv
	}
	c.Headers[k] = v
	return &c
}

type def struct {
	status int
	msg    string
}

var table = map[Code]def{
	AccessDenied:                            {http.StatusForbidden, "Access Denied."},
	AccountProblem:                          {http.StatusForbidden, "There is a problem with your AWS account that prevents the operation from completing successfully."},
	AllAccessDisabled:                       {http.StatusForbidden, "All access to this Amazon S3 resource has been disabled."},
	AmbiguousGrantByEmailAddress:            {http.StatusBadRequest, "The email address you provided is associated with more than one account."},
	AuthorizationHeaderMalformed:            {http.StatusBadRequest, "The authorization header is malformed."},
	AuthorizationQueryParametersError:       {http.StatusBadRequest, "Query-string authentication requires the Signature, Expires and AWSAccessKeyId parameters"},
	BadDigest:                               {http.StatusBadRequest, "The Content-MD5 you specified did not match what we received."},
	BadRequest:                              {http.StatusBadRequest, "Bad Request"},
	BucketAlreadyExists:                     {http.StatusConflict, "The requested bucket name is not available. The bucket namespace is shared by all users of the system. Please select a different name and try again."},
	BucketAlreadyOwnedByYou:                 {http.StatusConflict, "Your previous request to create the named bucket succeeded and you already own it."},
	BucketNotEmpty:                          {http.StatusConflict, "The bucket you tried to delete is not empty"},
	ConditionalRequestConflict:              {http.StatusConflict, "The conditional request cannot succeed due to a conflicting operation against this resource."},
	CredentialsNotSupported:                 {http.StatusBadRequest, "This request does not support credentials."},
	CrossLocationLoggingProhibited:          {http.StatusForbidden, "Cross-location logging not allowed."},
	EntityTooSmall:                          {http.StatusBadRequest, "Your proposed upload is smaller than the minimum allowed object size."},
	EntityTooLarge:                          {http.StatusBadRequest, "Your proposed upload exceeds the maximum allowed object size."},
	ExpiredToken:                            {http.StatusBadRequest, "The provided token has expired."},
	IllegalLocationConstraintException:      {http.StatusBadRequest, "The unspecified location constraint is incompatible for the region specific endpoint this request was sent to."},
	IllegalVersioningConfigurationException: {http.StatusBadRequest, "The Versioning configuration specified in the request is invalid."},
	IncompleteBody:                          {http.StatusBadRequest, "You did not provide the number of bytes specified by the Content-Length HTTP header."},
	IncorrectNumberOfFilesInPostRequest:     {http.StatusBadRequest, "POST requires exactly one file upload per request."},
	InlineDataTooLarge:                      {http.StatusBadRequest, "Inline data exceeds the maximum allowed size."},
	InternalError:                           {http.StatusInternalServerError, "We encountered an internal error. Please try again."},
	InvalidAccessKeyId:                      {http.StatusForbidden, "The AWS Access Key Id you provided does not exist in our records."},
	InvalidArgument:                         {http.StatusBadRequest, "Invalid Argument"},
	InvalidBucketName:                       {http.StatusBadRequest, "The specified bucket is not valid."},
	InvalidBucketState:                      {http.StatusConflict, "The request is not valid with the current state of the bucket."},
	InvalidDigest:                           {http.StatusBadRequest, "The Content-MD5 you specified is not valid."},
	InvalidEncryptionAlgorithmError:         {http.StatusBadRequest, "The encryption request you specified is not valid. The valid value is AES256."},
	InvalidLocationConstraint:               {http.StatusBadRequest, "The specified location constraint is not valid."},
	InvalidObjectState:                      {http.StatusForbidden, "The operation is not valid for the current state of the object."},
	InvalidPart:                             {http.StatusBadRequest, "One or more of the specified parts could not be found. The part may not have been uploaded, or the specified entity tag may not match the part's entity tag."},
	InvalidPartOrder:                        {http.StatusBadRequest, "The list of parts was not in ascending order. Parts list must be specified in order by part number."},
	InvalidPolicyDocument:                   {http.StatusBadRequest, "The content of the form does not meet the conditions specified in the policy document."},
	InvalidRange:                            {http.StatusRequestedRangeNotSatisfiable, "The requested range is not satisfiable"},
	InvalidRequest:                          {http.StatusBadRequest, "Invalid Request"},
	InvalidSecurity:                         {http.StatusForbidden, "The provided security credentials are not valid."},
	InvalidStorageClass:                     {http.StatusBadRequest, "The storage class you specified is not valid."},
	InvalidTag:                              {http.StatusBadRequest, "The TagValue you have provided is invalid"},
	InvalidTargetBucketForLogging:           {http.StatusBadRequest, "The target bucket for logging does not exist, is not owned by you, or does not have the appropriate grants for the log-delivery group."},
	InvalidToken:                            {http.StatusBadRequest, "The provided token is malformed or otherwise invalid."},
	InvalidURI:                              {http.StatusBadRequest, "Couldn't parse the specified URI."},
	KeyTooLongError:                         {http.StatusBadRequest, "Your key is too long."},
	MalformedACLError:                       {http.StatusBadRequest, "The XML you provided was not well-formed or did not validate against our published schema."},
	MalformedPOSTRequest:                    {http.StatusBadRequest, "The body of your POST request is not well-formed multipart/form-data."},
	MalformedPolicy:                         {http.StatusBadRequest, "Policy has invalid resource."},
	MalformedXML:                            {http.StatusBadRequest, "The XML you provided was not well-formed or did not validate against our published schema."},
	MaxMessageLengthExceeded:                {http.StatusBadRequest, "Your request was too big."},
	MaxPostPreDataLengthExceededError:       {http.StatusBadRequest, "Your POST request fields preceding the upload file were too large."},
	MetadataTooLarge:                        {http.StatusBadRequest, "Your metadata headers exceed the maximum allowed metadata size."},
	MethodNotAllowed:                        {http.StatusMethodNotAllowed, "The specified method is not allowed against this resource."},
	MissingContentLength:                    {http.StatusLengthRequired, "You must provide the Content-Length HTTP header."},
	MissingRequestBodyError:                 {http.StatusBadRequest, "Request body is empty."},
	MissingSecurityHeader:                   {http.StatusBadRequest, "Your request is missing a required header."},
	NoLoggingStatusForKey:                   {http.StatusBadRequest, "There is no such thing as a logging status subresource for a key."},
	NoSuchBucket:                            {http.StatusNotFound, "The specified bucket does not exist"},
	NoSuchBucketPolicy:                      {http.StatusNotFound, "The bucket policy does not exist"},
	NoSuchCORSConfiguration:                 {http.StatusNotFound, "The CORS configuration does not exist"},
	NoSuchKey:                               {http.StatusNotFound, "The specified key does not exist."},
	NoSuchLifecycleConfiguration:            {http.StatusNotFound, "The lifecycle configuration does not exist"},
	NoSuchObjectLockConfiguration:           {http.StatusNotFound, "The specified object does not have a ObjectLock configuration"},
	NoSuchPublicAccessBlockConfiguration:    {http.StatusNotFound, "The public access block configuration was not found"},
	NoSuchTagSet:                            {http.StatusNotFound, "The TagSet does not exist"},
	NoSuchUpload:                            {http.StatusNotFound, "The specified multipart upload does not exist. The upload ID may be invalid, or the upload may have been aborted or completed."},
	NoSuchVersion:                           {http.StatusNotFound, "The specified version does not exist."},
	NoSuchWebsiteConfiguration:              {http.StatusNotFound, "The specified bucket does not have a website configuration"},
	NotImplemented:                          {http.StatusNotImplemented, "A header you provided implies functionality that is not implemented"},
	NotModified:                             {http.StatusNotModified, "Not Modified"},
	NotSignedUp:                             {http.StatusForbidden, "Your account is not signed up for the Amazon S3 service."},
	ObjectLockConfigurationNotFoundError:    {http.StatusNotFound, "Object Lock configuration does not exist for this bucket"},
	ObjectNotInActiveTierError:              {http.StatusForbidden, "The source object of the COPY operation is not in the active tier and is only stored in Amazon S3 Glacier."},
	OperationAborted:                        {http.StatusConflict, "A conflicting conditional operation is currently in progress against this resource. Try again."},
	OwnershipControlsNotFoundError:          {http.StatusNotFound, "The bucket ownership controls were not found"},
	PermanentRedirect:                       {http.StatusMovedPermanently, "The bucket you are attempting to access must be addressed using the specified endpoint. Send all future requests to this endpoint."},
	PreconditionFailed:                      {http.StatusPreconditionFailed, "At least one of the pre-conditions you specified did not hold"},
	Redirect:                                {http.StatusTemporaryRedirect, "Temporary redirect."},
	ReplicationConfigurationNotFoundError:   {http.StatusNotFound, "The replication configuration was not found"},
	RequestIsNotMultiPartContent:            {http.StatusBadRequest, "Bucket POST must be of the enclosure-type multipart/form-data."},
	RequestTimeout:                          {http.StatusBadRequest, "Your socket connection to the server was not read from or written to within the timeout period."},
	RequestTimeTooSkewed:                    {http.StatusForbidden, "The difference between the request time and the server's time is too large."},
	RequestTorrentOfBucketError:             {http.StatusBadRequest, "Requesting the torrent file of a bucket is not permitted."},
	ServerSideEncryptionConfigurationNotFoundError: {http.StatusNotFound, "The server side encryption configuration was not found"},
	ServiceUnavailable:                {http.StatusServiceUnavailable, "Please reduce your request rate."},
	SignatureDoesNotMatch:             {http.StatusForbidden, "The request signature we calculated does not match the signature you provided. Check your key and signing method."},
	SlowDown:                          {http.StatusServiceUnavailable, "Please reduce your request rate."},
	TemporaryRedirect:                 {http.StatusTemporaryRedirect, "You are being redirected to the bucket while DNS updates."},
	TokenRefreshRequired:              {http.StatusBadRequest, "The provided token must be refreshed."},
	TooManyBuckets:                    {http.StatusBadRequest, "You have attempted to create more buckets than allowed."},
	UnexpectedContent:                 {http.StatusBadRequest, "This request does not support content."},
	UnresolvableGrantByEmailAddress:   {http.StatusBadRequest, "The email address you provided does not match any account on record."},
	UserKeyMustBeSpecified:            {http.StatusBadRequest, "The bucket POST must contain the specified field name. If it is specified, check the order of the fields."},
	XAmzContentSHA256Mismatch:         {http.StatusBadRequest, "The provided 'x-amz-content-sha256' header does not match what was computed."},
	InvalidObjectName:                 {http.StatusBadRequest, "Object name contains unsupported characters."},
	ObjectLockConfigurationNotAllowed: {http.StatusBadRequest, "Object Lock configuration cannot be enabled on existing buckets"},
	InvalidRetentionDate:              {http.StatusBadRequest, "The retain until date must be in the future"},
	ObjectLocked:                      {http.StatusForbidden, "Object is WORM protected and cannot be overwritten"},
	NoSuchObjectLegalHold:             {http.StatusNotFound, "The specified object does not have a legal hold configuration"},
	NoSuchObjectRetention:             {http.StatusNotFound, "The specified object does not have a retention configuration"},
	InvalidEncryptionMethod:           {http.StatusBadRequest, "The encryption method specified is not supported"},
	KMSKeyNotFoundException:           {http.StatusBadRequest, "The specified KMS key does not exist"},
	MissingContentMD5:                 {http.StatusBadRequest, "Missing required header for this request: Content-MD5."},
	NoSuchNotificationConfiguration:   {http.StatusNotFound, "The specified notification configuration does not exist"},
	NoSuchAccessPoint:                 {http.StatusNotFound, "The specified access point does not exist"},
	InvalidChecksum:                   {http.StatusBadRequest, "The provided checksum does not match what was computed."},
	InvalidRequestParameter:           {http.StatusBadRequest, "The request parameter is invalid."},
	QuotaExceeded:                     {http.StatusForbidden, "Bucket quota exceeded"},
	NoSuchUser:                        {http.StatusNotFound, "The specified user does not exist"},
	NoSuchPolicy:                      {http.StatusNotFound, "The specified policy does not exist"},
	NoSuchGroup:                       {http.StatusNotFound, "The specified group does not exist"},
	IncompleteMultipartUpload:         {http.StatusBadRequest, "The multipart upload is incomplete"},
}

// All the codes.
const (
	AccessDenied                                   Code = "AccessDenied"
	AccountProblem                                 Code = "AccountProblem"
	AllAccessDisabled                              Code = "AllAccessDisabled"
	AmbiguousGrantByEmailAddress                   Code = "AmbiguousGrantByEmailAddress"
	AuthorizationHeaderMalformed                   Code = "AuthorizationHeaderMalformed"
	AuthorizationQueryParametersError              Code = "AuthorizationQueryParametersError"
	BadDigest                                      Code = "BadDigest"
	BadRequest                                     Code = "BadRequest"
	BucketAlreadyExists                            Code = "BucketAlreadyExists"
	BucketAlreadyOwnedByYou                        Code = "BucketAlreadyOwnedByYou"
	BucketNotEmpty                                 Code = "BucketNotEmpty"
	ConditionalRequestConflict                     Code = "ConditionalRequestConflict"
	CredentialsNotSupported                        Code = "CredentialsNotSupported"
	CrossLocationLoggingProhibited                 Code = "CrossLocationLoggingProhibited"
	EntityTooSmall                                 Code = "EntityTooSmall"
	EntityTooLarge                                 Code = "EntityTooLarge"
	ExpiredToken                                   Code = "ExpiredToken"
	IllegalLocationConstraintException             Code = "IllegalLocationConstraintException"
	IllegalVersioningConfigurationException        Code = "IllegalVersioningConfigurationException"
	IncompleteBody                                 Code = "IncompleteBody"
	IncorrectNumberOfFilesInPostRequest            Code = "IncorrectNumberOfFilesInPostRequest"
	InlineDataTooLarge                             Code = "InlineDataTooLarge"
	InternalError                                  Code = "InternalError"
	InvalidAccessKeyId                             Code = "InvalidAccessKeyId"
	InvalidArgument                                Code = "InvalidArgument"
	InvalidBucketName                              Code = "InvalidBucketName"
	InvalidBucketState                             Code = "InvalidBucketState"
	InvalidDigest                                  Code = "InvalidDigest"
	InvalidEncryptionAlgorithmError                Code = "InvalidEncryptionAlgorithmError"
	InvalidLocationConstraint                      Code = "InvalidLocationConstraint"
	InvalidObjectState                             Code = "InvalidObjectState"
	InvalidPart                                    Code = "InvalidPart"
	InvalidPartOrder                               Code = "InvalidPartOrder"
	InvalidPolicyDocument                          Code = "InvalidPolicyDocument"
	InvalidRange                                   Code = "InvalidRange"
	InvalidRequest                                 Code = "InvalidRequest"
	InvalidSecurity                                Code = "InvalidSecurity"
	InvalidStorageClass                            Code = "InvalidStorageClass"
	InvalidTag                                     Code = "InvalidTag"
	InvalidTargetBucketForLogging                  Code = "InvalidTargetBucketForLogging"
	InvalidToken                                   Code = "InvalidToken"
	InvalidURI                                     Code = "InvalidURI"
	KeyTooLongError                                Code = "KeyTooLongError"
	MalformedACLError                              Code = "MalformedACLError"
	MalformedPOSTRequest                           Code = "MalformedPOSTRequest"
	MalformedPolicy                                Code = "MalformedPolicy"
	MalformedXML                                   Code = "MalformedXML"
	MaxMessageLengthExceeded                       Code = "MaxMessageLengthExceeded"
	MaxPostPreDataLengthExceededError              Code = "MaxPostPreDataLengthExceededError"
	MetadataTooLarge                               Code = "MetadataTooLarge"
	MethodNotAllowed                               Code = "MethodNotAllowed"
	MissingContentLength                           Code = "MissingContentLength"
	MissingRequestBodyError                        Code = "MissingRequestBodyError"
	MissingSecurityHeader                          Code = "MissingSecurityHeader"
	NoLoggingStatusForKey                          Code = "NoLoggingStatusForKey"
	NoSuchBucket                                   Code = "NoSuchBucket"
	NoSuchBucketPolicy                             Code = "NoSuchBucketPolicy"
	NoSuchCORSConfiguration                        Code = "NoSuchCORSConfiguration"
	NoSuchKey                                      Code = "NoSuchKey"
	NoSuchLifecycleConfiguration                   Code = "NoSuchLifecycleConfiguration"
	NoSuchObjectLockConfiguration                  Code = "NoSuchObjectLockConfiguration"
	NoSuchPublicAccessBlockConfiguration           Code = "NoSuchPublicAccessBlockConfiguration"
	NoSuchTagSet                                   Code = "NoSuchTagSet"
	NoSuchUpload                                   Code = "NoSuchUpload"
	NoSuchVersion                                  Code = "NoSuchVersion"
	NoSuchWebsiteConfiguration                     Code = "NoSuchWebsiteConfiguration"
	NotImplemented                                 Code = "NotImplemented"
	NotModified                                    Code = "NotModified"
	NotSignedUp                                    Code = "NotSignedUp"
	ObjectLockConfigurationNotFoundError           Code = "ObjectLockConfigurationNotFoundError"
	ObjectNotInActiveTierError                     Code = "ObjectNotInActiveTierError"
	OperationAborted                               Code = "OperationAborted"
	OwnershipControlsNotFoundError                 Code = "OwnershipControlsNotFoundError"
	PermanentRedirect                              Code = "PermanentRedirect"
	PreconditionFailed                             Code = "PreconditionFailed"
	Redirect                                       Code = "Redirect"
	ReplicationConfigurationNotFoundError          Code = "ReplicationConfigurationNotFoundError"
	RequestIsNotMultiPartContent                   Code = "RequestIsNotMultiPartContent"
	RequestTimeout                                 Code = "RequestTimeout"
	RequestTimeTooSkewed                           Code = "RequestTimeTooSkewed"
	RequestTorrentOfBucketError                    Code = "RequestTorrentOfBucketError"
	ServerSideEncryptionConfigurationNotFoundError Code = "ServerSideEncryptionConfigurationNotFoundError"
	ServiceUnavailable                             Code = "ServiceUnavailable"
	SignatureDoesNotMatch                          Code = "SignatureDoesNotMatch"
	SlowDown                                       Code = "SlowDown"
	TemporaryRedirect                              Code = "TemporaryRedirect"
	TokenRefreshRequired                           Code = "TokenRefreshRequired"
	TooManyBuckets                                 Code = "TooManyBuckets"
	UnexpectedContent                              Code = "UnexpectedContent"
	UnresolvableGrantByEmailAddress                Code = "UnresolvableGrantByEmailAddress"
	UserKeyMustBeSpecified                         Code = "UserKeyMustBeSpecified"
	XAmzContentSHA256Mismatch                      Code = "XAmzContentSHA256Mismatch"
	InvalidObjectName                              Code = "InvalidObjectName"
	ObjectLockConfigurationNotAllowed              Code = "ObjectLockConfigurationNotAllowed"
	InvalidRetentionDate                           Code = "InvalidRetentionDate"
	ObjectLocked                                   Code = "ObjectLocked"
	NoSuchObjectLegalHold                          Code = "NoSuchObjectLegalHold"
	NoSuchObjectRetention                          Code = "NoSuchObjectRetention"
	InvalidEncryptionMethod                        Code = "InvalidEncryptionMethod"
	KMSKeyNotFoundException                        Code = "KMSKeyNotFoundException"
	MissingContentMD5                              Code = "MissingContentMD5"
	NoSuchNotificationConfiguration                Code = "NoSuchNotificationConfiguration"
	NoSuchAccessPoint                              Code = "NoSuchAccessPoint"
	InvalidChecksum                                Code = "InvalidChecksum"
	InvalidRequestParameter                        Code = "InvalidRequestParameter"
	QuotaExceeded                                  Code = "QuotaExceeded"
	NoSuchUser                                     Code = "NoSuchUser"
	NoSuchPolicy                                   Code = "NoSuchPolicy"
	NoSuchGroup                                    Code = "NoSuchGroup"
	IncompleteMultipartUpload                      Code = "IncompleteMultipartUpload"
	AccessControlListNotSupported                  Code = "AccessControlListNotSupported"
	NoSuchConfiguration                            Code = "NoSuchConfiguration"
	TooManyConfigurations                          Code = "TooManyConfigurations"
)

// New returns the canonical error for code.
func New(code Code) *Error {
	d, ok := table[code]
	if !ok {
		return &Error{Code: code, Status: http.StatusInternalServerError, Message: string(code)}
	}
	return &Error{Code: code, Status: d.status, Message: d.msg}
}

// Newf returns the error for code with a custom message.
func Newf(code Code, format string, args ...any) *Error {
	return New(code).WithMessage(format, args...)
}

// From converts an arbitrary error into an *Error. Non-S3 errors become
// InternalError (the original message is not leaked to clients).
func From(err error) *Error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		return e
	}
	return New(InternalError)
}
