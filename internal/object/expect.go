package object

import (
	"context"
	"time"

	"github.com/edward-b-1/opens3/internal/meta"
)

// Request expectations bind an operation to the records it was authorised
// against. The API layer reads a bucket and, for object operations, an
// object version when it authorises a request; the service re-reads them
// inside its write transaction. If a record was replaced in between (a
// bucket deleted and recreated under the same name, an object overwritten
// so that a public version became a private one) the operation must not
// proceed on the replacement, because the authorisation no longer applies
// to it. The expectations travel in the context so every service entry
// point checks them without extra parameters.

type expectKey struct{}

type expectation struct {
	bucketCreated time.Time // zero = no expectation
	objectSeq     uint64    // 0 = no expectation
	sourceSeq     uint64    // copy source, 0 = none
	sourceBucket  time.Time // creation time of the copy source's bucket
}

func expectations(ctx context.Context) expectation {
	e, _ := ctx.Value(expectKey{}).(expectation)
	return e
}

// WithExpectedBucket records that the operation was authorised against b.
func WithExpectedBucket(ctx context.Context, b *meta.Bucket) context.Context {
	if b == nil {
		return ctx
	}
	e := expectations(ctx)
	e.bucketCreated = b.Created
	return context.WithValue(ctx, expectKey{}, e)
}

// WithExpectedObject records that the operation was authorised against
// object version o (the target of the operation).
func WithExpectedObject(ctx context.Context, o *meta.Object) context.Context {
	if o == nil {
		return ctx
	}
	e := expectations(ctx)
	e.objectSeq = o.Seq
	return context.WithValue(ctx, expectKey{}, e)
}

// WithExpectedSource records the copy source bucket and version that
// were authorised.
func WithExpectedSource(ctx context.Context, b *meta.Bucket, o *meta.Object) context.Context {
	e := expectations(ctx)
	if b != nil {
		e.sourceBucket = b.Created
	}
	if o != nil {
		e.sourceSeq = o.Seq
	}
	return context.WithValue(ctx, expectKey{}, e)
}

// bucketExpected reports whether b is the bucket the operation was
// authorised against (true when nothing was recorded).
func bucketExpected(ctx context.Context, b *meta.Bucket) bool {
	e := expectations(ctx)
	return e.bucketCreated.IsZero() || (b != nil && b.Created.Equal(e.bucketCreated))
}

// objectExpected reports whether o is the version the operation was
// authorised against.
func objectExpected(ctx context.Context, o *meta.Object) bool {
	e := expectations(ctx)
	return e.objectSeq == 0 || (o != nil && o.Seq == e.objectSeq)
}

// sourceExpected reports whether b and o are the copy source bucket and
// version that were authorised.
func sourceExpected(ctx context.Context, b *meta.Bucket, o *meta.Object) bool {
	e := expectations(ctx)
	if !e.sourceBucket.IsZero() && (b == nil || !b.Created.Equal(e.sourceBucket)) {
		return false
	}
	return e.sourceSeq == 0 || (o != nil && o.Seq == e.sourceSeq)
}
