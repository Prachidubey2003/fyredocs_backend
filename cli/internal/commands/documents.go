package commands

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

// document mirrors the editor-service shape we render. Hand-rolled
// to keep the CLI a standalone Go module — same convention as
// `apiKey` in keys.go.
type document struct {
	ID           string `json:"id"`
	OwnerUserID  string `json:"ownerUserId,omitempty"`
	Title        string `json:"title"`
	StorageKey   string `json:"storageKey,omitempty"`
	SizeBytes    int64  `json:"sizeBytes,omitempty"`
	PageCount    int    `json:"pageCount,omitempty"`
	Status       string `json:"status,omitempty"`
	CurrentRevID string `json:"currentRevId,omitempty"`
	CreatedAt    string `json:"createdAt,omitempty"`
	UpdatedAt    string `json:"updatedAt,omitempty"`
}

type revision struct {
	ID           string `json:"id"`
	DocumentID   string `json:"documentId"`
	ParentRevID  string `json:"parentRevId,omitempty"`
	AuthorUserID string `json:"authorUserId,omitempty"`
	Message      string `json:"message,omitempty"`
	CreatedAt    string `json:"createdAt,omitempty"`
}

// Documents routes the `fyredocs documents <sub>` family.
//
//   - `documents list [--limit N] [--page N]`
//   - `documents get <id>`
//   - `documents revisions <id>`
//   - `documents download <id> [--rev <revId>] [-o <path>]`
//   - `documents edit <id> --ops-file <path>`
//   - `documents delete <id>`
//
// All endpoints proxy through the api-gateway at
// `/api/editor/v1/documents/...` — the CLI's BaseURL points at
// the gateway, so paths start with `/api/editor/v1`.
func Documents(ctx Ctx, args []string) int {
	if len(args) == 0 {
		errorf(ctx, "documents: missing subcommand. Use one of: list, get, revisions, download, edit, delete")
		return 2
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		return docsList(ctx, rest)
	case "get":
		return docsGet(ctx, rest)
	case "revisions":
		return docsRevisions(ctx, rest)
	case "download":
		return docsDownload(ctx, rest)
	case "edit":
		return docsEdit(ctx, rest)
	case "delete":
		return docsDelete(ctx, rest)
	default:
		errorf(ctx, "documents: unknown subcommand %q", sub)
		return 2
	}
}

func docsList(ctx Ctx, args []string) int {
	fs := flag.NewFlagSet("documents list", flag.ContinueOnError)
	fs.SetOutput(ctx.Stderr)
	limit := fs.Int("limit", 20, "page size (max enforced server-side)")
	page := fs.Int("page", 1, "1-indexed page number")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c, err := ctx.NewClient()
	if err != nil {
		if IsNotLoggedIn(err) {
			errorf(ctx, "documents list: not logged in. Run `fyredocs login` first.")
			return 1
		}
		errorf(ctx, "documents list: %v", err)
		return 1
	}
	q := url.Values{}
	q.Set("limit", fmt.Sprintf("%d", *limit))
	q.Set("page", fmt.Sprintf("%d", *page))
	var docs []document
	if err := c.Do("GET", "/api/editor/v1/documents", q, nil, &docs); err != nil {
		errorf(ctx, "documents list: %v", err)
		return 1
	}
	if len(docs) == 0 {
		infof(ctx, "No documents.")
		return 0
	}
	const headerFmt = "%-36s  %-30s  %-7s  %-10s  %s"
	infof(ctx, headerFmt, "ID", "TITLE", "PAGES", "SIZE", "UPDATED")
	infof(ctx, headerFmt, strings.Repeat("-", 36), strings.Repeat("-", 30), "-------",
		"----------", strings.Repeat("-", 19))
	for _, d := range docs {
		infof(ctx, headerFmt, d.ID, truncate(d.Title, 30),
			fmt.Sprintf("%d", d.PageCount), humanBytes(d.SizeBytes), d.UpdatedAt)
	}
	return 0
}

func docsGet(ctx Ctx, args []string) int {
	fs := flag.NewFlagSet("documents get", flag.ContinueOnError)
	fs.SetOutput(ctx.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		errorf(ctx, "documents get: expected exactly one argument <document-id>")
		return 2
	}
	id := fs.Arg(0)
	c, err := ctx.NewClient()
	if err != nil {
		if IsNotLoggedIn(err) {
			errorf(ctx, "documents get: not logged in. Run `fyredocs login` first.")
			return 1
		}
		errorf(ctx, "documents get: %v", err)
		return 1
	}
	var doc document
	if err := c.Do("GET", "/api/editor/v1/documents/"+url.PathEscape(id), nil, nil, &doc); err != nil {
		errorf(ctx, "documents get: %v", err)
		return 1
	}
	// Render as pretty JSON so scripts can pipe through jq.
	enc := json.NewEncoder(ctx.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(doc)
	return 0
}

func docsRevisions(ctx Ctx, args []string) int {
	fs := flag.NewFlagSet("documents revisions", flag.ContinueOnError)
	fs.SetOutput(ctx.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		errorf(ctx, "documents revisions: expected exactly one argument <document-id>")
		return 2
	}
	id := fs.Arg(0)
	c, err := ctx.NewClient()
	if err != nil {
		if IsNotLoggedIn(err) {
			errorf(ctx, "documents revisions: not logged in. Run `fyredocs login` first.")
			return 1
		}
		errorf(ctx, "documents revisions: %v", err)
		return 1
	}
	var revs []revision
	if err := c.Do("GET", "/api/editor/v1/documents/"+url.PathEscape(id)+"/revisions", nil, nil, &revs); err != nil {
		errorf(ctx, "documents revisions: %v", err)
		return 1
	}
	if len(revs) == 0 {
		infof(ctx, "No revisions.")
		return 0
	}
	const headerFmt = "%-36s  %-36s  %-30s  %s"
	infof(ctx, headerFmt, "ID", "PARENT", "MESSAGE", "CREATED")
	infof(ctx, headerFmt, strings.Repeat("-", 36), strings.Repeat("-", 36),
		strings.Repeat("-", 30), strings.Repeat("-", 19))
	for _, r := range revs {
		parent := r.ParentRevID
		if parent == "" {
			parent = "—"
		}
		infof(ctx, headerFmt, r.ID, parent, truncate(r.Message, 30), r.CreatedAt)
	}
	return 0
}

func docsDownload(ctx Ctx, args []string) int {
	fs := flag.NewFlagSet("documents download", flag.ContinueOnError)
	fs.SetOutput(ctx.Stderr)
	rev := fs.String("rev", "", "Optional revision ID — if empty, downloads the current revision")
	outPath := fs.String("o", "", "Output file path (default stdout)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		errorf(ctx, "documents download: expected exactly one argument <document-id>")
		return 2
	}
	id := fs.Arg(0)
	c, err := ctx.NewClient()
	if err != nil {
		if IsNotLoggedIn(err) {
			errorf(ctx, "documents download: not logged in. Run `fyredocs login` first.")
			return 1
		}
		errorf(ctx, "documents download: %v", err)
		return 1
	}

	path := "/api/editor/v1/documents/" + url.PathEscape(id) + "/download"
	if *rev != "" {
		path = "/api/editor/v1/documents/" + url.PathEscape(id) +
			"/revisions/" + url.PathEscape(*rev) + "/download"
	}

	dst := ctx.Stdout
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			errorf(ctx, "documents download: %v", err)
			return 1
		}
		defer f.Close()
		dst = f
	}
	if err := c.DoRaw("GET", path, nil, dst); err != nil {
		errorf(ctx, "documents download: %v", err)
		return 1
	}
	if *outPath != "" {
		errorf(ctx, "Saved to %s", *outPath)
	}
	return 0
}

func docsEdit(ctx Ctx, args []string) int {
	fs := flag.NewFlagSet("documents edit", flag.ContinueOnError)
	fs.SetOutput(ctx.Stderr)
	opsFile := fs.String("ops-file", "", "Path to a JSON file containing the ops array (or '-' for stdin)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		errorf(ctx, "documents edit: expected exactly one argument <document-id>")
		return 2
	}
	if *opsFile == "" {
		errorf(ctx, "documents edit: --ops-file is required (use '-' for stdin)")
		return 2
	}
	id := fs.Arg(0)

	var raw []byte
	if *opsFile == "-" {
		// stdin pipe — tests don't exercise this path because
		// Ctx doesn't yet carry an io.Reader for stdin; that's a
		// minor follow-up. Production-side this works as expected
		// (`cat ops.json | fyredocs documents edit … --ops-file -`).
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			errorf(ctx, "documents edit: read stdin: %v", err)
			return 1
		}
		raw = b
	} else {
		b, err := os.ReadFile(*opsFile)
		if err != nil {
			errorf(ctx, "documents edit: read %s: %v", *opsFile, err)
			return 1
		}
		raw = b
	}

	// Validate JSON shape before sending — the server returns
	// ErrInvalidArgs with op-index prefixes, but pre-validating
	// here means typos in the file get a clearer error.
	var ops []map[string]any
	if err := json.Unmarshal(raw, &ops); err != nil {
		errorf(ctx, "documents edit: ops file must be a JSON array of op objects: %v", err)
		return 2
	}
	if len(ops) == 0 {
		errorf(ctx, "documents edit: ops array is empty")
		return 2
	}

	c, err := ctx.NewClient()
	if err != nil {
		if IsNotLoggedIn(err) {
			errorf(ctx, "documents edit: not logged in. Run `fyredocs login` first.")
			return 1
		}
		errorf(ctx, "documents edit: %v", err)
		return 1
	}
	body := struct {
		Ops []map[string]any `json:"ops"`
	}{Ops: ops}
	var resp struct {
		RevID string `json:"revId"`
	}
	if err := c.Do("POST", "/api/editor/v1/documents/"+url.PathEscape(id)+"/edit", nil, body, &resp); err != nil {
		errorf(ctx, "documents edit: %v", err)
		return 1
	}
	infof(ctx, "%s", resp.RevID)
	return 0
}

func docsDelete(ctx Ctx, args []string) int {
	fs := flag.NewFlagSet("documents delete", flag.ContinueOnError)
	fs.SetOutput(ctx.Stderr)
	yes := fs.Bool("yes", false, "Skip the confirmation prompt (required for non-interactive use)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		errorf(ctx, "documents delete: expected exactly one argument <document-id>")
		return 2
	}
	if !*yes {
		errorf(ctx, "documents delete: pass --yes to confirm deletion (the CLI doesn't prompt interactively)")
		return 2
	}
	id := fs.Arg(0)
	c, err := ctx.NewClient()
	if err != nil {
		if IsNotLoggedIn(err) {
			errorf(ctx, "documents delete: not logged in. Run `fyredocs login` first.")
			return 1
		}
		errorf(ctx, "documents delete: %v", err)
		return 1
	}
	if err := c.Do("DELETE", "/api/editor/v1/documents/"+url.PathEscape(id), nil, nil, nil); err != nil {
		errorf(ctx, "documents delete: %v", err)
		return 1
	}
	infof(ctx, "Deleted %s.", id)
	return 0
}

// humanBytes renders a byte count in the smallest unit ≥ 1.
// Stays terse so the list table doesn't blow horizontally.
func humanBytes(n int64) string {
	switch {
	case n <= 0:
		return "—"
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.1fGB", float64(n)/(1024*1024*1024))
	}
}
