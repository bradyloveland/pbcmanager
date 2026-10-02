package alerts

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
)

// Email is an alert's content, made into a plain-text and an HTML body.
// Both come from the same parts, so they always say the same thing.
type Email struct {
	Tone     string   // ok, bad, warn or info: the colour of the status bar
	Label    string   // the status bar's words, such as "Backup failed"
	Headline string   // one line saying what happened
	Paras    []string // sentences: what it means and what to do
	Rows     []Row    // details
	Link     string   // a page in the web UI ("" for none)
	LinkText string   // the button's words, such as "View the run"
	Log      string   // the end of a failed run's log
}

// Row is one detail line. Indented rows belong to the row above them.
type Row struct {
	Label, Value string
	Indent       bool
}

// Text is the plain-text body, laid out the way alert emails always were.
func (e *Email) Text(footer string) string {
	var b strings.Builder
	b.WriteString(strings.Join(e.Paras, "\n\n"))
	b.WriteString("\n")
	if len(e.Rows) > 0 {
		b.WriteString("\n")
		for _, r := range e.Rows {
			if r.Indent {
				fmt.Fprintf(&b, "  %-12s %s\n", r.Label+":", r.Value)
			} else {
				fmt.Fprintf(&b, "%-14s%s\n", r.Label+":", r.Value)
			}
		}
	}
	if e.Link != "" {
		fmt.Fprintf(&b, "\n%s: %s\n", e.LinkText, e.Link)
	}
	if e.Log != "" {
		b.WriteString("\nLast lines of the log:\n" + strings.Repeat("-", 60) + "\n" + e.Log)
	}
	return b.String() + footer
}

var tones = map[string]string{"ok": "#2E7A4F", "bad": "#B3261E", "warn": "#96610A", "info": "#56636E"}

// emailTemplate is laid out the way email apps reliably show: tables, inline
// styles, at most 600px wide, no web fonts, scripts or remote images (so
// nothing loads from the internet and nothing tracks opens). html/template
// escapes every value, so names and log lines can't change the layout.
var emailTemplate = template.Must(template.New("email").Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="color-scheme" content="light only"><meta name="supported-color-schemes" content="light"><title>{{.Headline}}</title></head>
<body style="margin:0;padding:0;background-color:#EEF1F4;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background-color:#EEF1F4;"><tr><td align="center" style="padding:24px 12px;">
<table role="presentation" width="600" cellpadding="0" cellspacing="0" border="0" style="width:100%;max-width:600px;background-color:#FFFFFF;border:1px solid #D6DDE3;border-radius:10px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#15202B;">
<tr><td style="background-color:{{.Color}};border-radius:9px 9px 0 0;padding:11px 24px;color:#FFFFFF;font-size:13px;font-weight:700;letter-spacing:0.03em;text-transform:uppercase;">{{.Label}}</td></tr>
<tr><td style="padding:22px 24px 4px;"><h1 style="margin:0;font-size:20px;line-height:1.3;font-weight:700;color:#15202B;">{{.Headline}}</h1></td></tr>
{{range .Paras}}<tr><td style="padding:10px 24px 0;font-size:15px;line-height:1.55;color:#33414D;">{{.}}</td></tr>
{{end}}{{if .Rows}}<tr><td style="padding:18px 24px 0;"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="font-size:14px;line-height:1.45;border-top:1px solid #E3E8EC;">
{{range .Rows}}<tr><td valign="top" style="padding:7px 14px 7px {{if .Indent}}16px{{else}}0{{end}};color:#5B6873;white-space:nowrap;border-bottom:1px solid #E3E8EC;">{{.Label}}</td><td valign="top" style="padding:7px 0;color:#15202B;border-bottom:1px solid #E3E8EC;">{{.Value}}</td></tr>
{{end}}</table></td></tr>
{{end}}{{if .Link}}<tr><td style="padding:22px 24px 0;"><a href="{{.Link}}" style="display:inline-block;background-color:#2F6EA5;color:#FFFFFF;text-decoration:none;font-weight:600;font-size:14px;line-height:1;padding:12px 18px;border-radius:6px;">{{.LinkText}}</a></td></tr>
{{end}}{{if .Log}}<tr><td style="padding:22px 24px 0;"><div style="font-size:13px;color:#5B6873;padding-bottom:6px;">Last lines of the log</div><pre style="margin:0;background-color:#0F1720;color:#D5DEE6;font-family:Menlo,Consolas,'Courier New',monospace;font-size:12px;line-height:1.5;padding:12px 14px;border-radius:6px;white-space:pre-wrap;word-break:break-word;">{{.Log}}</pre></td></tr>
{{end}}<tr><td style="padding:24px 24px 22px;"><div style="border-top:1px solid #E3E8EC;padding-top:14px;font-size:12px;line-height:1.5;color:#7A8791;">{{.Footer}}</div></td></tr>
</table></td></tr></table></body></html>
`))

// HTML is the HTML body.
func (e *Email) HTML(footer string) (string, error) {
	color := tones[e.Tone]
	if color == "" {
		color = tones["info"]
	}
	var b bytes.Buffer
	err := emailTemplate.Execute(&b, struct {
		*Email
		Color  template.CSS
		Footer string
	}{e, template.CSS(color), footer})
	return b.String(), err
}
