package fyredocs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// DocumentsAPI wraps the `/api/editor/v1/documents/*` endpoints
// owned by editor-service.
type DocumentsAPI struct {
	c *Client
}

// ListDocumentsOptions tunes a List call.
type ListDocumentsOptions struct {
	Page  int // 1-indexed; 0 means "use server default"
	Limit int // server caps this regardless of caller value
}

// List returns the calling user's documents, newest-first.
func (d *DocumentsAPI) List(ctx context.Context, opts *ListDocumentsOptions) ([]EditorDocument, error) {
	q := url.Values{}
	if opts != nil {
		if opts.Page > 0 {
			q.Set("page", fmt.Sprintf("%d", opts.Page))
		}
		if opts.Limit > 0 {
			q.Set("limit", fmt.Sprintf("%d", opts.Limit))
		}
	}
	var out []EditorDocument
	if err := d.c.Request(ctx, "/api/editor/v1/documents", RequestOptions{
		Method: http.MethodGet,
		Query:  q,
		Out:    &out,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// Get fetches one document's metadata by ID.
func (d *DocumentsAPI) Get(ctx context.Context, id string) (*EditorDocument, error) {
	var out EditorDocument
	if err := d.c.Request(ctx, "/api/editor/v1/documents/"+url.PathEscape(id), RequestOptions{
		Method: http.MethodGet,
		Out:    &out,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// Revisions lists every revision for a document.
func (d *DocumentsAPI) Revisions(ctx context.Context, id string) ([]EditorRevision, error) {
	var out []EditorRevision
	if err := d.c.Request(ctx, "/api/editor/v1/documents/"+url.PathEscape(id)+"/revisions", RequestOptions{
		Method: http.MethodGet,
		Out:    &out,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// Edit applies a batch of sPDOM ops as one revision. Returns the
// new Revision row. Ops chain in order; an error on op[i] leaves
// the document unchanged (the dispatcher prefixes failures with
// `ops[i]:` so callers can pinpoint the bad op).
func (d *DocumentsAPI) Edit(ctx context.Context, id string, req EditRequest) (*EditorRevision, error) {
	var out EditorRevision
	if err := d.c.Request(ctx, "/api/editor/v1/documents/"+url.PathEscape(id)+"/edit", RequestOptions{
		Method: http.MethodPost,
		Body:   req,
		Out:    &out,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// DownloadOptions tunes a Download call.
type DownloadOptions struct {
	// RevID selects a prior revision; empty means current.
	RevID string
}

// Download streams the PDF bytes for `id`'s current revision (or
// the revision in opts.RevID when set) to `dst`. The response is
// `application/pdf`, not the JSON envelope.
func (d *DocumentsAPI) Download(ctx context.Context, id string, opts *DownloadOptions, dst io.Writer) error {
	path := "/api/editor/v1/documents/" + url.PathEscape(id) + "/download"
	if opts != nil && opts.RevID != "" {
		path = "/api/editor/v1/documents/" + url.PathEscape(id) +
			"/revisions/" + url.PathEscape(opts.RevID) + "/download"
	}
	return d.c.RequestStream(ctx, path, RequestOptions{Method: http.MethodGet}, dst)
}

// Delete soft-deletes a document. Idempotent — the server flips
// `status` to "deleted"; cleanup-worker eventually purges the
// underlying bytes.
func (d *DocumentsAPI) Delete(ctx context.Context, id string) error {
	return d.c.Request(ctx, "/api/editor/v1/documents/"+url.PathEscape(id), RequestOptions{
		Method: http.MethodDelete,
	})
}
