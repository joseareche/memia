package commercial

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/jackc/pgx/v5"
)

// TemplateVersion is stored with every export; bump when the layout changes.
const TemplateVersion = "cot-docx-v1"

//go:embed templates/cotizacion-v1.xml
var tplFS embed.FS

var xmlEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")

func run(text, style string, size int, color string) string {
	rp := `<w:rFonts w:ascii="Liberation Sans" w:hAnsi="Liberation Sans" w:cs="Liberation Sans"/>`
	if strings.Contains(style, "B") {
		rp += "<w:b/>"
	}
	if strings.Contains(style, "I") {
		rp += "<w:i/>"
	}
	if color != "" {
		rp += `<w:color w:val="` + color + `"/>`
	}
	rp += fmt.Sprintf(`<w:sz w:val="%d"/>`, size)
	return `<w:r><w:rPr>` + rp + `</w:rPr><w:t xml:space="preserve">` + xmlEsc.Replace(text) + `</w:t></w:r>`
}

func para(text, style string, size int, color, align string) string {
	return `<w:p><w:pPr><w:spacing w:before="0" w:after="40"/><w:jc w:val="` + align + `"/></w:pPr>` + run(text, style, size, color) + `</w:p>`
}

func table(head []string, rows [][]string, widths []int) string {
	var b strings.Builder
	b.WriteString(`<w:tbl><w:tblPr><w:tblW w:w="9900" w:type="dxa"/><w:tblBorders><w:top w:val="single" w:sz="4" w:color="E5EAF1"/><w:bottom w:val="single" w:sz="4" w:color="E5EAF1"/><w:insideH w:val="single" w:sz="4" w:color="E5EAF1"/></w:tblBorders><w:tblCellMar><w:left w:w="80" w:type="dxa"/><w:right w:w="80" w:type="dxa"/></w:tblCellMar></w:tblPr><w:tblGrid>`)
	for _, w := range widths {
		fmt.Fprintf(&b, `<w:gridCol w:w="%d"/>`, w)
	}
	b.WriteString(`</w:tblGrid>`)
	cell := func(t string, w int, hdr bool, right bool) {
		al := "left"
		if right {
			al = "right"
		}
		shade := ""
		st, col := "", "16243D"
		if hdr {
			shade = `<w:shd w:val="clear" w:color="auto" w:fill="EAF2FF"/>`
			st, col = "B", "1F2A44"
		}
		fmt.Fprintf(&b, `<w:tc><w:tcPr><w:tcW w:w="%d" w:type="dxa"/>%s</w:tcPr>%s</w:tc>`, w, shade, para(t, st, 17, col, al))
	}
	b.WriteString(`<w:tr><w:trPr><w:tblHeader/></w:trPr>`)
	for i, h := range head {
		cell(h, widths[i], true, i >= 3)
	}
	b.WriteString(`</w:tr>`)
	for _, r := range rows {
		b.WriteString(`<w:tr><w:trPr><w:cantSplit/></w:trPr>`)
		for i, c := range r {
			cell(c, widths[i], false, i >= 3)
		}
		b.WriteString(`</w:tr>`)
	}
	b.WriteString(`</w:tbl>`)
	return b.String()
}

var docTpl = template.Must(template.New("doc").Funcs(template.FuncMap{"p": para, "table": table}).ParseFS(tplFS, "templates/cotizacion-v1.xml"))

type docCycle struct {
	Label, Total string
	Lines        []string
}
type docData struct {
	Company                                                 struct{ LegalName, TradeName, Footer string }
	CompanyLine, Heading, MetaLine, Title, CycleNote, Legal string
	Customer                                                struct{ Name, Doc, Extra, Flag string }
	Head                                                    []string
	Rows                                                    [][]string
	Widths                                                  []int
	Cycles                                                  []docCycle
	Terms                                                   []string
}

var cycLabel = map[string]string{"one_time": "Pago único", "monthly": "Mensual", "annual": "Anual", "usage": "Variable / por uso"}
var cycShort = map[string]string{"one_time": "único", "monthly": "mensual", "annual": "anual", "usage": "variable"}
var taxLabel = map[string]string{"gravado": "Gravado", "exonerado": "Exonerado", "inafecto": "Inafecto"}

func moneyFmt(cur, v string) string {
	sym := "S/"
	if cur == "USD" {
		sym = "US$"
	}
	neg := strings.HasPrefix(v, "-")
	v = strings.TrimPrefix(v, "-")
	ip, dp, _ := strings.Cut(v, ".")
	var b strings.Builder
	for i, c := range ip {
		if i > 0 && (len(ip)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	dp = (dp + "00")[:2]
	if neg {
		return "-" + sym + " " + b.String() + "." + dp
	}
	return sym + " " + b.String() + "." + dp
}

// BuildDOCX renders an approved snapshot. Amounts come only from snap.Totals (already verified).
func BuildDOCX(c company, snap Quote, generated time.Time) ([]byte, error) {
	var d docData
	d.Company.LegalName, d.Company.TradeName, d.Company.Footer = c.LegalName, c.TradeName, c.Footer
	ruc := "RUC: " + c.TaxID
	if c.TaxID == "" {
		ruc = "RUC: (configurar)"
	}
	d.CompanyLine = strings.Join(nonEmpty(ruc, c.Address, c.Phone, c.Email, c.Website), " · ")
	num := "BORRADOR"
	if snap.Number != nil {
		num = *snap.Number
	}
	d.Heading = "COTIZACIÓN " + num
	vu := "—"
	if snap.ValidUntil != nil {
		vu = fmtDate(*snap.ValidUntil)
	}
	igvTxt := "Precios sin IGV"
	if snap.PricesIncludeIGV {
		igvTxt = "Precios incluyen IGV"
	}
	d.MetaLine = fmt.Sprintf("Fecha: %s · Vigente hasta: %s · Moneda: %s · %s · Versión %d", generated.In(lima).Format("02/01/2006"), vu, snap.Currency, igvTxt, snap.Version)
	var cs struct {
		DisplayName string       `json:"display_name"`
		Data        CustomerData `json:"data"`
		Source      string       `json:"source"`
	}
	_ = json.Unmarshal(snap.Customer, &cs)
	d.Customer.Name = cs.DisplayName
	if cs.Data.TaxID != "" {
		d.Customer.Doc = cs.Data.DocumentType + ": " + cs.Data.TaxID
	}
	d.Customer.Extra = strings.Join(nonEmpty(cs.Data.Address, cs.Data.Email, cs.Data.Phone), " · ")
	if cs.Data.TaxID != "" && strings.TrimSpace(cs.DisplayName) == cs.Data.DocumentType+" "+cs.Data.TaxID {
		d.Customer.Flag = "Razón social pendiente de verificación."
	}
	d.Title = snap.Title
	d.Head = []string{"#", "Descripción", "Ciclo", "Cant.", "P. unit.", "Dscto.", "Total"}
	d.Widths = []int{400, 3900, 1100, 800, 1300, 800, 1600}
	var tot CalcOut
	_ = json.Unmarshal(snap.Totals, &tot)
	for i, it := range snap.Items {
		desc := it.Description
		if it.TaxRule != "gravado" {
			desc += " (" + taxLabel[it.TaxRule] + ")"
		}
		disc := ""
		if it.DiscountPct != "" && it.DiscountPct != "0" {
			disc = it.DiscountPct + " %"
		}
		lt := ""
		if i < len(tot.Lines) {
			lt = moneyFmt(snap.Currency, tot.Lines[i].Total)
		}
		d.Rows = append(d.Rows, []string{fmt.Sprint(i + 1), desc, cycLabel[it.BillingMode], it.Qty + " " + it.Unit, moneyFmt(snap.Currency, it.UnitPrice), disc, lt})
	}
	for _, k := range BillingModes {
		c, ok := tot.Cycles[k]
		if !ok {
			continue
		}
		dc := docCycle{Label: cycLabel[k]}
		add := func(l, v string) {
			if v != "" && v != "0.00" {
				dc.Lines = append(dc.Lines, l+": "+moneyFmt(snap.Currency, v))
			}
		}
		add("Descuentos", c.Discount)
		add("Op. gravada", c.Gravada)
		add("Op. exonerada", c.Exonerada)
		add("Op. inafecta", c.Inafecta)
		dc.Lines = append(dc.Lines, "IGV: "+moneyFmt(snap.Currency, c.IGV))
		dc.Total = "Total " + cycShort[k] + ": " + moneyFmt(snap.Currency, c.Total)
		d.Cycles = append(d.Cycles, dc)
	}
	if len(d.Cycles) > 1 {
		d.CycleNote = "Los importes de cada ciclo se facturan por separado; no se suman pagos únicos con recurrentes."
	}
	for _, l := range strings.Split(strings.ReplaceAll(snap.Terms, "\r", ""), "\n") {
		if strings.TrimSpace(l) != "" {
			d.Terms = append(d.Terms, l)
		}
	}
	d.Legal = "Documento generado por el Cotizador IA (" + TemplateVersion + "). No es un comprobante de pago."
	var doc bytes.Buffer
	if err := docTpl.ExecuteTemplate(&doc, "cotizacion-v1.xml", d); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	files := []struct{ n, b string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/></Relationships>`},
		{"docProps/core.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>` + xmlEsc.Replace(d.Heading) + `</dc:title><dc:creator>Cotizador IA</dc:creator><cp:keywords>` + TemplateVersion + `</cp:keywords></cp:coreProperties>`},
		{"word/document.xml", doc.String()},
	}
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // deterministic zip timestamps
	for _, f := range files {
		w, err := z.CreateHeader(&zip.FileHeader{Name: f.n, Method: zip.Deflate, Modified: fixed})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(f.b)); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func nonEmpty(ss ...string) []string {
	o := []string{}
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			o = append(o, strings.TrimSpace(s))
		}
	}
	return o
}
func fmtDate(s string) string {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Format("02/01/2006")
	}
	return s
}

// VerifySnapshot recomputes the totals of an approved snapshot and requires an exact match.
func VerifySnapshot(snap Quote) error {
	in := CalcIn{Currency: snap.Currency, PricesIncludeIGV: snap.PricesIncludeIGV, IGVRate: snap.IGVRate, GlobalDiscountPct: snap.GlobalDiscountPct}
	for _, it := range snap.Items {
		in.Lines = append(in.Lines, it.LineIn)
	}
	got, err := Calculate(in)
	if err != nil {
		return err
	}
	var want CalcOut
	if err := json.Unmarshal(snap.Totals, &want); err != nil {
		return err
	}
	g, _ := json.Marshal(struct {
		L []LineOut
		C map[string]CycleTotals
	}{got.Lines, got.Cycles})
	w, _ := json.Marshal(struct {
		L []LineOut
		C map[string]CycleTotals
	}{want.Lines, want.Cycles})
	if !bytes.Equal(g, w) {
		return errors.New("los importes recalculados no coinciden con la versión aprobada")
	}
	return nil
}

// ---------- LibreOffice converter (one at a time, timeout, private profile) ----------

var sofficeMu sync.Mutex

func SofficePDF(bin string, timeout time.Duration) func(ctx context.Context, docx []byte) ([]byte, error) {
	return func(ctx context.Context, docx []byte) ([]byte, error) {
		sofficeMu.Lock()
		defer sofficeMu.Unlock()
		dir, err := os.MkdirTemp("", "cotpdf-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		in := filepath.Join(dir, "q.docx")
		if err := os.WriteFile(in, docx, 0o600); err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, "--headless", "--norestore", "--nologo", "--nodefault", "--nolockcheck",
			"-env:UserInstallation=file://"+filepath.Join(dir, "profile"), "--convert-to", "pdf:writer_pdf_Export", "--outdir", dir, in)
		cmd.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin", "LANG=C.UTF-8", "SAL_USE_VCLPLUGIN=svp"}
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("soffice: %v: %.300s", err, out)
		}
		b, err := os.ReadFile(filepath.Join(dir, "q.pdf"))
		if err != nil {
			return nil, fmt.Errorf("soffice produced no pdf: %w", err)
		}
		if !bytes.HasPrefix(b, []byte("%PDF-")) {
			return nil, errors.New("output is not a PDF")
		}
		return b, nil
	}
}

// ---------- handlers ----------

type exportMeta struct {
	ID              string `json:"id"`
	Version         int    `json:"version"`
	Format          string `json:"format"`
	TemplateVersion string `json:"template_version"`
	SHA256          string `json:"sha256"`
	Filename        string `json:"filename"`
	Size            int    `json:"size"`
	TotalsVerified  bool   `json:"totals_verified"`
	CreatedAt       string `json:"created_at"`
	URL             string `json:"url"`
}

var exportable = map[string]bool{"approved": true, "sent": true, "accepted": true}
var fileSafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (a *API) exportQuote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Format  string `json:"format"`
		Version int    `json:"version"` // optional: a specific approved version (e.g. after a revision)
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Format != "pdf" && in.Format != "docx" {
		fail(w, 400, "Formato no válido (pdf o docx).")
		return
	}
	if in.Format == "pdf" && a.PDF == nil {
		fail(w, 503, "Conversión a PDF no disponible en el servidor.")
		return
	}
	ac := actorOf(r)
	var snap Quote
	var comp company
	var cached *exportMeta
	err := a.tx(r, func(tx pgx.Tx) error {
		var status string
		var ver int
		var lastOK *int
		if err := tx.QueryRow(r.Context(), `SELECT status, current_version, (SELECT max(v.version) FROM quote_versions v WHERE v.quote_id=quotes.id AND v.status IN ('approved','sent','accepted')) FROM quotes WHERE id=$1`, id).Scan(&status, &ver, &lastOK); err != nil {
			return err
		}
		switch {
		case in.Version > 0:
			ver = in.Version
		case exportable[status]:
		case lastOK != nil:
			ver = *lastOK // revised draft: last approved version
		default:
			return badReq{"Solo se exporta una cotización aprobada (estado actual: " + status + ")."}
		}
		var raw []byte
		var vst string
		if err := tx.QueryRow(r.Context(), "SELECT snapshot, status FROM quote_versions WHERE quote_id=$1 AND version=$2", id, ver).Scan(&raw, &vst); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return badReq{"No existe la versión aprobada para exportar."}
			}
			return err
		}
		if !exportable[vst] && !(ver == lastOrZero(lastOK) && exportable[status]) {
			return badReq{"Esa versión no está aprobada; no se puede exportar."}
		}
		if err := json.Unmarshal(raw, &snap); err != nil {
			return err
		}
		var m exportMeta
		e := tx.QueryRow(r.Context(), `SELECT e.id::text, e.version, e.format, e.template_version, e.sha256, e.filename, b.size, e.totals_verified, to_char(e.created_at AT TIME ZONE 'UTC',`+ts+`)
			FROM quote_exports e JOIN commercial_blobs b ON b.tenant_id=e.tenant_id AND b.sha256=e.sha256
			WHERE e.quote_id=$1 AND e.version=$2 AND e.format=$3 AND e.template_version=$4 ORDER BY e.created_at DESC LIMIT 1`, id, ver, in.Format, TemplateVersion).
			Scan(&m.ID, &m.Version, &m.Format, &m.TemplateVersion, &m.SHA256, &m.Filename, &m.Size, &m.TotalsVerified, &m.CreatedAt)
		if e == nil {
			m.URL = "exports/" + m.ID
			cached = &m
			return nil
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		var err error
		comp, err = a.companyDefaults(r, tx)
		return err
	})
	if err != nil {
		a.respondQuote(w, err, Quote{}, 0)
		return
	}
	if cached != nil {
		jsonOut(w, 200, cached)
		return
	}
	if err := VerifySnapshot(snap); err != nil {
		a.Log.Error("export verify", "quote", id, "err", err)
		fail(w, 409, "Exportación cancelada: "+err.Error()+".")
		return
	}
	docx, err := BuildDOCX(comp, snap, a.Now())
	if err != nil {
		a.internal(w, "docx", err)
		return
	}
	file, mime := docx, "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	if in.Format == "pdf" {
		if file, err = a.PDF(r.Context(), docx); err != nil {
			a.Log.Error("pdf convert", "err", err)
			fail(w, 502, "No se pudo generar el PDF. Inténtalo de nuevo.")
			return
		}
		mime = "application/pdf"
	}
	sum := sha256.Sum256(file)
	sh := hex.EncodeToString(sum[:])
	base := "cotizacion"
	if snap.Number != nil {
		base = *snap.Number
	}
	name := fileSafe.ReplaceAllString(fmt.Sprintf("%s-v%d.%s", base, snap.Version, in.Format), "_")
	var m exportMeta
	err = a.tx(r, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `INSERT INTO commercial_blobs (tenant_id, sha256, mime, size, data) VALUES (current_setting('app.tenant_id')::uuid, $1, $2, $3, $4) ON CONFLICT DO NOTHING`,
			sh, mime, len(file), file); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `INSERT INTO quote_exports (tenant_id, quote_id, version, format, template_version, sha256, filename, totals_verified, created_by)
			VALUES (current_setting('app.tenant_id')::uuid, $1, $2, $3, $4, $5, $6, true, $7) RETURNING id::text, to_char(created_at AT TIME ZONE 'UTC',`+ts+`)`,
			id, snap.Version, in.Format, TemplateVersion, sh, name, ac.Email).Scan(&m.ID, &m.CreatedAt); err != nil {
			return err
		}
		return event(r.Context(), tx, id, ac.Email, "export", "", "", map[string]any{"format": in.Format, "version": snap.Version, "sha256": sh, "template": TemplateVersion})
	})
	if err != nil {
		a.internal(w, "export save", err)
		return
	}
	m.Version, m.Format, m.TemplateVersion, m.SHA256, m.Filename, m.Size, m.TotalsVerified = snap.Version, in.Format, TemplateVersion, sh, name, len(file), true
	m.URL = "exports/" + m.ID
	jsonOut(w, 201, m)
}

func (a *API) listExports(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out := []exportMeta{}
	err := a.tx(r, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT e.id::text, e.version, e.format, e.template_version, e.sha256, e.filename, b.size, e.totals_verified, to_char(e.created_at AT TIME ZONE 'UTC',`+ts+`)
			FROM quote_exports e JOIN commercial_blobs b ON b.tenant_id=e.tenant_id AND b.sha256=e.sha256 WHERE e.quote_id=$1 ORDER BY e.created_at DESC LIMIT 50`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m exportMeta
			if err := rows.Scan(&m.ID, &m.Version, &m.Format, &m.TemplateVersion, &m.SHA256, &m.Filename, &m.Size, &m.TotalsVerified, &m.CreatedAt); err != nil {
				return err
			}
			m.URL = "exports/" + m.ID
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		a.internal(w, "exports", err)
		return
	}
	jsonOut(w, 200, map[string]any{"exports": out})
}

func (a *API) downloadExport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var name, mime, sh string
	var data []byte
	err := a.tx(r, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT e.filename, b.mime, b.sha256, b.data FROM quote_exports e JOIN commercial_blobs b ON b.tenant_id=e.tenant_id AND b.sha256=e.sha256 WHERE e.id=$1`, id).
			Scan(&name, &mime, &sh, &data)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 404, "No encontrado.")
		return
	} else if err != nil {
		a.internal(w, "download", err)
		return
	}
	if s := sha256.Sum256(data); hex.EncodeToString(s[:]) != sh {
		a.Log.Error("blob integrity", "export", id)
		fail(w, 500, "Error de integridad del archivo.")
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-SHA256", sh)
	_, _ = w.Write(data)
}

func (a *API) integrations(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, 200, map[string]any{"adapters": adapterNeeds, "pdf_available": a.PDF != nil, "ruc_lookup": a.ruc().Enabled(), "mail": a.mailer().Enabled()})
}

func lastOrZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
