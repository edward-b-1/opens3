package object

import (
	"context"
	"io"
	"strconv"

	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/s3err"
)

// CopyInput parameters for CopyObject.
type CopyInput struct {
	SrcBucket, SrcKey, SrcVersionID string
	SrcConditions                   Conditions // x-amz-copy-source-if-*
	SrcSSE                          SSERequest // SSE-C key of the source
	DstBucket, DstKey               string
	DstConditions                   Conditions // conditional write on destination
	// MetadataDirective COPY keeps source attributes; REPLACE uses Attrs.
	MetadataDirective string
	TaggingDirective  string
	Attrs             ObjectAttrs
	SSE               SSERequest
	Checksum          *ChecksumRequest // algorithm to compute on the copy
}

// CopyObject copies an object (or version) server-side.
func (s *Service) CopyObject(ctx context.Context, actor Actor, in CopyInput) (*meta.Object, *meta.Object, error) {
	src, err := s.GetObject(ctx, GetInput{Bucket: in.SrcBucket, Key: in.SrcKey, VersionID: in.SrcVersionID, Conditions: in.SrcConditions, SSE: in.SrcSSE})
	if err != nil {
		if e, ok := err.(*s3err.Error); ok && e.Code == s3err.NotModified {
			return nil, nil, s3err.New(s3err.PreconditionFailed)
		}
		return nil, nil, err
	}
	defer src.Body.Close()
	so := src.Object
	if so.StorageClass == "GLACIER" || so.StorageClass == "DEEP_ARCHIVE" {
		if so.RestoreExpiry == nil {
			return nil, nil, s3err.New(s3err.InvalidObjectState)
		}
	}
	attrs := in.Attrs
	if in.MetadataDirective != "REPLACE" {
		attrs.ContentType, attrs.ContentEncoding, attrs.ContentDisposition, attrs.ContentLanguage = so.ContentType, so.ContentEncoding, so.ContentDisposition, so.ContentLanguage
		attrs.CacheControl, attrs.Expires, attrs.WebsiteRedirect, attrs.UserMeta = so.CacheControl, so.Expires, so.WebsiteRedirect, so.UserMeta
		if in.SrcBucket == in.DstBucket && in.SrcKey == in.DstKey && in.SrcVersionID == "" {
			return nil, nil, s3err.New(s3err.InvalidRequest).WithMessage("This copy request is illegal because it is trying to copy an object to itself without changing the object's metadata, storage class, website redirect location or encryption attributes.")
		}
	}
	if in.TaggingDirective != "REPLACE" {
		attrs.Tags = so.Tags
	}
	if attrs.StorageClass == "" {
		attrs.StorageClass = so.StorageClass
	}
	var cr *ChecksumRequest
	if in.Checksum != nil && in.Checksum.Algorithm != "" {
		cr = &ChecksumRequest{Algorithm: in.Checksum.Algorithm}
	} else if so.Checksum != nil {
		cr = &ChecksumRequest{Algorithm: so.Checksum.Algorithm}
	}
	var body io.Reader = src.Body
	dst, err := s.PutObject(ctx, actor, PutInput{Bucket: in.DstBucket, Key: in.DstKey, Body: body, Size: so.Size, Checksum: cr,
		SSE: in.SSE, Attrs: attrs, Conditions: in.DstConditions, Event: "s3:ObjectCreated:Copy"})
	if err != nil {
		return nil, nil, err
	}
	return dst, so, nil
}

// UploadPartCopyInput parameters.
type UploadPartCopyInput struct {
	SrcBucket, SrcKey, SrcVersionID string
	SrcRange                        *Range
	SrcConditions                   Conditions
	SrcSSE                          SSERequest
	Bucket, Key, UploadID           string
	PartNumber                      int
	SSE                             SSERequest
}

// UploadPartCopy copies a byte range of an object into a part.
func (s *Service) UploadPartCopy(ctx context.Context, in UploadPartCopyInput) (*meta.Part, *meta.Object, error) {
	src, err := s.GetObject(ctx, GetInput{Bucket: in.SrcBucket, Key: in.SrcKey, VersionID: in.SrcVersionID, Range: in.SrcRange, Conditions: in.SrcConditions, SSE: in.SrcSSE})
	if err != nil {
		return nil, nil, err
	}
	defer src.Body.Close()
	size := src.Object.Size
	if r := in.SrcRange; r != nil && r.Start >= 0 && (r.Start >= src.Object.Size || r.End >= src.Object.Size) {
		return nil, nil, s3err.New(s3err.InvalidRange).WithMessage("The requested range is not satisfiable").WithExtra("ActualObjectSize", strconv.FormatInt(src.Object.Size, 10))
	}
	if src.Range != nil {
		size = src.Range.End - src.Range.Start + 1
	}
	if size > MaxPartSize {
		return nil, nil, s3err.New(s3err.InvalidRequest).WithMessage("The specified copy source is larger than the maximum allowable size for a copy source: %d", MaxPartSize)
	}
	p, err := s.UploadPart(ctx, UploadPartInput{Bucket: in.Bucket, Key: in.Key, UploadID: in.UploadID, PartNumber: in.PartNumber, Body: src.Body, Size: size, SSE: in.SSE})
	if err != nil {
		return nil, nil, err
	}
	return p, src.Object, nil
}
