package fonts

// registry is the package-private font catalog. Values are static — never
// mutated at runtime. Cap-heights are taken from each font's published AFM
// file. Substitutes are ordered highest-fidelity-first (cap-height delta
// rises down the list); consumers normally pick the first entry.
//
// References for cap-heights:
//   - PDF-14 base fonts: the 14 published Adobe Core AFMs (canonical).
//   - Croscore (Arimo / Tinos / Cousine, Apache 2.0): published by Google
//     under the Chrome OS Liberation-compatible spec; these are the
//     intended metrics-compatible drop-ins for Helvetica / Times / Courier.
var registry = map[string]Font{
	// -----------------------------------------------------------------
	// PDF-14: Helvetica family (sans-serif). CapHeight 718 per Adobe AFM.
	// -----------------------------------------------------------------
	"Helvetica": {
		PSName: "Helvetica", Family: "Helvetica", Style: StyleRegular,
		Origin: OriginPDFCore, License: LicensePDFCore, CapHeight1000: 718,
		Substitutes: []string{"Arimo"},
	},
	"Helvetica-Bold": {
		PSName: "Helvetica-Bold", Family: "Helvetica", Style: StyleBold,
		Origin: OriginPDFCore, License: LicensePDFCore, CapHeight1000: 718,
		Substitutes: []string{"Arimo-Bold"},
	},
	"Helvetica-Oblique": {
		PSName: "Helvetica-Oblique", Family: "Helvetica", Style: StyleOblique,
		Origin: OriginPDFCore, License: LicensePDFCore, CapHeight1000: 718,
		Substitutes: []string{"Arimo-Italic"},
	},
	"Helvetica-BoldOblique": {
		PSName: "Helvetica-BoldOblique", Family: "Helvetica", Style: StyleBoldOblique,
		Origin: OriginPDFCore, License: LicensePDFCore, CapHeight1000: 718,
		Substitutes: []string{"Arimo-BoldItalic"},
	},

	// -----------------------------------------------------------------
	// PDF-14: Times family (serif). CapHeight 662 per Adobe AFM.
	// -----------------------------------------------------------------
	"Times-Roman": {
		PSName: "Times-Roman", Family: "Times", Style: StyleRegular,
		Origin: OriginPDFCore, License: LicensePDFCore, CapHeight1000: 662,
		Substitutes: []string{"Tinos"},
	},
	"Times-Bold": {
		PSName: "Times-Bold", Family: "Times", Style: StyleBold,
		Origin: OriginPDFCore, License: LicensePDFCore, CapHeight1000: 662,
		Substitutes: []string{"Tinos-Bold"},
	},
	"Times-Italic": {
		PSName: "Times-Italic", Family: "Times", Style: StyleItalic,
		Origin: OriginPDFCore, License: LicensePDFCore, CapHeight1000: 662,
		Substitutes: []string{"Tinos-Italic"},
	},
	"Times-BoldItalic": {
		PSName: "Times-BoldItalic", Family: "Times", Style: StyleBoldItalic,
		Origin: OriginPDFCore, License: LicensePDFCore, CapHeight1000: 662,
		Substitutes: []string{"Tinos-BoldItalic"},
	},

	// -----------------------------------------------------------------
	// PDF-14: Courier family (monospace). CapHeight 562 per Adobe AFM.
	// -----------------------------------------------------------------
	"Courier": {
		PSName: "Courier", Family: "Courier", Style: StyleRegular,
		Origin: OriginPDFCore, License: LicensePDFCore, CapHeight1000: 562,
		Substitutes: []string{"Cousine"},
	},
	"Courier-Bold": {
		PSName: "Courier-Bold", Family: "Courier", Style: StyleBold,
		Origin: OriginPDFCore, License: LicensePDFCore, CapHeight1000: 562,
		Substitutes: []string{"Cousine-Bold"},
	},
	"Courier-Oblique": {
		PSName: "Courier-Oblique", Family: "Courier", Style: StyleOblique,
		Origin: OriginPDFCore, License: LicensePDFCore, CapHeight1000: 562,
		Substitutes: []string{"Cousine-Italic"},
	},
	"Courier-BoldOblique": {
		PSName: "Courier-BoldOblique", Family: "Courier", Style: StyleBoldOblique,
		Origin: OriginPDFCore, License: LicensePDFCore, CapHeight1000: 562,
		Substitutes: []string{"Cousine-BoldItalic"},
	},

	// -----------------------------------------------------------------
	// PDF-14: glyph-set fonts. No clean open substitutes — the writer
	// keeps the original. CapHeight is not meaningful for these.
	// -----------------------------------------------------------------
	"Symbol": {
		PSName: "Symbol", Family: "Symbol", Style: StyleRegular,
		Origin: OriginPDFCore, License: LicensePDFCore,
	},
	"ZapfDingbats": {
		PSName: "ZapfDingbats", Family: "ZapfDingbats", Style: StyleRegular,
		Origin: OriginPDFCore, License: LicensePDFCore,
	},

	// -----------------------------------------------------------------
	// Croscore — Apache 2.0, metrics-compatible drop-ins for the
	// PDF-14 sans / serif / mono families.
	// CapHeights from each face's published AFM-equivalent table:
	//   Arimo:   716 / 716 / 716 / 716    (Helvetica delta ≤ 0.3%)
	//   Tinos:   660 / 660 / 660 / 660    (Times    delta ≤ 0.3%)
	//   Cousine: 555 / 555 / 555 / 555    (Courier  delta ≈ 1.25%)
	// Note: Cousine's cap-height delta is above the 0.5% threshold from
	// plan §1.3, but it remains the closest open monospace substitute.
	// The validator flags this as "out-of-threshold but accepted as
	// best-available" (see substitute.go ResultOutsideThreshold).
	// -----------------------------------------------------------------
	"Arimo": {
		PSName: "Arimo", Family: "Arimo", Style: StyleRegular,
		Origin: OriginOpenSource, License: LicenseApache2, CapHeight1000: 716,
	},
	"Arimo-Bold": {
		PSName: "Arimo-Bold", Family: "Arimo", Style: StyleBold,
		Origin: OriginOpenSource, License: LicenseApache2, CapHeight1000: 716,
	},
	"Arimo-Italic": {
		PSName: "Arimo-Italic", Family: "Arimo", Style: StyleItalic,
		Origin: OriginOpenSource, License: LicenseApache2, CapHeight1000: 716,
	},
	"Arimo-BoldItalic": {
		PSName: "Arimo-BoldItalic", Family: "Arimo", Style: StyleBoldItalic,
		Origin: OriginOpenSource, License: LicenseApache2, CapHeight1000: 716,
	},
	"Tinos": {
		PSName: "Tinos", Family: "Tinos", Style: StyleRegular,
		Origin: OriginOpenSource, License: LicenseApache2, CapHeight1000: 660,
	},
	"Tinos-Bold": {
		PSName: "Tinos-Bold", Family: "Tinos", Style: StyleBold,
		Origin: OriginOpenSource, License: LicenseApache2, CapHeight1000: 660,
	},
	"Tinos-Italic": {
		PSName: "Tinos-Italic", Family: "Tinos", Style: StyleItalic,
		Origin: OriginOpenSource, License: LicenseApache2, CapHeight1000: 660,
	},
	"Tinos-BoldItalic": {
		PSName: "Tinos-BoldItalic", Family: "Tinos", Style: StyleBoldItalic,
		Origin: OriginOpenSource, License: LicenseApache2, CapHeight1000: 660,
	},
	"Cousine": {
		PSName: "Cousine", Family: "Cousine", Style: StyleRegular,
		Origin: OriginOpenSource, License: LicenseApache2, CapHeight1000: 555,
	},
	"Cousine-Bold": {
		PSName: "Cousine-Bold", Family: "Cousine", Style: StyleBold,
		Origin: OriginOpenSource, License: LicenseApache2, CapHeight1000: 555,
	},
	"Cousine-Italic": {
		PSName: "Cousine-Italic", Family: "Cousine", Style: StyleItalic,
		Origin: OriginOpenSource, License: LicenseApache2, CapHeight1000: 555,
	},
	"Cousine-BoldItalic": {
		PSName: "Cousine-BoldItalic", Family: "Cousine", Style: StyleBoldItalic,
		Origin: OriginOpenSource, License: LicenseApache2, CapHeight1000: 555,
	},
}
