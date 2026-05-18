package fyredocs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fyredocs/fyredocs-go"
)

// newClient builds a Client pointed at the supplied httptest
// server. Used across every namespace test so the table-driven
// pattern stays tight.
func newClient(t *testing.T, srv *httptest.Server) *fyredocs.Client {
	t.Helper()
	return fyredocs.New(fyredocs.Options{
		APIKey:  "fyr_test_key",
		BaseURL: srv.URL,
	})
}

// ---- Client + Request --------------------------------------------------

func TestRequest_SendsAuthorizationHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"success":true,"data":{"ok":true}}`))
	}))
	defer srv.Close()
	c := newClient(t, srv)
	var out map[string]bool
	if err := c.Request(context.Background(), "/anything", fyredocs.RequestOptions{Out: &out}); err != nil {
		t.Fatalf("Request: %v", err)
	}
	if gotAuth != "Bearer fyr_test_key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer fyr_test_key")
	}
	if !out["ok"] {
		t.Errorf("envelope unwrap failed: %v", out)
	}
}

func TestRequest_MapsEnvelopeErrorToFyredocsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"success":false,"error":{"code":"INVALID_INPUT","details":"pageNum must be >= 1"}}`))
	}))
	defer srv.Close()
	c := newClient(t, srv)
	err := c.Request(context.Background(), "/x", fyredocs.RequestOptions{})
	var apiErr *fyredocs.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *fyredocs.Error", err)
	}
	if apiErr.Status != 400 || apiErr.Code != "INVALID_INPUT" {
		t.Errorf("Status/Code = %d %q, want 400 INVALID_INPUT", apiErr.Status, apiErr.Code)
	}
	if !strings.Contains(apiErr.Message, "pageNum") {
		t.Errorf("Message = %q, want a hint about pageNum", apiErr.Message)
	}
}

func TestRequest_NetworkErrorReturnsZeroStatus(t *testing.T) {
	c := fyredocs.New(fyredocs.Options{
		APIKey:  "fyr_test",
		BaseURL: "http://127.0.0.1:1", // closed port
	})
	err := c.Request(context.Background(), "/x", fyredocs.RequestOptions{})
	var apiErr *fyredocs.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *fyredocs.Error, got %v", err)
	}
	if apiErr.Status != 0 || apiErr.Code != "NETWORK" {
		t.Errorf("Status/Code = %d %q, want 0 NETWORK", apiErr.Status, apiErr.Code)
	}
}

func TestRequest_SetsUserAgentDefaultAndOverride(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	// Default UA.
	c := newClient(t, srv)
	_ = c.Request(context.Background(), "/x", fyredocs.RequestOptions{})
	if gotUA != "fyredocs-go" {
		t.Errorf("default UA = %q, want fyredocs-go", gotUA)
	}

	// Override.
	c2 := fyredocs.New(fyredocs.Options{
		APIKey:    "fyr_test",
		BaseURL:   srv.URL,
		UserAgent: "myapp/1.2.3",
	})
	_ = c2.Request(context.Background(), "/x", fyredocs.RequestOptions{})
	if gotUA != "myapp/1.2.3" {
		t.Errorf("override UA = %q, want myapp/1.2.3", gotUA)
	}
}

func TestRequest_NoBodyOn204(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := newClient(t, srv)
	if err := c.Request(context.Background(), "/x", fyredocs.RequestOptions{Method: http.MethodPost}); err != nil {
		t.Errorf("204 should be a clean nil; got %v", err)
	}
}

// ---- APIKeys ----------------------------------------------------------

func TestAPIKeys_List(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/api-keys" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("revoked") != "true" {
			t.Errorf("revoked query = %q, want true", r.URL.Query().Get("revoked"))
		}
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"key_1","name":"CI","environment":"live","keyPrefix":"fyr_live_abc","createdAt":"2026-01-01"}
		]}`))
	}))
	defer srv.Close()
	c := newClient(t, srv)
	keys, err := c.APIKeys.List(context.Background(), &fyredocs.ListAPIKeysOptions{Revoked: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 || keys[0].ID != "key_1" || keys[0].Environment != fyredocs.APIKeyLive {
		t.Errorf("got %+v", keys)
	}
}

func TestAPIKeys_IssueAndRevoke(t *testing.T) {
	calls := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/auth/api-keys":
			body, _ := io.ReadAll(r.Body)
			var got fyredocs.IssueAPIKeyRequest
			_ = json.Unmarshal(body, &got)
			if got.Name != "ops" || got.Environment != fyredocs.APIKeyTest {
				t.Errorf("issue body = %+v", got)
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{
				"key":{"id":"key_new","name":"ops","environment":"test","keyPrefix":"fyr_test_x","createdAt":"2026-05-16"},
				"plaintext":"fyr_test_PLAIN"
			}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/auth/api-keys/key_new/revoke":
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv)
	resp, err := c.APIKeys.Issue(context.Background(), fyredocs.IssueAPIKeyRequest{
		Name: "ops", Environment: fyredocs.APIKeyTest,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if resp.Plaintext != "fyr_test_PLAIN" || resp.Key.ID != "key_new" {
		t.Errorf("got %+v", resp)
	}
	if err := c.APIKeys.Revoke(context.Background(), "key_new"); err != nil {
		t.Errorf("Revoke: %v", err)
	}
	if len(calls) != 2 {
		t.Errorf("expected 2 calls, got %v", calls)
	}
}

// ---- Billing ----------------------------------------------------------

func TestBilling_Me(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/billing/v1/billing/me" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{
			"plan":{"code":"pro","name":"Pro","description":"d","monthlyPriceCents":1500,"perSeat":false,"selfServe":true,"limits":{}},
			"subscription":{"id":"sub_1","userId":"u","planCode":"pro","status":"active","seats":1,"currentPeriodStart":"a","currentPeriodEnd":"b","createdAt":"c","updatedAt":"d"}
		}}`))
	}))
	defer srv.Close()
	c := newClient(t, srv)
	me, err := c.Billing.Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if me.Plan.Code != "pro" {
		t.Errorf("plan = %+v", me.Plan)
	}
	if me.Subscription == nil || me.Subscription.Status != "active" {
		t.Errorf("subscription = %+v", me.Subscription)
	}
}

func TestBilling_Plans_UnwrapsPlansKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"plans":[
			{"code":"free","name":"Free","description":"","monthlyPriceCents":0,"perSeat":false,"selfServe":true,"limits":{}},
			{"code":"pro","name":"Pro","description":"","monthlyPriceCents":1500,"perSeat":false,"selfServe":true,"limits":{}}
		]}}`))
	}))
	defer srv.Close()
	c := newClient(t, srv)
	plans, err := c.Billing.Plans(context.Background())
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	if len(plans) != 2 || plans[1].Code != "pro" {
		t.Errorf("plans = %+v", plans)
	}
}

func TestBilling_Subscribe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got fyredocs.SubscribeRequest
		_ = json.Unmarshal(body, &got)
		if got.PlanCode != "teams" || got.Seats != 5 {
			t.Errorf("body = %+v", got)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{
			"id":"sub_2","userId":"u","planCode":"teams","status":"active","seats":5,
			"currentPeriodStart":"a","currentPeriodEnd":"b","createdAt":"c","updatedAt":"d"
		}}`))
	}))
	defer srv.Close()
	c := newClient(t, srv)
	sub, err := c.Billing.Subscribe(context.Background(), fyredocs.SubscribeRequest{
		PlanCode: "teams", Seats: 5,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if sub.PlanCode != "teams" || sub.Seats != 5 {
		t.Errorf("sub = %+v", sub)
	}
}

// ---- Usage -----------------------------------------------------------

func TestUsage_Me_DefaultsToCurrentPeriod(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("period") != "" {
			t.Errorf("period query should be omitted; got %q", r.URL.Query().Get("period"))
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{
			"userId":"u","period":"2026-05","items":[
				{"eventType":"editor.edit.text.replace","unit":"calls","totalQuantity":3,"eventCount":3}
			]}}`))
	}))
	defer srv.Close()
	c := newClient(t, srv)
	rollup, err := c.Usage.Me(context.Background(), nil)
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if rollup.Period != "2026-05" || len(rollup.Items) != 1 {
		t.Errorf("rollup = %+v", rollup)
	}
}

func TestUsage_Me_PassesPeriodQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("period") != "2026-04" {
			t.Errorf("period = %q", r.URL.Query().Get("period"))
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"userId":"u","period":"2026-04","items":[]}}`))
	}))
	defer srv.Close()
	c := newClient(t, srv)
	_, err := c.Usage.Me(context.Background(), &fyredocs.UsageMeOptions{Period: "2026-04"})
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
}

// ---- Documents -------------------------------------------------------

func TestDocuments_Get(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/editor/v1/documents/doc_abc" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{
			"id":"doc_abc","title":"Spec","pageCount":7,"currentRevId":"rev_xyz","status":"ready"
		}}`))
	}))
	defer srv.Close()
	c := newClient(t, srv)
	doc, err := c.Documents.Get(context.Background(), "doc_abc")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if doc.ID != "doc_abc" || doc.CurrentRevID != "rev_xyz" {
		t.Errorf("doc = %+v", doc)
	}
}

func TestDocuments_List_PassesPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "2" || r.URL.Query().Get("limit") != "10" {
			t.Errorf("query = %v", r.URL.Query())
		}
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"doc_1","title":"A","status":"ready"}
		]}`))
	}))
	defer srv.Close()
	c := newClient(t, srv)
	docs, err := c.Documents.List(context.Background(), &fyredocs.ListDocumentsOptions{Page: 2, Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(docs) != 1 {
		t.Errorf("len = %d", len(docs))
	}
}

func TestDocuments_Revisions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/editor/v1/documents/doc_x/revisions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"rev_a","documentId":"doc_x"},
			{"id":"rev_b","documentId":"doc_x","parentRevId":"rev_a"}
		]}`))
	}))
	defer srv.Close()
	c := newClient(t, srv)
	revs, err := c.Documents.Revisions(context.Background(), "doc_x")
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if len(revs) != 2 || revs[1].ParentRevID != "rev_a" {
		t.Errorf("revs = %+v", revs)
	}
}

func TestDocuments_Edit_PostsOpsAndReturnsRev(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/editor/v1/documents/doc_x/edit" || r.Method != http.MethodPost {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var got fyredocs.EditRequest
		_ = json.Unmarshal(body, &got)
		if len(got.Ops) != 1 || got.Ops[0].Type != fyredocs.OpPageRotate || got.Ops[0].Rotation != 90 {
			t.Errorf("body = %+v", got)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{
			"id":"rev_new","documentId":"doc_x","createdAt":"2026-05-16"
		}}`))
	}))
	defer srv.Close()
	c := newClient(t, srv)
	rev, err := c.Documents.Edit(context.Background(), "doc_x", fyredocs.EditRequest{
		Ops: []fyredocs.EditorOp{
			{Type: fyredocs.OpPageRotate, Page: 1, Rotation: 90},
		},
	})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if rev.ID != "rev_new" {
		t.Errorf("rev = %+v", rev)
	}
}

func TestDocuments_Download_CurrentAndSpecificRevision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-" + r.URL.Path))
	}))
	defer srv.Close()
	c := newClient(t, srv)

	var current bytes.Buffer
	if err := c.Documents.Download(context.Background(), "doc_x", nil, &current); err != nil {
		t.Fatalf("Download current: %v", err)
	}
	if !strings.Contains(current.String(), "/api/editor/v1/documents/doc_x/download") {
		t.Errorf("current path missing: %q", current.String())
	}

	var rev bytes.Buffer
	if err := c.Documents.Download(context.Background(), "doc_x",
		&fyredocs.DownloadOptions{RevID: "rev_b"}, &rev); err != nil {
		t.Fatalf("Download rev: %v", err)
	}
	if !strings.Contains(rev.String(), "/api/editor/v1/documents/doc_x/revisions/rev_b/download") {
		t.Errorf("rev path missing: %q", rev.String())
	}
}

func TestDocuments_Delete(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s", r.Method)
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := newClient(t, srv)
	if err := c.Documents.Delete(context.Background(), "doc_x"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if !called {
		t.Error("server never received DELETE")
	}
}

// ---- table.cell.edit coord form ----

func TestEditorOp_TableCellEditCoordFormSerialisesCorrectly(t *testing.T) {
	// Region + Row + Col round-trip as the wire shape the
	// editor-service dispatcher expects. Pin the JSON keys
	// here so a future struct-tag rename doesn't silently
	// break the SDK's coord-form callers.
	row, col := 2, 1
	op := fyredocs.EditorOp{
		Type:   fyredocs.OpTableCellEdit,
		Page:   5,
		Region: []float64{80, 540, 520, 700},
		Row:    &row,
		Col:    &col,
		Text:   "$950",
	}
	out, err := json.Marshal(op)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	for _, want := range []string{
		`"type":"table.cell.edit"`,
		`"page":5`,
		`"region":[80,540,520,700]`,
		`"row":2`,
		`"col":1`,
		`"text":"$950"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\noutput: %s", want, got)
		}
	}
	// Coord-form ops MUST NOT serialise a stray `rect` —
	// that would flip the server to the rect form and
	// silently skip the grid detection.
	if strings.Contains(got, `"rect"`) {
		t.Errorf("coord-form op leaked rect into wire JSON: %s", got)
	}
}

func TestEditorOp_RectFormStaysRectFormWithoutRegion(t *testing.T) {
	// Sanity check the other side: rect form (no Region /
	// Row / Col) must NOT serialise any of the new fields.
	op := fyredocs.EditorOp{
		Type: fyredocs.OpTableCellEdit,
		Page: 5,
		Rect: []float64{320, 600, 480, 620},
		Text: "$1,200",
	}
	out, err := json.Marshal(op)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"rect":[320,600,480,620]`) {
		t.Errorf("expected rect array in output: %s", got)
	}
	for _, unexpected := range []string{`"region"`, `"row"`, `"col"`} {
		if strings.Contains(got, unexpected) {
			t.Errorf("rect-form op leaked %q into wire JSON: %s", unexpected, got)
		}
	}
}

func TestEditorOp_RowZeroAndColZeroAreEmittedDistinctFromMissing(t *testing.T) {
	// Row + Col are *int — the JSON `0` MUST distinguish
	// from absent. (0, 0) is a legal top-left cell
	// selection; if the marshaller dropped them the server
	// would see the rect-form branch with no rect and
	// reject.
	zero := 0
	op := fyredocs.EditorOp{
		Type:   fyredocs.OpTableCellEdit,
		Page:   1,
		Region: []float64{0, 0, 100, 100},
		Row:    &zero,
		Col:    &zero,
		Text:   "TL",
	}
	out, _ := json.Marshal(op)
	got := string(out)
	if !strings.Contains(got, `"row":0`) || !strings.Contains(got, `"col":0`) {
		t.Errorf("zero row/col must serialise as `\"row\":0` / `\"col\":0`; got %s", got)
	}
}
