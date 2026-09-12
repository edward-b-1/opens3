package object

import (
	"context"
	"errors"
	"io"

	"gitlab.com/Birdsall/opens3/internal/blob"
	"gitlab.com/Birdsall/opens3/internal/kv"
	"gitlab.com/Birdsall/opens3/internal/meta"
	"gitlab.com/Birdsall/opens3/internal/s3err"
	"gitlab.com/Birdsall/opens3/internal/sse"
)

// Range is a byte range [Start, End] inclusive, as in HTTP.
type Range struct {
	Start, End int64
}

// GetInput parameters.
type GetInput struct {
	Bucket     string
	Key        string
	VersionID  string // "" = latest
	Range      *Range
	PartNumber int // >0 = return that part only
	Conditions Conditions
	SSE        SSERequest // SSE-C key for encrypted objects
}

// GetResult is the metadata plus a reader over the requested bytes.
type GetResult struct {
	Object *meta.Object
	Body   io.ReadCloser
	// Range actually served (Start/End inclusive) and total object size.
	Range      *Range
	PartsCount int
}

// StatObject returns the version record (or the latest) without opening
// data. A latest delete marker yields NoSuchKey with the marker attached
// via the returned object (o.DeleteMarker) for HEAD/GET semantics.
func (s *Service) StatObject(ctx context.Context, bucket, key, versionID string) (*meta.Object, error) {
	var o *meta.Object
	err := s.kv.View(func(tx kv.Txn) error {
		if _, err := meta.GetBucket(tx, bucket); errors.Is(err, kv.ErrNotFound) {
			return s3err.New(s3err.NoSuchBucket).WithResource(bucket)
		} else if err != nil {
			return err
		}
		var err error
		if versionID == "" {
			o, err = meta.GetLatest(tx, bucket, key)
		} else {
			o, err = meta.GetVersion(tx, bucket, key, versionID)
			if errors.Is(err, kv.ErrNotFound) {
				return s3err.New(s3err.NoSuchVersion)
			}
		}
		return keyErr(err, bucket, key)
	})
	if err != nil {
		return nil, err
	}
	return o, nil
}

// GetObject opens an object for reading.
func (s *Service) GetObject(ctx context.Context, in GetInput) (*GetResult, error) {
	o, err := s.StatObject(ctx, in.Bucket, in.Key, in.VersionID)
	if err != nil {
		return nil, err
	}
	if o.DeleteMarker {
		e := s3err.New(s3err.NoSuchKey).WithHeader("x-amz-delete-marker", "true")
		if in.VersionID != "" {
			e = s3err.New(s3err.MethodNotAllowed).WithHeader("x-amz-delete-marker", "true")
		}
		return nil, e.WithHeader("x-amz-version-id", o.VersionID)
	}
	if err := checkReadConditions(o, in.Conditions); err != nil {
		return nil, err
	}
	if o.SSE != nil && o.SSE.Type == "SSE-C" && in.SSE.CustomerKey == nil {
		return nil, s3err.New(s3err.InvalidRequest).WithMessage("The object was stored using a form of Server Side Encryption. The correct parameters must be provided to retrieve the object.")
	}
	if o.SSE == nil && in.SSE.CustomerKey != nil {
		return nil, s3err.New(s3err.InvalidRequest).WithMessage("The encryption parameters are not applicable to this object.")
	}
	dek, err := s.unwrapDEK(in.Bucket, o.SSE, in.SSE)
	if err != nil {
		return nil, err
	}
	res := &GetResult{Object: o, PartsCount: len(o.Parts)}
	start, end := int64(0), o.Size-1
	if in.PartNumber > 0 {
		if in.PartNumber > len(o.Parts) {
			return nil, s3err.New(s3err.InvalidRange).WithMessage("The requested partnumber is not satisfiable")
		}
		var off int64
		for i := 0; i < in.PartNumber-1; i++ {
			off += o.Parts[i].Size
		}
		start, end = off, off+o.Parts[in.PartNumber-1].Size-1
		res.Range = &Range{start, end}
	} else if in.Range != nil {
		r := *in.Range
		if r.Start < 0 { // suffix range: last -Start bytes
			n := -r.Start
			if n > o.Size {
				n = o.Size
			}
			r.Start, r.End = o.Size-n, o.Size-1
		} else {
			if r.End < 0 || r.End >= o.Size {
				r.End = o.Size - 1
			}
			if r.Start >= o.Size || r.Start > r.End {
				return nil, s3err.New(s3err.InvalidRange).WithHeader("Content-Range", "bytes */"+itoa(o.Size))
			}
		}
		start, end = r.Start, r.End
		res.Range = &r
	}
	if o.Size == 0 || end < start {
		res.Body = io.NopCloser(io.MultiReader())
		return res, nil
	}
	res.Body = s.openRange(ctx, o, dek, start, end-start+1)
	return res, nil
}

// openRange lazily concatenates the parts covering [offset, offset+length).
func (s *Service) openRange(ctx context.Context, o *meta.Object, dek []byte, offset, length int64) io.ReadCloser {
	return &partsReader{s: s, ctx: ctx, o: o, dek: dek, offset: offset, remain: length}
}

type partsReader struct {
	s      *Service
	ctx    context.Context
	o      *meta.Object
	dek    []byte
	offset int64 // absolute plaintext offset of next byte
	remain int64
	cur    io.ReadCloser
	err    error
}

func (p *partsReader) Read(buf []byte) (int, error) {
	for {
		if p.err != nil {
			return 0, p.err
		}
		if p.remain <= 0 {
			return 0, io.EOF
		}
		if p.cur == nil {
			if err := p.openNext(); err != nil {
				p.err = err
				return 0, err
			}
		}
		if int64(len(buf)) > p.remain {
			buf = buf[:p.remain]
		}
		n, err := p.cur.Read(buf)
		p.offset += int64(n)
		p.remain -= int64(n)
		if err == io.EOF {
			p.cur.Close()
			p.cur = nil
			if n == 0 {
				continue
			}
			return n, nil
		}
		if err != nil {
			p.err = err
		}
		return n, err
	}
}

func (p *partsReader) openNext() error {
	var base int64
	for _, part := range p.o.Parts {
		if p.offset < base+part.Size {
			in := p.offset - base
			ln := part.Size - in
			if ln > p.remain {
				ln = p.remain
			}
			rc, err := p.s.openPart(p.ctx, p.o.Bucket, part, p.dek, in, ln)
			if err != nil {
				return err
			}
			p.cur = rc
			return nil
		}
		base += part.Size
	}
	return io.ErrUnexpectedEOF
}

func (p *partsReader) Close() error {
	if p.cur != nil {
		return p.cur.Close()
	}
	return nil
}

// openPart opens plaintext bytes [off, off+ln) of one part.
func (s *Service) openPart(ctx context.Context, bucket string, part meta.Part, dek []byte, off, ln int64) (io.ReadCloser, error) {
	if dek == nil {
		rc, err := s.blobs.Open(ctx, bucket, part.Blob, off, ln)
		if errors.Is(err, blob.ErrNotFound) {
			return nil, s3err.New(s3err.InternalError).WithMessage("object data is missing")
		}
		return rc, err
	}
	return sse.NewDecryptReader(func(ctOff, ctLen int64) (io.ReadCloser, error) {
		rc, err := s.blobs.Open(ctx, bucket, part.Blob, ctOff, ctLen)
		if errors.Is(err, blob.ErrNotFound) {
			return nil, s3err.New(s3err.InternalError).WithMessage("object data is missing")
		}
		return rc, err
	}, dek, part.Nonce, part.Size, off, ln)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// --- listing -------------------------------------------------------------

// ListObjects lists current objects; ListVersions lists all versions.
func (s *Service) ListObjects(ctx context.Context, bucket string, opt meta.ListOptions) (*meta.ListResult, error) {
	var res *meta.ListResult
	err := s.kv.View(func(tx kv.Txn) error {
		if _, err := meta.GetBucket(tx, bucket); errors.Is(err, kv.ErrNotFound) {
			return s3err.New(s3err.NoSuchBucket).WithResource(bucket)
		} else if err != nil {
			return err
		}
		var err error
		res, err = meta.ListObjects(tx, bucket, opt)
		return err
	})
	return res, err
}

// ListVersions lists all versions and delete markers.
func (s *Service) ListVersions(ctx context.Context, bucket string, opt meta.ListOptions) (*meta.ListResult, error) {
	var res *meta.ListResult
	err := s.kv.View(func(tx kv.Txn) error {
		if _, err := meta.GetBucket(tx, bucket); errors.Is(err, kv.ErrNotFound) {
			return s3err.New(s3err.NoSuchBucket).WithResource(bucket)
		} else if err != nil {
			return err
		}
		var err error
		res, err = meta.ListVersions(tx, bucket, opt)
		return err
	})
	return res, err
}
