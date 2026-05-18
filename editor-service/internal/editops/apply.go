package editops

import (
	"encoding/json"
	"errors"
	"fmt"

	"editor-service/internal/pdfedit"
)

// OpType enumerates the wire-level op identifiers. Values mirror the
// sPDOM op contract in plan §5.5 — keep the strings stable; they are
// part of the public API.
type OpType string

const (
	PageRotate    OpType = "page.rotate"
	PageDelete    OpType = "page.delete"
	PageInsert    OpType = "page.insert"
	AnnotationAdd OpType = "annotation.add"

	// Reserved — translators not yet implemented; emit ErrUnknownOp.
	// Listed here so the constant block reads as the op roadmap rather
	// than the v0 surface.
	TextReplace   OpType = "text.replace"
	TextInsert    OpType = "text.insert"
	TextDelete    OpType = "text.delete"
	RedactApply   OpType = "redact.apply"
	TableCellEdit OpType = "table.cell.edit"
)

// Op is one wire-format edit operation. The shape is intentionally
// `{type, …op-specific fields}` flat (not nested under "args") so it
// matches the OpenAPI example in docs/developer/swagger/openapi.yaml.
//
// Validation lives per-op in the apply switch — we don't pre-validate
// here because each op type has different required fields.
type Op struct {
	Type OpType `json:"type"`

	// page.rotate fields
	Page     int `json:"page,omitempty"`     // 1-indexed page number
	Rotation int `json:"rotation,omitempty"` // 0 / 90 / 180 / 270

	// page.insert fields
	//
	// AfterPage is the 1-indexed page after which the new blank page
	// is inserted. 0 means "insert before the first page". Range:
	// [0, currentPageCount]. We keep it as a pointer so a missing JSON
	// field is distinguishable from a JSON literal `0`; the latter is
	// a legitimate "insert at start" request.
	AfterPage *int `json:"afterPage,omitempty"`

	// annotation.add fields
	//
	// Kind selects the annotation shape. Rect is [x0, y0, x1, y1] in
	// PDF user-space points (origin bottom-left). Color is optional
	// RGB in [0,1]^3; nil falls back to the per-kind default.
	// Contents is optional review text shown on hover.
	Kind     string    `json:"kind,omitempty"`
	Rect     []float64 `json:"rect,omitempty"`
	Color    []float64 `json:"color,omitempty"`
	Contents string    `json:"contents,omitempty"`

	// kind="freehand" only — each entry is one stroke as a flat
	// [x1,y1,x2,y2,…] slice in PDF user-space points. Rect is
	// auto-computed from the strokes' bounding box, so callers can
	// omit it on freehand ops.
	Strokes [][]float64 `json:"strokes,omitempty"`

	// kind="callout" only — [x, y] point the callout line points at
	// (typically outside `rect`). Pointer-typed so a JSON `[0, 0]`
	// is distinguishable from a missing field.
	Anchor *[2]float64 `json:"anchor,omitempty"`

	// text.replace fields. `Find` is the exact literal text the
	// op should locate on the page; `Replace` is what it becomes.
	// Both required for text.replace; ignored by every other op.
	Find    string `json:"find,omitempty"`
	Replace string `json:"replace,omitempty"`

	// table.cell.edit fields. `Text` is the new content the
	// cell at `Rect` should hold. The op scrubs every text-show
	// op overlapping `Rect` and re-emits `Text` at the original
	// anchor in the original font. Required for table.cell.edit;
	// ignored by every other op.
	//
	// table.cell.edit supports a second addressing form
	// `{region, row, col, text}` that runs
	// spdom.DetectTableGrid over `region` and snaps to the
	// matching cell — usable when the caller has the table's
	// bbox but doesn't want to compute per-cell rects. The
	// rect form takes precedence when both are supplied.
	//
	// text.insert reuses `Text` for the content to draw.
	Text string `json:"text,omitempty"`

	// table.cell.edit (coord form) fields. `Region` is the
	// table's overall bounding box [x0, y0, x1, y1] in PDF
	// user space; `Row` / `Col` select the target cell within
	// the detected grid (both 0-indexed; row 0 is the top
	// row, col 0 the leftmost column). Pointer-typed so a
	// JSON `0` is distinguishable from "missing" — (0, 0) is
	// a legal top-left cell selection.
	Region []float64 `json:"region,omitempty"`
	Row    *int      `json:"row,omitempty"`
	Col    *int      `json:"col,omitempty"`

	// text.insert fields. Pointer-typed so a JSON `0` is
	// distinguishable from "missing" — (0, 0) is a legal anchor
	// at the bottom-left corner of the page. `Font` is the
	// resource name (e.g. "F1") that must already exist in the
	// page's /Resources/Font dict — v0 doesn't add new fonts.
	// `SizePt` is the rendered point size; must be > 0.
	X      *float64 `json:"x,omitempty"`
	Y      *float64 `json:"y,omitempty"`
	Font   string   `json:"font,omitempty"`
	SizePt float64  `json:"sizePt,omitempty"`
}

// Sentinel errors. Tests pattern-match on these so callers (HTTP
// handler today, gRPC tomorrow) can map them to status codes without
// scraping error strings.
var (
	ErrNoOps       = errors.New("editops: no ops provided")
	ErrUnknownOp   = errors.New("editops: unknown op type")
	ErrInvalidArgs = errors.New("editops: invalid op arguments")
)

// MaxOpsPerRequest caps how many ops one Apply call processes. The
// limit is conservative — most realistic /edit requests carry one or
// two ops, and a huge batch would compound latency (each op re-parses
// the PDF via pdfcpu). Pick a number high enough that no honest UX
// flow hits it (Acrobat-style "highlight every match" pre-batches at
// the client) but low enough that an accidental N-thousand request
// fails fast.
const MaxOpsPerRequest = 64

// Apply runs `ops` against `original` and returns the new PDF bytes.
//
// Multi-op semantics: ops run in order, each chaining onto the bytes
// produced by the previous op. The result is `original` plus N
// appended incremental sections (one per op). Every standard PDF
// reader follows /Prev through stacked sections — the final state is
// the same as if the ops had been merged into a single section, but
// the per-op section boundaries make per-op audit + rollback easier
// for follow-up work (e.g., "undo the third annotation in this
// revision").
//
// Errors map cleanly to HTTP status codes at the handler layer:
//   - ErrNoOps, ErrInvalidArgs → 400
//   - ErrUnknownOp             → 400 (caller sent a type we don't
//     recognise / haven't shipped yet)
//   - anything else (the underlying pdfedit / pdfwriter error) → 500
//
// Errors from ops past the first are wrapped with the op index so
// the caller can point the user at the offending element of their
// request body.
func Apply(ops []Op, original []byte) ([]byte, error) {
	if len(ops) == 0 {
		return nil, ErrNoOps
	}
	if len(ops) > MaxOpsPerRequest {
		return nil, fmt.Errorf("%w: %d ops exceeds limit of %d", ErrInvalidArgs, len(ops), MaxOpsPerRequest)
	}
	current := original
	for i, op := range ops {
		next, err := applyOne(op, current)
		if err != nil {
			// Keep sentinel-wrap for errors.Is matching at the handler
			// layer, but prefix with the op index so multi-op error
			// messages point at the right element.
			return nil, fmt.Errorf("ops[%d]: %w", i, err)
		}
		current = next
	}
	return current, nil
}

// applyOne dispatches a single op to its pdfedit translator. Pulled
// out of Apply so the multi-op loop has one indirection per step and
// each translator's failure surface is identical to v0's single-op
// behavior.
func applyOne(op Op, original []byte) ([]byte, error) {
	switch op.Type {
	case PageRotate:
		if op.Page < 1 {
			return nil, fmt.Errorf("%w: page.rotate requires page >= 1, got %d", ErrInvalidArgs, op.Page)
		}
		// pdfedit.RotatePage validates the rotation; surface its error
		// as ErrInvalidArgs so the handler returns 400 on bad rotation
		// rather than 500.
		out, err := pdfedit.RotatePage(original, op.Page, op.Rotation)
		if err != nil {
			return nil, classifyPdfeditErr(err)
		}
		return out, nil

	case PageDelete:
		if op.Page < 1 {
			return nil, fmt.Errorf("%w: page.delete requires page >= 1, got %d", ErrInvalidArgs, op.Page)
		}
		out, err := pdfedit.DeletePage(original, op.Page)
		if err != nil {
			return nil, classifyPdfeditErr(err)
		}
		return out, nil

	case PageInsert:
		if op.AfterPage == nil {
			return nil, fmt.Errorf("%w: page.insert requires `afterPage` (0 = insert before first page)", ErrInvalidArgs)
		}
		if *op.AfterPage < 0 {
			return nil, fmt.Errorf("%w: page.insert afterPage %d must be >= 0", ErrInvalidArgs, *op.AfterPage)
		}
		out, err := pdfedit.InsertBlankPage(original, *op.AfterPage)
		if err != nil {
			return nil, classifyPdfeditErr(err)
		}
		return out, nil

	case AnnotationAdd:
		return applyAnnotationAdd(op, original)

	case TextReplace:
		if op.Page < 1 {
			return nil, fmt.Errorf("%w: text.replace requires page >= 1, got %d", ErrInvalidArgs, op.Page)
		}
		if op.Find == "" {
			return nil, fmt.Errorf("%w: text.replace requires non-empty `find`", ErrInvalidArgs)
		}
		out, err := pdfedit.ReplaceText(original, op.Page, op.Find, op.Replace)
		if err != nil {
			return nil, classifyTextReplaceErr(err)
		}
		return out, nil

	case RedactApply:
		if op.Page < 1 {
			return nil, fmt.Errorf("%w: redact.apply requires page >= 1, got %d", ErrInvalidArgs, op.Page)
		}
		if len(op.Rect) != 4 {
			return nil, fmt.Errorf("%w: redact.apply requires rect [x0,y0,x1,y1] (got %d values)", ErrInvalidArgs, len(op.Rect))
		}
		rect := pdfedit.AnnotRect{X0: op.Rect[0], Y0: op.Rect[1], X1: op.Rect[2], Y1: op.Rect[3]}
		out, err := pdfedit.RedactArea(original, op.Page, rect)
		if err != nil {
			// RedactArea returns the same content-stream sentinels
			// as ReplaceText (single-stream + uncompressed gates),
			// so reuse that classifier.
			return nil, classifyTextReplaceErr(err)
		}
		return out, nil

	case TableCellEdit:
		if op.Page < 1 {
			return nil, fmt.Errorf("%w: table.cell.edit requires page >= 1, got %d", ErrInvalidArgs, op.Page)
		}
		if op.Text == "" {
			return nil, fmt.Errorf("%w: table.cell.edit requires non-empty `text`", ErrInvalidArgs)
		}
		// Two valid addressing forms:
		//   - Rect form: `{page, rect, text}`. Caller knows
		//     the exact cell rect.
		//   - Coord form: `{page, region, row, col, text}`.
		//     Caller knows the table's bbox + cell index;
		//     the dispatcher runs spdom.DetectTableGrid + snaps
		//     to the matching cell.
		// Rect form takes precedence when both are present —
		// it's the more precise input and the older contract.
		switch {
		case len(op.Rect) == 4:
			rect := pdfedit.AnnotRect{X0: op.Rect[0], Y0: op.Rect[1], X1: op.Rect[2], Y1: op.Rect[3]}
			out, err := pdfedit.EditTableCell(original, op.Page, rect, op.Text)
			if err != nil {
				return nil, classifyTableCellEditErr(err)
			}
			return out, nil
		case len(op.Region) == 4 && op.Row != nil && op.Col != nil:
			region := pdfedit.AnnotRect{X0: op.Region[0], Y0: op.Region[1], X1: op.Region[2], Y1: op.Region[3]}
			out, err := pdfedit.EditTableCellByCoord(original, op.Page, region, *op.Row, *op.Col, op.Text)
			if err != nil {
				return nil, classifyTableCellEditErr(err)
			}
			return out, nil
		case len(op.Rect) > 0:
			return nil, fmt.Errorf("%w: table.cell.edit requires rect [x0,y0,x1,y1] (got %d values)", ErrInvalidArgs, len(op.Rect))
		default:
			return nil, fmt.Errorf("%w: table.cell.edit requires either `rect` or {`region`, `row`, `col`}", ErrInvalidArgs)
		}

	case TextInsert:
		if op.Page < 1 {
			return nil, fmt.Errorf("%w: text.insert requires page >= 1, got %d", ErrInvalidArgs, op.Page)
		}
		if op.X == nil || op.Y == nil {
			return nil, fmt.Errorf("%w: text.insert requires both `x` and `y` (PDF user-space points)", ErrInvalidArgs)
		}
		if op.Text == "" {
			return nil, fmt.Errorf("%w: text.insert requires non-empty `text`", ErrInvalidArgs)
		}
		if op.Font == "" {
			return nil, fmt.Errorf("%w: text.insert requires `font` (a resource name like \"F1\" that exists in the page's /Resources/Font)", ErrInvalidArgs)
		}
		if op.SizePt <= 0 {
			return nil, fmt.Errorf("%w: text.insert requires sizePt > 0, got %v", ErrInvalidArgs, op.SizePt)
		}
		out, err := pdfedit.InsertText(original, op.Page, *op.X, *op.Y, op.Text, op.Font, op.SizePt)
		if err != nil {
			return nil, classifyTextInsertErr(err)
		}
		return out, nil

	case TextDelete:
		if op.Page < 1 {
			return nil, fmt.Errorf("%w: text.delete requires page >= 1, got %d", ErrInvalidArgs, op.Page)
		}
		if len(op.Rect) != 4 {
			return nil, fmt.Errorf("%w: text.delete requires rect [x0,y0,x1,y1] (got %d values)", ErrInvalidArgs, len(op.Rect))
		}
		rect := pdfedit.AnnotRect{X0: op.Rect[0], Y0: op.Rect[1], X1: op.Rect[2], Y1: op.Rect[3]}
		out, err := pdfedit.DeleteTextInRect(original, op.Page, rect)
		if err != nil {
			return nil, classifyTextDeleteErr(err)
		}
		return out, nil

	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownOp, op.Type)
	}
}

// applyAnnotationAdd validates the annotation.add wire fields and
// dispatches to the matching pdfedit translator.
//
// Rect/color/contents are common across every kind. Per-kind
// extras (Strokes for freehand, Anchor for callout) are validated
// inside the switch so the error points at the offending field
// name.
func applyAnnotationAdd(op Op, original []byte) ([]byte, error) {
	if op.Page < 1 {
		return nil, fmt.Errorf("%w: annotation.add requires page >= 1, got %d", ErrInvalidArgs, op.Page)
	}

	var color *pdfedit.AnnotColor
	if len(op.Color) > 0 {
		if len(op.Color) != 3 {
			return nil, fmt.Errorf("%w: annotation.add color must have 3 channels [R,G,B] (got %d)", ErrInvalidArgs, len(op.Color))
		}
		color = &pdfedit.AnnotColor{R: op.Color[0], G: op.Color[1], B: op.Color[2]}
	}

	kind := op.Kind
	if kind == "" {
		kind = "highlight"
	}

	// freehand and callout take different payload shapes from the
	// rect-only kinds, so we branch before the rect-required path.
	switch kind {
	case "freehand":
		if len(op.Strokes) == 0 {
			return nil, fmt.Errorf("%w: annotation.add kind=freehand requires non-empty `strokes`", ErrInvalidArgs)
		}
		out, err := pdfedit.AddInkAnnotation(original, op.Page, op.Strokes, color, op.Contents)
		if err != nil {
			return nil, classifyPdfeditErr(err)
		}
		return out, nil

	case "callout":
		if op.Anchor == nil {
			return nil, fmt.Errorf("%w: annotation.add kind=callout requires `anchor: [x, y]`", ErrInvalidArgs)
		}
		if len(op.Rect) != 4 {
			return nil, fmt.Errorf("%w: annotation.add kind=callout requires rect [x0,y0,x1,y1] (got %d values)", ErrInvalidArgs, len(op.Rect))
		}
		rect := pdfedit.AnnotRect{X0: op.Rect[0], Y0: op.Rect[1], X1: op.Rect[2], Y1: op.Rect[3]}
		out, err := pdfedit.AddCalloutAnnotation(original, op.Page, rect, *op.Anchor, color, op.Contents)
		if err != nil {
			return nil, classifyPdfeditErr(err)
		}
		return out, nil
	}

	// Rect-only kinds (the text-markup family + square + sticky).
	if len(op.Rect) != 4 {
		return nil, fmt.Errorf("%w: annotation.add requires rect [x0,y0,x1,y1] (got %d values)", ErrInvalidArgs, len(op.Rect))
	}
	rect := pdfedit.AnnotRect{X0: op.Rect[0], Y0: op.Rect[1], X1: op.Rect[2], Y1: op.Rect[3]}

	var ak pdfedit.AnnotKind
	switch kind {
	case "highlight":
		ak = pdfedit.AnnotHighlight
	case "underline":
		ak = pdfedit.AnnotUnderline
	case "strikeout":
		ak = pdfedit.AnnotStrikeOut
	case "squiggly":
		ak = pdfedit.AnnotSquiggly
	case "square":
		ak = pdfedit.AnnotSquare
	case "sticky":
		ak = pdfedit.AnnotSticky
	default:
		return nil, fmt.Errorf("%w: annotation.add kind %q is not supported", ErrUnknownOp, kind)
	}
	out, err := pdfedit.AddAnnotation(original, ak, op.Page, rect, color, op.Contents)
	if err != nil {
		return nil, classifyPdfeditErr(err)
	}
	return out, nil
}

// classifyPdfeditErr decides whether an error from pdfedit should be
// surfaced as bad-input (400) or as a server failure (500). pdfedit
// uses fmt.Errorf-wrapped errors so we string-match on a small number
// of known prefixes — fragile but the surface is small enough that
// the tradeoff is worth keeping the pdfedit API stringly-typed.
//
// Recognised bad-input markers:
//   - "rotation %d not allowed"           — rotate.go
//   - "pageNum %d out of range"           — rotate.go, annotation.go, page.go
//   - "afterPage %d out of range"         — page.go (InsertBlankPage)
//   - "pageNum %d must be >= 1"           — annotation.go, page.go
//   - "afterPage %d must be >= 0"         — page.go (InsertBlankPage)
//   - "degenerate annotation rect"        — annotation.go
//   - "refusing to delete the last page"  — page.go
//   - "cannot insert into a document with no pages" — page.go
//   - "read source PDF"                   — all translators (garbage input)
//
// Anything else is treated as a server-side fault.
func classifyPdfeditErr(err error) error {
	msg := err.Error()
	if contains(msg, "rotation") && contains(msg, "not allowed") {
		return fmt.Errorf("%w: %s", ErrInvalidArgs, msg)
	}
	if contains(msg, "out of range") {
		return fmt.Errorf("%w: %s", ErrInvalidArgs, msg)
	}
	if contains(msg, "must be >= 1") {
		return fmt.Errorf("%w: %s", ErrInvalidArgs, msg)
	}
	if contains(msg, "degenerate") {
		return fmt.Errorf("%w: %s", ErrInvalidArgs, msg)
	}
	if contains(msg, "last page") {
		return fmt.Errorf("%w: %s", ErrInvalidArgs, msg)
	}
	if contains(msg, "cannot insert into a document with no pages") {
		return fmt.Errorf("%w: %s", ErrInvalidArgs, msg)
	}
	if contains(msg, "read source PDF") {
		return fmt.Errorf("%w: source bytes are not a parseable PDF", ErrInvalidArgs)
	}
	return err
}

// classifyTextReplaceErr maps text.replace's specific sentinels
// to the editops error set. Unlike classifyPdfeditErr (which
// matches on string prefixes for legacy reasons), pdfedit's
// text-replace path exposes typed sentinels via errors.Is, so we
// switch on those directly. Anything unrecognised falls through
// as a 500.
func classifyTextReplaceErr(err error) error {
	switch {
	case errors.Is(err, pdfedit.ErrTextNotFound),
		errors.Is(err, pdfedit.ErrContentsNotInline),
		errors.Is(err, pdfedit.ErrStreamFiltered):
		return fmt.Errorf("%w: %s", ErrInvalidArgs, err.Error())
	}
	// Fall back to the page-range / parse-error checks shared
	// with the rest of pdfedit.
	return classifyPdfeditErr(err)
}

// classifyTableCellEditErr maps table.cell.edit's typed sentinels
// to the editops error set. ErrCellEmpty surfaces as 400 — the
// caller's rect doesn't overlap any text on the page (typically
// wrong cell coordinates or an empty cell). The coord-form
// sentinels (ErrGridNotDetected, ErrCoordOutOfRange) are
// likewise 400 — both indicate caller-side mistakes (wrong
// region, out-of-bounds row/col) that retry won't fix. The
// stream-shape sentinels (ErrContentsNotInline, ErrStreamFiltered)
// and the shared page-range / parse-error checks come along
// for free.
func classifyTableCellEditErr(err error) error {
	switch {
	case errors.Is(err, pdfedit.ErrCellEmpty),
		errors.Is(err, pdfedit.ErrGridNotDetected),
		errors.Is(err, pdfedit.ErrCoordOutOfRange),
		errors.Is(err, pdfedit.ErrContentsNotInline),
		errors.Is(err, pdfedit.ErrStreamFiltered):
		return fmt.Errorf("%w: %s", ErrInvalidArgs, err.Error())
	}
	return classifyPdfeditErr(err)
}

// classifyTextInsertErr maps text.insert's typed sentinels to the
// editops error set. Empty-text / missing-font / non-positive-size
// already get pre-validated at the dispatcher (so they return
// ErrInvalidArgs before the translator runs), but the sentinels
// are still wired here for defence in depth and direct-translator
// callers.
func classifyTextInsertErr(err error) error {
	switch {
	case errors.Is(err, pdfedit.ErrEmptyTextInsert),
		errors.Is(err, pdfedit.ErrEmptyFontResource),
		errors.Is(err, pdfedit.ErrNonPositiveFontSize),
		errors.Is(err, pdfedit.ErrContentsNotInline),
		errors.Is(err, pdfedit.ErrStreamFiltered):
		return fmt.Errorf("%w: %s", ErrInvalidArgs, err.Error())
	}
	return classifyPdfeditErr(err)
}

// classifyTextDeleteErr maps text.delete's typed sentinels to the
// editops error set. ErrTextDeleteNoOverlap surfaces as 400 — the
// caller's rect doesn't overlap any text on the page (mirrors
// table.cell.edit's ErrCellEmpty path).
func classifyTextDeleteErr(err error) error {
	switch {
	case errors.Is(err, pdfedit.ErrTextDeleteNoOverlap),
		errors.Is(err, pdfedit.ErrContentsNotInline),
		errors.Is(err, pdfedit.ErrStreamFiltered):
		return fmt.Errorf("%w: %s", ErrInvalidArgs, err.Error())
	}
	return classifyPdfeditErr(err)
}

// contains is strings.Contains in a tiny wrapper — kept here to avoid
// pulling the strings import for one use, and to make the classify
// helper read cleanly.
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// ParseRequest decodes the wire format `{ops:[…], message?:string}` and
// returns the ops slice. Centralised here so the HTTP handler doesn't
// have to know the JSON shape — it just hands us the request body.
type Request struct {
	Ops     []Op   `json:"ops"`
	Message string `json:"message,omitempty"`
}

// ParseRequest is a thin wrapper over json.Unmarshal — exists mostly
// to give the handler a single import surface (handlers depend on
// editops, not on the wire JSON shape).
func ParseRequest(body []byte) (Request, error) {
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return req, fmt.Errorf("%w: %v", ErrInvalidArgs, err)
	}
	return req, nil
}
