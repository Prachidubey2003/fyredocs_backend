package storage

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestOwnerForUser(t *testing.T) {
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	o := OwnerForUser(id)
	want := filepath.Join("users", id.String())
	if o.String() != want {
		t.Errorf("OwnerForUser: got %q, want %q", o.String(), want)
	}
}

func TestOwnerForGuest(t *testing.T) {
	jobID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	o := OwnerForGuest(jobID)
	want := filepath.Join("guests", jobID.String())
	if o.String() != want {
		t.Errorf("OwnerForGuest: got %q, want %q", o.String(), want)
	}
}

func TestOwnerFor_AuthAndGuest(t *testing.T) {
	uid := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	job := uuid.MustParse("44444444-4444-4444-4444-444444444444")

	got := OwnerFor(&uid, job)
	if !strings.HasPrefix(got.String(), "users/") {
		t.Errorf("auth path should be under users/, got %q", got.String())
	}

	got = OwnerFor(nil, job)
	if !strings.HasPrefix(got.String(), "guests/") {
		t.Errorf("guest path should be under guests/, got %q", got.String())
	}
}

func TestUploadChunkPath(t *testing.T) {
	uid := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	upload := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	got := UploadChunkPath("/files", OwnerForUser(uid), upload, 7)
	want := filepath.Join("/files", "users", uid.String(), "uploads", upload.String(), "000007.part")
	if got != want {
		t.Errorf("UploadChunkPath: got %q, want %q", got, want)
	}
}

func TestUploadAssembledPath_SanitizesFileName(t *testing.T) {
	uid := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	upload := uuid.MustParse("66666666-6666-6666-6666-666666666666")
	cases := map[string]string{
		"doc.pdf":             "doc.pdf",
		"../../../etc/passwd": "passwd",
		"/etc/shadow":         "shadow",
		"":                    "_",
		".":                   "_",
		"..":                  "_",
		"sub/dir/leaf.docx":   "leaf.docx",
		"backslash\\path.txt": "_",
	}
	for input, wantBase := range cases {
		got := UploadAssembledPath("/files", OwnerForUser(uid), upload, input)
		gotBase := filepath.Base(got)
		if gotBase != wantBase {
			t.Errorf("input %q: got base %q, want %q", input, gotBase, wantBase)
		}
		// Path must always stay under the user's upload dir.
		wantPrefix := filepath.Join("/files", "users", uid.String(), "uploads", upload.String())
		if !strings.HasPrefix(got, wantPrefix) {
			t.Errorf("input %q: path escaped owner dir: got %q", input, got)
		}
	}
}

func TestJobPaths(t *testing.T) {
	uid := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	job := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	owner := OwnerForUser(uid)

	in := JobInputPath("/files", owner, job, "src.pdf")
	out := JobOutputPath("/files", owner, job, "out.docx")
	scratch := JobScratchDir("/files", owner, job)

	for _, p := range []string{in, out, scratch} {
		wantPrefix := filepath.Join("/files", "users", uid.String(), "jobs", job.String())
		if !strings.HasPrefix(p, wantPrefix) {
			t.Errorf("path %q does not start with %q", p, wantPrefix)
		}
	}
	if filepath.Base(filepath.Dir(in)) != "input" {
		t.Errorf("JobInputPath should land under input/, got %q", in)
	}
	if filepath.Base(filepath.Dir(out)) != "output" {
		t.Errorf("JobOutputPath should land under output/, got %q", out)
	}
	if filepath.Base(scratch) != "scratch" {
		t.Errorf("JobScratchDir should end in scratch/, got %q", scratch)
	}
}

func TestSafeFileName(t *testing.T) {
	tests := map[string]string{
		"doc.pdf":        "doc.pdf",
		"a/b/c.txt":      "c.txt",
		"../../../bad":   "bad",
		"":               "_",
		".":              "_",
		"..":             "_",
		"/":              "_",
		"sub\\bad":       "_",
		" leading space": " leading space",
	}
	for input, want := range tests {
		if got := SafeFileName(input); got != want {
			t.Errorf("SafeFileName(%q): got %q, want %q", input, got, want)
		}
	}
}
