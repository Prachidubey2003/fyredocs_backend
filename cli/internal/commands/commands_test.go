package commands

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"fyredocs-cli/internal/client"
	"fyredocs-cli/internal/config"
)

// testCtx builds a Ctx with stdout/stderr buffers + a NewClient
// that points at the given httptest server. Used by every
// command test to capture output without writing to os.Stdout.
func testCtx(serverURL string) (Ctx, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	ctx := Ctx{
		Stdout: &stdout,
		Stderr: &stderr,
		NewClient: func() (*client.Client, error) {
			return client.New(serverURL, "fyr_test_key"), nil
		},
	}
	return ctx, &stdout, &stderr
}

func TestLogin_RequiresAPIKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, _, stderr := testCtx("")
	if rc := Login(ctx, []string{}); rc != 2 {
		t.Errorf("rc = %d, want 2 (missing flag)", rc)
	}
	if !strings.Contains(stderr.String(), "--api-key is required") {
		t.Errorf("stderr = %q, want hint about --api-key", stderr.String())
	}
}

func TestLogin_StoresKeyAndLogoutClearsIt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, stdout, _ := testCtx("")

	if rc := Login(ctx, []string{"--api-key", "fyr_live_abc"}); rc != 0 {
		t.Fatalf("Login rc = %d", rc)
	}
	if !strings.Contains(stdout.String(), "Logged in") {
		t.Errorf("expected confirmation; got %q", stdout.String())
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config not stored: %v", err)
	}
	if cfg.APIKey != "fyr_live_abc" {
		t.Errorf("APIKey = %q, want fyr_live_abc", cfg.APIKey)
	}

	// Logout clears it.
	stdout.Reset()
	if rc := Logout(ctx, nil); rc != 0 {
		t.Errorf("Logout rc = %d", rc)
	}
	if _, err := config.Load(); !errors.Is(err, config.ErrNotLoggedIn) {
		t.Errorf("post-logout Load: %v, want ErrNotLoggedIn", err)
	}
}

func TestWhoami_PrintsPlanAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{
			"plan":{"code":"pro","name":"Pro"},
			"subscription":{"status":"active","seats":1,"currentPeriodEnd":"2026-06-01T00:00:00Z"}
		}}`))
	}))
	defer srv.Close()
	ctx, stdout, _ := testCtx(srv.URL)
	if rc := Whoami(ctx, nil); rc != 0 {
		t.Fatalf("Whoami rc = %d; stdout=%q", rc, stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Plan:") || !strings.Contains(out, "Pro") {
		t.Errorf("missing plan line: %q", out)
	}
	if !strings.Contains(out, "Status: active") {
		t.Errorf("missing status line: %q", out)
	}
}

func TestWhoami_HandlesFreeTier(t *testing.T) {
	// subscription=null → "free tier (no subscription)"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{
			"plan":{"code":"free","name":"Free"},
			"subscription":null
		}}`))
	}))
	defer srv.Close()
	ctx, stdout, _ := testCtx(srv.URL)
	if rc := Whoami(ctx, nil); rc != 0 {
		t.Fatalf("Whoami rc = %d", rc)
	}
	if !strings.Contains(stdout.String(), "free tier") {
		t.Errorf("expected free-tier hint; got %q", stdout.String())
	}
}

func TestUsage_RendersTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{
			"userId":"u1","period":"2026-05",
			"items":[
				{"eventType":"op.merge","unit":"ops","totalQuantity":12,"eventCount":12},
				{"eventType":"op.ocr","unit":"pages","totalQuantity":50,"eventCount":1}
			]
		}}`))
	}))
	defer srv.Close()
	ctx, stdout, _ := testCtx(srv.URL)
	if rc := Usage(ctx, []string{"--period", "2026-05"}); rc != 0 {
		t.Fatalf("Usage rc = %d; stdout=%q", rc, stdout.String())
	}
	out := stdout.String()
	for _, needle := range []string{"2026-05", "op.merge", "op.ocr", "Event", "Quantity"} {
		if !strings.Contains(out, needle) {
			t.Errorf("table missing %q in:\n%s", needle, out)
		}
	}
}

func TestUsage_EmptyState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{
			"userId":"u1","period":"2026-05","items":[]
		}}`))
	}))
	defer srv.Close()
	ctx, stdout, _ := testCtx(srv.URL)
	if rc := Usage(ctx, nil); rc != 0 {
		t.Fatalf("Usage rc = %d", rc)
	}
	if !strings.Contains(stdout.String(), "No metered usage") {
		t.Errorf("expected empty-state hint; got %q", stdout.String())
	}
}

func TestKeys_RoutesSubcommands(t *testing.T) {
	ctx, _, stderr := testCtx("")
	// Empty args → usage hint, exit 2.
	if rc := Keys(ctx, nil); rc != 2 {
		t.Errorf("Keys with no sub: rc = %d, want 2", rc)
	}
	if !strings.Contains(stderr.String(), "missing subcommand") {
		t.Errorf("missing-sub hint not printed: %q", stderr.String())
	}

	// Unknown subcommand → exit 2.
	stderr.Reset()
	if rc := Keys(ctx, []string{"frobnicate"}); rc != 2 {
		t.Errorf("Keys with unknown sub: rc = %d, want 2", rc)
	}
	if !strings.Contains(stderr.String(), "unknown subcommand") {
		t.Errorf("unknown-sub hint not printed: %q", stderr.String())
	}
}

func TestKeys_ListPrintsTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"k1","name":"CI","environment":"live","keyPrefix":"fyr_live_abc","createdAt":"2026-05-01T00:00:00Z"}
		]}`))
	}))
	defer srv.Close()
	ctx, stdout, _ := testCtx(srv.URL)
	if rc := Keys(ctx, []string{"list"}); rc != 0 {
		t.Fatalf("rc = %d; out=%q", rc, stdout.String())
	}
	for _, needle := range []string{"k1", "CI", "live", "fyr_live_abc"} {
		if !strings.Contains(stdout.String(), needle) {
			t.Errorf("table missing %q: %q", needle, stdout.String())
		}
	}
}

func TestKeys_CreateShowsPlaintextOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"data":{
			"key":{"id":"k1","name":"CI","environment":"live","keyPrefix":"fyr_live_abc","createdAt":"2026-05-01T00:00:00Z"},
			"plaintext":"fyr_live_abc_supersecrettoken"
		}}`))
	}))
	defer srv.Close()
	ctx, stdout, _ := testCtx(srv.URL)
	if rc := Keys(ctx, []string{"create", "--name", "CI"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(stdout.String(), "fyr_live_abc_supersecrettoken") {
		t.Errorf("plaintext missing from output: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "ONLY time") {
		t.Errorf("missing one-shot warning: %q", stdout.String())
	}
}

func TestKeys_CreateRejectsEmptyName(t *testing.T) {
	ctx, _, stderr := testCtx("")
	if rc := Keys(ctx, []string{"create"}); rc != 2 {
		t.Errorf("rc = %d, want 2", rc)
	}
	if !strings.Contains(stderr.String(), "--name is required") {
		t.Errorf("missing hint: %q", stderr.String())
	}
}

func TestKeys_CreateRejectsBadEnvironment(t *testing.T) {
	ctx, _, stderr := testCtx("")
	if rc := Keys(ctx, []string{"create", "--name", "x", "--environment", "production"}); rc != 2 {
		t.Errorf("rc = %d, want 2", rc)
	}
	if !strings.Contains(stderr.String(), "live or test") {
		t.Errorf("missing hint: %q", stderr.String())
	}
}

func TestKeys_Revoke(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/auth/api-keys/") || !strings.HasSuffix(r.URL.Path, "/revoke") {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	ctx, stdout, _ := testCtx(srv.URL)
	if rc := Keys(ctx, []string{"revoke", "k1"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(stdout.String(), "Revoked k1") {
		t.Errorf("confirmation missing: %q", stdout.String())
	}
}

func TestKeys_RevokeRequiresOneArg(t *testing.T) {
	ctx, _, stderr := testCtx("")
	if rc := Keys(ctx, []string{"revoke"}); rc != 2 {
		t.Errorf("rc = %d, want 2", rc)
	}
	if !strings.Contains(stderr.String(), "exactly one argument") {
		t.Errorf("missing hint: %q", stderr.String())
	}
}

func TestNotLoggedInIsHandledGracefully(t *testing.T) {
	// When NewClient returns ErrNotLoggedIn, commands should
	// exit 1 with a "run login first" hint — never panic.
	ctx := Ctx{
		Stdout: new(bytes.Buffer),
		Stderr: new(bytes.Buffer),
		NewClient: func() (*client.Client, error) {
			return nil, config.ErrNotLoggedIn
		},
	}
	for _, cmd := range []func(Ctx, []string) int{Whoami, Usage} {
		stderr := ctx.Stderr.(*bytes.Buffer)
		stderr.Reset()
		rc := cmd(ctx, nil)
		if rc != 1 {
			t.Errorf("rc = %d, want 1", rc)
		}
		if !strings.Contains(stderr.String(), "not logged in") {
			t.Errorf("missing hint: %q", stderr.String())
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is too long", 10, "this is t…"},
		{"x", 0, ""},
		{"x", 1, "x"},
	}
	for _, c := range cases {
		if got := truncate(c.in, c.max); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

// ---- Documents ---------------------------------------------------

func TestDocuments_RoutesSubcommands(t *testing.T) {
	ctx, _, stderr := testCtx("")
	if rc := Documents(ctx, nil); rc != 2 {
		t.Errorf("no-args rc = %d, want 2", rc)
	}
	if !strings.Contains(stderr.String(), "missing subcommand") {
		t.Errorf("stderr = %q, want hint about subcommand", stderr.String())
	}

	stderr.Reset()
	if rc := Documents(ctx, []string{"bogus"}); rc != 2 {
		t.Errorf("unknown-sub rc = %d, want 2", rc)
	}
	if !strings.Contains(stderr.String(), "unknown subcommand") {
		t.Errorf("stderr = %q, want unknown-subcommand hint", stderr.String())
	}
}

func TestDocuments_ListRendersTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/editor/v1/documents" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("limit"); got != "5" {
			t.Errorf("limit query = %q, want 5", got)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"doc_1","title":"Q4 Report","pageCount":12,"sizeBytes":204800,"updatedAt":"2026-05-16T10:00:00Z"},
			{"id":"doc_2","title":"NDA","pageCount":3,"sizeBytes":1024,"updatedAt":"2026-05-15T09:00:00Z"}
		]}`))
	}))
	defer srv.Close()
	ctx, stdout, _ := testCtx(srv.URL)
	if rc := Documents(ctx, []string{"list", "--limit", "5"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	out := stdout.String()
	if !strings.Contains(out, "Q4 Report") || !strings.Contains(out, "NDA") {
		t.Errorf("missing titles in table: %q", out)
	}
	if !strings.Contains(out, "doc_1") {
		t.Errorf("missing doc id in table: %q", out)
	}
}

func TestDocuments_ListEmptyState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	}))
	defer srv.Close()
	ctx, stdout, _ := testCtx(srv.URL)
	if rc := Documents(ctx, []string{"list"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(stdout.String(), "No documents.") {
		t.Errorf("expected empty-state hint, got %q", stdout.String())
	}
}

func TestDocuments_GetPrintsJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/editor/v1/documents/doc_abc" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{
			"id":"doc_abc","title":"Spec","pageCount":7,"currentRevId":"rev_xyz"
		}}`))
	}))
	defer srv.Close()
	ctx, stdout, _ := testCtx(srv.URL)
	if rc := Documents(ctx, []string{"get", "doc_abc"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	out := stdout.String()
	if !strings.Contains(out, `"id": "doc_abc"`) {
		t.Errorf("expected pretty-JSON `id`: %q", out)
	}
	if !strings.Contains(out, `"currentRevId": "rev_xyz"`) {
		t.Errorf("expected pretty-JSON `currentRevId`: %q", out)
	}
}

func TestDocuments_GetRequiresOneArg(t *testing.T) {
	ctx, _, stderr := testCtx("")
	if rc := Documents(ctx, []string{"get"}); rc != 2 {
		t.Errorf("rc = %d, want 2", rc)
	}
	if !strings.Contains(stderr.String(), "expected exactly one argument") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestDocuments_RevisionsRendersTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/editor/v1/documents/doc_x/revisions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"rev_a","documentId":"doc_x","message":"initial","createdAt":"2026-05-10T08:00:00Z"},
			{"id":"rev_b","documentId":"doc_x","parentRevId":"rev_a","message":"highlight pg 3","createdAt":"2026-05-11T12:00:00Z"}
		]}`))
	}))
	defer srv.Close()
	ctx, stdout, _ := testCtx(srv.URL)
	if rc := Documents(ctx, []string{"revisions", "doc_x"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	out := stdout.String()
	if !strings.Contains(out, "rev_a") || !strings.Contains(out, "rev_b") {
		t.Errorf("missing revision ids in table: %q", out)
	}
	if !strings.Contains(out, "highlight pg 3") {
		t.Errorf("missing revision message: %q", out)
	}
}

func TestDocuments_DownloadCurrentRevision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/editor/v1/documents/doc_x/download" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4\n%fake-bytes"))
	}))
	defer srv.Close()
	ctx, stdout, _ := testCtx(srv.URL)
	if rc := Documents(ctx, []string{"download", "doc_x"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.HasPrefix(stdout.String(), "%PDF-1.4") {
		t.Errorf("expected PDF bytes on stdout; got %q", stdout.String())
	}
}

func TestDocuments_DownloadSpecificRevision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "/api/editor/v1/documents/doc_x/revisions/rev_b/download"
		if r.URL.Path != want {
			t.Errorf("path = %s, want %s", r.URL.Path, want)
		}
		_, _ = w.Write([]byte("%PDF-1.4\n%rev-b-bytes"))
	}))
	defer srv.Close()
	ctx, stdout, _ := testCtx(srv.URL)
	if rc := Documents(ctx, []string{"download", "--rev", "rev_b", "doc_x"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(stdout.String(), "rev-b-bytes") {
		t.Errorf("expected rev-b bytes on stdout; got %q", stdout.String())
	}
}

func TestDocuments_EditPostsOpsFromFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/editor/v1/documents/doc_x/edit" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		// Body must wrap the ops in {ops:[...]}.
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		body := buf.String()
		if !strings.Contains(body, `"page.rotate"`) {
			t.Errorf("ops not forwarded: body=%s", body)
		}
		if !strings.Contains(body, `"ops"`) {
			t.Errorf("body missing top-level `ops`: %s", body)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"revId":"rev_new"}}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	opsPath := dir + "/ops.json"
	if err := writeTempFile(opsPath, `[{"type":"page.rotate","page":1,"rotation":90}]`); err != nil {
		t.Fatal(err)
	}
	ctx, stdout, _ := testCtx(srv.URL)
	if rc := Documents(ctx, []string{"edit", "--ops-file", opsPath, "doc_x"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(stdout.String(), "rev_new") {
		t.Errorf("expected revId on stdout: %q", stdout.String())
	}
}

func TestDocuments_EditRejectsEmptyOpsArray(t *testing.T) {
	dir := t.TempDir()
	opsPath := dir + "/ops.json"
	if err := writeTempFile(opsPath, `[]`); err != nil {
		t.Fatal(err)
	}
	ctx, _, stderr := testCtx("")
	if rc := Documents(ctx, []string{"edit", "--ops-file", opsPath, "doc_x"}); rc != 2 {
		t.Errorf("rc = %d, want 2 (empty ops)", rc)
	}
	if !strings.Contains(stderr.String(), "ops array is empty") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestDocuments_EditRejectsBadJSON(t *testing.T) {
	dir := t.TempDir()
	opsPath := dir + "/ops.json"
	if err := writeTempFile(opsPath, `not-json`); err != nil {
		t.Fatal(err)
	}
	ctx, _, stderr := testCtx("")
	if rc := Documents(ctx, []string{"edit", "--ops-file", opsPath, "doc_x"}); rc != 2 {
		t.Errorf("rc = %d, want 2 (bad json)", rc)
	}
	if !strings.Contains(stderr.String(), "must be a JSON array") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestDocuments_DeleteRequiresYes(t *testing.T) {
	ctx, _, stderr := testCtx("")
	if rc := Documents(ctx, []string{"delete", "doc_x"}); rc != 2 {
		t.Errorf("rc = %d, want 2 (missing --yes)", rc)
	}
	if !strings.Contains(stderr.String(), "--yes") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestDocuments_DeleteWithYesCallsAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/api/editor/v1/documents/doc_x" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	ctx, stdout, _ := testCtx(srv.URL)
	if rc := Documents(ctx, []string{"delete", "--yes", "doc_x"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(stdout.String(), "Deleted doc_x") {
		t.Errorf("expected confirmation: %q", stdout.String())
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "—"},
		{-5, "—"},
		{500, "500B"},
		{2048, "2.0KB"},
		{3 * 1024 * 1024, "3.0MB"},
		{int64(2.5 * 1024 * 1024 * 1024), "2.5GB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func writeTempFile(path, contents string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(contents); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
