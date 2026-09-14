package console

import (
	"archive/zip"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/edward-b-1/OpenS3/internal/meta"
	"github.com/edward-b-1/OpenS3/internal/object"
)

// Folder operations. S3 has no folders: a "folder" is a key prefix. Deleting
// one means deleting every key (or version) under the prefix; downloading
// one means streaming a zip archive of every current object under it.
// Both are offered by the MinIO console, and the AWS console offers the
// recursive delete.

const deletePrefixPage = 1000

type deletePrefixResult struct {
	Prefix   string          `json:"prefix"`
	Matched  int             `json:"matched"`
	Bytes    int64           `json:"bytes"`
	Deleted  int             `json:"deleted"`
	Failed   int             `json:"failed"`
	Errors   []deleteOutcome `json:"errors"`
	DryRun   bool            `json:"dryRun"`
	Versions bool            `json:"versions"`
}

// deletePrefix removes every object under a prefix. With versions=true it
// permanently deletes every version and delete marker; otherwise it
// deletes current objects (creating delete markers in versioned buckets).
// dryRun only counts.
func (h *Handler) deletePrefix(w http.ResponseWriter, r *http.Request, s *session) error {
	var in struct {
		Prefix           string `json:"prefix"`
		Versions         bool   `json:"versions"`
		BypassGovernance bool   `json:"bypassGovernance"`
		DryRun           bool   `json:"dryRun"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Prefix == "" {
		return badRequest("prefix is required; use bucket deletion to empty a whole bucket")
	}
	bucket := r.PathValue("bucket")
	b, err := h.d.Obj.GetBucket(r.Context(), bucket)
	if err != nil {
		return err
	}
	listAction := "s3:ListBucket"
	if in.Versions {
		listAction = "s3:ListBucketVersions"
	}
	if err := h.authorize(s, listAction, bucket, "", b, nil, map[string][]string{"s3:prefix": {in.Prefix}}); err != nil {
		return err
	}
	res := deletePrefixResult{Prefix: in.Prefix, DryRun: in.DryRun, Versions: in.Versions, Errors: []deleteOutcome{}}
	opt := meta.ListOptions{Prefix: in.Prefix, MaxKeys: deletePrefixPage}
	for {
		var lr *meta.ListResult
		var err error
		if in.Versions {
			lr, err = h.d.Obj.ListVersions(r.Context(), bucket, opt)
		} else {
			lr, err = h.d.Obj.ListObjects(r.Context(), bucket, opt)
		}
		if err != nil {
			return err
		}
		for _, e := range lr.Entries {
			o := e.Object
			if o == nil {
				continue
			}
			res.Matched++
			if !o.DeleteMarker {
				res.Bytes += o.Size
			}
			if in.DryRun {
				continue
			}
			vid := ""
			if in.Versions {
				vid = o.VersionID
			}
			if err := h.deleteOne(r, s, b, o, vid, in.BypassGovernance); err != nil {
				res.Failed++
				if len(res.Errors) < 20 {
					ae := toAPIError(err)
					msg := "internal error"
					if ae != nil {
						msg = ae.Message
					} else {
						h.d.Log.Error("console prefix delete failed", "bucket", bucket, "key", o.Key, "err", err)
					}
					res.Errors = append(res.Errors, deleteOutcome{deleteItem: deleteItem{Key: o.Key, VersionID: vid}, Error: msg})
				}
				continue
			}
			res.Deleted++
		}
		if !lr.IsTruncated {
			break
		}
		// Resume after the last processed record, never from the start, so
		// keys that refuse deletion (object lock) cannot cause a loop.
		if in.Versions {
			opt.KeyMarker, opt.VersionMarker = lr.NextKey, lr.NextVersion
		} else {
			opt.StartAfter = lr.NextKey
		}
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

// deleteOne authorises and deletes one object or version.
func (h *Handler) deleteOne(r *http.Request, s *session, b *meta.Bucket, o *meta.Object, versionID string, bypass bool) error {
	action, cond := "s3:DeleteObject", map[string][]string{}
	if versionID != "" {
		action = "s3:DeleteObjectVersion"
		cond["s3:versionid"] = []string{versionID}
	}
	if err := h.authorize(s, action, b.Name, o.Key, b, o, cond); err != nil {
		return err
	}
	if bypass {
		if err := h.authorize(s, "s3:BypassGovernanceRetention", b.Name, o.Key, b, o, cond); err != nil {
			return err
		}
	}
	_, err := h.d.Obj.DeleteObject(r.Context(), s.actor(), object.DeleteInput{Bucket: b.Name, Key: o.Key, VersionID: versionID, BypassGovernance: bypass})
	return err
}

// zipPrefix streams a zip archive of every current object under a prefix
// (the whole bucket when the prefix is empty). Entry names are relative to
// the parent of the prefix, so zipping "photos/2024/" yields "2024/...".
// Objects the caller may not read and SSE-C objects (which need the
// customer key) are skipped; a MANIFEST lists anything skipped.
func (h *Handler) zipPrefix(w http.ResponseWriter, r *http.Request, s *session) error {
	bucket := r.PathValue("bucket")
	prefix := r.URL.Query().Get("prefix")
	b, err := h.d.Obj.GetBucket(r.Context(), bucket)
	if err != nil {
		return err
	}
	cond := map[string][]string{}
	if prefix != "" {
		cond["s3:prefix"] = []string{prefix}
	}
	if err := h.authorize(s, "s3:ListBucket", bucket, "", b, nil, cond); err != nil {
		return err
	}
	name := bucket
	if prefix != "" {
		name = path.Base(strings.TrimSuffix(prefix, "/"))
	}
	base := ""
	if i := strings.LastIndex(strings.TrimSuffix(prefix, "/"), "/"); i >= 0 {
		base = prefix[:i+1]
	}
	hdr := w.Header()
	hdr.Set("Content-Type", "application/zip")
	hdr.Set("Cache-Control", "private, no-cache")
	hdr.Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(name, `"`, "")+`.zip"; filename*=UTF-8''`+url.PathEscape(name+".zip"))
	w.WriteHeader(http.StatusOK)
	zw := zip.NewWriter(w)
	var skipped []string
	opt := meta.ListOptions{Prefix: prefix, MaxKeys: 1000}
	for {
		lr, err := h.d.Obj.ListObjects(r.Context(), bucket, opt)
		if err != nil {
			zw.Close()
			return nil // headers already sent; the truncated archive signals the failure
		}
		for _, e := range lr.Entries {
			o := e.Object
			if o == nil || strings.HasSuffix(o.Key, "/") && o.Size == 0 {
				continue // folder markers
			}
			if err := h.authorize(s, "s3:GetObject", bucket, o.Key, b, o, nil); err != nil {
				skipped = append(skipped, o.Key+" (access denied)")
				continue
			}
			if o.SSE != nil && o.SSE.Type == "SSE-C" {
				skipped = append(skipped, o.Key+" (SSE-C: customer key required)")
				continue
			}
			res, err := h.d.Obj.GetObject(r.Context(), object.GetInput{Bucket: bucket, Key: o.Key})
			if err != nil {
				skipped = append(skipped, o.Key+" ("+err.Error()+")")
				continue
			}
			fh := &zip.FileHeader{Name: strings.TrimPrefix(o.Key, base), Method: zip.Store, Modified: o.ModTime}
			fh.SetMode(0o644)
			fw, err := zw.CreateHeader(fh)
			if err != nil {
				res.Body.Close()
				zw.Close()
				return nil
			}
			_, err = io.Copy(fw, res.Body)
			res.Body.Close()
			if err != nil {
				zw.Close()
				return nil
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if !lr.IsTruncated {
			break
		}
		opt.StartAfter = lr.NextKey
	}
	if len(skipped) > 0 {
		if fw, err := zw.Create("OPENS3-SKIPPED.txt"); err == nil {
			io.WriteString(fw, "Objects not included in this archive:\n"+strings.Join(skipped, "\n")+"\n")
		}
	}
	return zw.Close()
}
