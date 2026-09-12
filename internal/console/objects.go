package console

import (
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"gitlab.com/Birdsall/opens3/internal/meta"
	"gitlab.com/Birdsall/opens3/internal/object"
)

type objectEntry struct {
	Key          string    `json:"key,omitempty"`
	Prefix       string    `json:"prefix,omitempty"`
	Size         int64     `json:"size"`
	ETag         string    `json:"etag,omitempty"`
	LastModified time.Time `json:"lastModified"`
	StorageClass string    `json:"storageClass,omitempty"`
	VersionID    string    `json:"versionId,omitempty"`
	IsLatest     bool      `json:"isLatest"`
	DeleteMarker bool      `json:"deleteMarker,omitempty"`
	Owner        string    `json:"owner,omitempty"`
}

func (h *Handler) listObjects(w http.ResponseWriter, r *http.Request, s *session) error {
	q := r.URL.Query()
	opt := meta.ListOptions{Prefix: q.Get("prefix"), Delimiter: "/", MaxKeys: 200}
	if q.Has("delimiter") {
		opt.Delimiter = q.Get("delimiter")
	}
	if n, err := strconv.Atoi(q.Get("max")); err == nil && n > 0 && n <= 1000 {
		opt.MaxKeys = n
	}
	versions := q.Get("versions") == "1"
	action := "s3:ListBucket"
	if versions {
		action = "s3:ListBucketVersions"
		opt.KeyMarker, opt.VersionMarker = q.Get("after"), q.Get("afterVersion")
	} else {
		opt.StartAfter = q.Get("after")
	}
	cond := map[string][]string{}
	if opt.Prefix != "" {
		cond["s3:prefix"] = []string{opt.Prefix}
	}
	if opt.Delimiter != "" {
		cond["s3:delimiter"] = []string{opt.Delimiter}
	}
	bucket := r.PathValue("bucket")
	b, err := h.d.Obj.GetBucket(r.Context(), bucket)
	if err != nil {
		return err
	}
	if err := h.authorize(s, action, bucket, "", b, nil, cond); err != nil {
		return err
	}
	var res *meta.ListResult
	if versions {
		res, err = h.d.Obj.ListVersions(r.Context(), bucket, opt)
	} else {
		res, err = h.d.Obj.ListObjects(r.Context(), bucket, opt)
	}
	if err != nil {
		return err
	}
	entries := []objectEntry{}
	for _, e := range res.Entries {
		if e.CommonPrefix != "" {
			entries = append(entries, objectEntry{Prefix: e.CommonPrefix})
			continue
		}
		o := e.Object
		ent := objectEntry{Key: o.Key, Size: o.Size, ETag: o.ETag, LastModified: o.ModTime, StorageClass: o.StorageClass, IsLatest: e.IsLatest || !versions, DeleteMarker: o.DeleteMarker, Owner: o.OwnerDisplay}
		if versions || b.Versioning != "" {
			ent.VersionID = o.VersionID
		}
		entries = append(entries, ent)
	}
	out := map[string]any{"entries": entries, "truncated": res.IsTruncated, "prefix": opt.Prefix, "delimiter": opt.Delimiter, "versioning": b.Versioning}
	if res.IsTruncated {
		out["nextAfter"] = res.NextKey
		if versions {
			out["nextAfterVersion"] = res.NextVersion
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type objectDetail struct {
	objectEntry
	ContentType        string              `json:"contentType,omitempty"`
	ContentEncoding    string              `json:"contentEncoding,omitempty"`
	ContentDisposition string              `json:"contentDisposition,omitempty"`
	ContentLanguage    string              `json:"contentLanguage,omitempty"`
	CacheControl       string              `json:"cacheControl,omitempty"`
	Expires            string              `json:"expires,omitempty"`
	UserMeta           map[string]string   `json:"userMeta"`
	Tags               []map[string]string `json:"tags"`
	Parts              int                 `json:"parts"`
	Checksum           *meta.Checksum      `json:"checksum,omitempty"`
	SSE                *sseInfo            `json:"sse,omitempty"`
	Retention          *meta.Retention     `json:"retention,omitempty"`
	LegalHold          bool                `json:"legalHold"`
}

type sseInfo struct {
	Type     string `json:"type"`
	KMSKeyID string `json:"kmsKeyId,omitempty"`
}

// loadObject stats the object and authorises action (versioned variant
// when a versionId is given) on it.
func (h *Handler) loadObject(r *http.Request, s *session, action, versionedAction string) (*meta.Bucket, *meta.Object, error) {
	bucket := r.PathValue("bucket")
	key, vid := r.URL.Query().Get("key"), r.URL.Query().Get("versionId")
	if err := object.ValidObjectKey(key); err != nil {
		return nil, nil, err
	}
	b, err := h.d.Obj.GetBucket(r.Context(), bucket)
	if err != nil {
		return nil, nil, err
	}
	o, err := h.d.Obj.StatObject(r.Context(), bucket, key, vid)
	cond := map[string][]string{}
	if vid != "" {
		action = versionedAction
		cond["s3:versionid"] = []string{vid}
	}
	// Authorise before revealing whether the object exists.
	if aerr := h.authorize(s, action, bucket, key, b, o, cond); aerr != nil {
		return nil, nil, aerr
	}
	if err != nil {
		return nil, nil, err
	}
	return b, o, nil
}

func (h *Handler) headObject(w http.ResponseWriter, r *http.Request, s *session) error {
	_, o, err := h.loadObject(r, s, "s3:GetObject", "s3:GetObjectVersion")
	if err != nil {
		return err
	}
	d := objectDetail{objectEntry: objectEntry{Key: o.Key, Size: o.Size, ETag: o.ETag, LastModified: o.ModTime, StorageClass: o.StorageClass,
		VersionID: o.VersionID, IsLatest: true, DeleteMarker: o.DeleteMarker, Owner: o.OwnerDisplay},
		ContentType: o.ContentType, ContentEncoding: o.ContentEncoding, ContentDisposition: o.ContentDisposition, ContentLanguage: o.ContentLanguage,
		CacheControl: o.CacheControl, Expires: o.Expires, UserMeta: o.UserMeta, Tags: tagsOut(o.Tags), Parts: len(o.Parts), Checksum: o.Checksum,
		Retention: o.Retention, LegalHold: o.LegalHold}
	if d.UserMeta == nil {
		d.UserMeta = map[string]string{}
	}
	if o.SSE != nil {
		d.SSE = &sseInfo{Type: o.SSE.Type, KMSKeyID: o.SSE.KMSKeyID}
	}
	writeJSON(w, http.StatusOK, d)
	return nil
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request, s *session) error {
	b, o, err := h.loadObject(r, s, "s3:GetObject", "s3:GetObjectVersion")
	if err != nil {
		return err
	}
	res, err := h.d.Obj.GetObject(r.Context(), object.GetInput{Bucket: b.Name, Key: o.Key, VersionID: r.URL.Query().Get("versionId")})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	ct := o.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	hdr := w.Header()
	hdr.Set("Content-Type", ct)
	hdr.Set("Content-Length", strconv.FormatInt(o.Size, 10))
	hdr.Set("ETag", `"`+o.ETag+`"`)
	hdr.Set("Last-Modified", o.ModTime.UTC().Format(http.TimeFormat))
	hdr.Set("Cache-Control", "private, no-cache")
	disp := "attachment"
	if r.URL.Query().Get("inline") == "1" && inlineSafe(ct) {
		disp = "inline"
	}
	name := path.Base(o.Key)
	hdr.Set("Content-Disposition", disp+`; filename="`+strings.ReplaceAll(name, `"`, "")+`"; filename*=UTF-8''`+url.PathEscape(name))
	if o.VersionID != "" {
		hdr.Set("x-amz-version-id", o.VersionID)
	}
	w.WriteHeader(http.StatusOK)
	_, err = io.Copy(w, res.Body)
	return err
}

// inlineSafe lists content types that may render in the browser; anything
// active (HTML, SVG, scripts) is always served as an attachment.
func inlineSafe(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	switch ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp", "text/plain", "application/pdf", "application/json", "text/csv",
		"audio/mpeg", "audio/ogg", "audio/wav", "video/mp4", "video/webm":
		return true
	}
	return false
}

type uploadResult struct {
	Key       string `json:"key"`
	VersionID string `json:"versionId,omitempty"`
	Size      int64  `json:"size"`
	ETag      string `json:"etag"`
}

// upload stores one object from a raw PUT body (?key=) or one or more
// objects from a multipart/form-data POST (fields: prefix, key, file...).
func (h *Handler) upload(w http.ResponseWriter, r *http.Request, s *session) error {
	bucket := r.PathValue("bucket")
	b, err := h.d.Obj.GetBucket(r.Context(), bucket)
	if err != nil {
		return err
	}
	put := func(key string, body io.Reader, ct string) (*uploadResult, error) {
		if err := object.ValidObjectKey(key); err != nil {
			return nil, err
		}
		if err := h.authorize(s, "s3:PutObject", bucket, key, b, nil, nil); err != nil {
			return nil, err
		}
		if ct == "" || ct == "application/octet-stream" {
			// Generic or missing type: infer from the extension.
			if byExt := mime.TypeByExtension(path.Ext(key)); byExt != "" {
				ct = byExt
			}
		}
		if ct == "" {
			ct = "application/octet-stream"
		}
		o, err := h.d.Obj.PutObject(r.Context(), s.actor(), object.PutInput{Bucket: bucket, Key: key, Body: body, Size: -1, Attrs: object.ObjectAttrs{ContentType: ct}})
		if err != nil {
			return nil, err
		}
		res := &uploadResult{Key: o.Key, Size: o.Size, ETag: o.ETag}
		if b.Versioning != "" {
			res.VersionID = o.VersionID
		}
		return res, nil
	}
	mt, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "multipart/form-data" {
		key := r.URL.Query().Get("key")
		if key == "" {
			return badRequest("key query parameter is required")
		}
		res, err := put(key, r.Body, r.Header.Get("X-Content-Type"))
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusCreated, map[string]any{"uploaded": []*uploadResult{res}})
		return nil
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return badRequest("invalid multipart body: " + err.Error())
	}
	_ = params
	prefix, forcedKey := "", ""
	var uploaded []*uploadResult
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return badRequest("invalid multipart body: " + err.Error())
		}
		switch p.FormName() {
		case "prefix", "key":
			v, err := io.ReadAll(io.LimitReader(p, 4096))
			if err != nil {
				return badRequest(err.Error())
			}
			if p.FormName() == "prefix" {
				prefix = string(v)
			} else {
				forcedKey = string(v)
			}
		case "file":
			key := forcedKey
			if key == "" {
				key = prefix + p.FileName()
			}
			forcedKey = ""
			res, err := put(key, p, p.Header.Get("Content-Type"))
			if err != nil {
				return err
			}
			uploaded = append(uploaded, res)
		}
		p.Close()
	}
	if len(uploaded) == 0 {
		return badRequest("no file part in upload")
	}
	writeJSON(w, http.StatusCreated, map[string]any{"uploaded": uploaded})
	return nil
}

type deleteItem struct {
	Key       string `json:"key"`
	VersionID string `json:"versionId,omitempty"`
}

type deleteOutcome struct {
	deleteItem
	DeleteMarker bool   `json:"deleteMarker,omitempty"`
	Error        string `json:"error,omitempty"`
}

// deleteObjects removes one or more objects (or specific versions).
func (h *Handler) deleteObjects(w http.ResponseWriter, r *http.Request, s *session) error {
	var in struct {
		Objects          []deleteItem `json:"objects"`
		BypassGovernance bool         `json:"bypassGovernance"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if len(in.Objects) == 0 || len(in.Objects) > 1000 {
		return badRequest("between 1 and 1000 objects per request")
	}
	bucket := r.PathValue("bucket")
	b, err := h.d.Obj.GetBucket(r.Context(), bucket)
	if err != nil {
		return err
	}
	results := make([]deleteOutcome, 0, len(in.Objects))
	failed := 0
	for _, it := range in.Objects {
		out := deleteOutcome{deleteItem: it}
		err := func() error {
			if err := object.ValidObjectKey(it.Key); err != nil {
				return err
			}
			action, cond := "s3:DeleteObject", map[string][]string{}
			if it.VersionID != "" {
				action = "s3:DeleteObjectVersion"
				cond["s3:versionid"] = []string{it.VersionID}
			}
			o, _ := h.d.Obj.StatObject(r.Context(), bucket, it.Key, it.VersionID)
			if err := h.authorize(s, action, bucket, it.Key, b, o, cond); err != nil {
				return err
			}
			if in.BypassGovernance {
				if err := h.authorize(s, "s3:BypassGovernanceRetention", bucket, it.Key, b, o, cond); err != nil {
					return err
				}
			}
			res, err := h.d.Obj.DeleteObject(r.Context(), s.actor(), object.DeleteInput{Bucket: bucket, Key: it.Key, VersionID: it.VersionID, BypassGovernance: in.BypassGovernance})
			if err != nil {
				return err
			}
			out.DeleteMarker = res.DeleteMarker
			if res.DeleteMarker && it.VersionID == "" {
				out.VersionID = res.VersionID
			}
			return nil
		}()
		if err != nil {
			failed++
			ae := toAPIError(err)
			if ae == nil {
				h.d.Log.Error("console delete failed", "bucket", bucket, "key", it.Key, "err", err)
				ae = apiErr(http.StatusInternalServerError, "InternalError", "internal error")
			}
			out.Error = ae.Message
		}
		results = append(results, out)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "failed": failed})
	return nil
}
