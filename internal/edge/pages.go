package edge

import (
	"fmt"
	"html"
	"net/http"
	"strings"
)

// The pages below are what a person sees when a deploy is not answering, and
// they are written for that person: which hostname was asked for, what loods
// thinks is wrong with it, and what to type next. A bare 502 from a proxy
// tells you none of that, and on a board where deploys come and go that is the
// difference between a two-minute fix and a hunt.

const pageCSS = `*{box-sizing:border-box}body{font:16px/1.65 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;
margin:0;min-height:100vh;display:grid;place-items:center;padding:2rem;background:#fafafa;color:#1a1a1a}
main{max-width:34rem}h1{font-size:1.35rem;margin:0 0 .4rem;font-weight:620}
p{margin:.55rem 0;color:#444}code{font:0.92em ui-monospace,SFMono-Regular,Menlo,monospace;
background:#ececec;padding:.12em .38em;border-radius:4px}
ul{margin:.6rem 0 0;padding:0;list-style:none}li{margin:.2rem 0}
a{color:inherit}.dim{color:#6b6b6b;font-size:.9rem;margin-top:1.1rem}
@media(prefers-color-scheme:dark){body{background:#121212;color:#ededed}p{color:#b9b9b9}
code{background:#242424}.dim{color:#8a8a8a}}`

func writePage(w http.ResponseWriter, code int, title, body string) {
	page := fmt.Sprintf(
		"<!doctype html><meta charset=utf-8><meta name=viewport content=\"width=device-width,initial-scale=1\">"+
			"<title>%s</title><style>%s</style><main><h1>%s</h1>%s</main>",
		html.EscapeString(title), pageCSS, html.EscapeString(title), body)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(page))
}

func code(s string) string { return "<code>" + html.EscapeString(s) + "</code>" }

// writeUnknownHost answers a hostname no deploy claims. The wildcard record
// means every name under the domain resolves, so this is the common mistake:
// a typo, or a project that was never deployed. Listing what does answer turns
// it into a one-glance fix.
func writeUnknownHost(w http.ResponseWriter, host string, routes []Route) {
	var b strings.Builder
	fmt.Fprintf(&b, "<p>Nothing is deployed at %s.</p>", code(host))
	if len(routes) == 0 {
		b.WriteString("<p>No project is published yet. In a project: <code>loods deploy</code>.</p>")
	} else {
		b.WriteString("<p>What is published:</p><ul>")
		for _, r := range routes {
			fmt.Fprintf(&b, "<li>%s", code(r.Host))
			if r.Status != "" {
				fmt.Fprintf(&b, " <span class=dim>%s</span>", html.EscapeString(r.Status))
			}
			b.WriteString("</li>")
		}
		b.WriteString("</ul>")
	}
	b.WriteString("<p class=dim>loods edge</p>")
	writePage(w, http.StatusNotFound, "No deploy here", b.String())
}

// writeNotServing answers a deploy the board knows about but that is not
// running: stopped by hand, a build that failed, or one still being built.
func writeNotServing(w http.ResponseWriter, r Route) {
	status := r.Status
	if status == "" {
		status = "not serving"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<p>%s is %s.</p>", code(r.Host), html.EscapeString(status))
	if r.Note != "" {
		fmt.Fprintf(&b, "<p>%s</p>", html.EscapeString(r.Note))
	}
	if r.Project != "" {
		fmt.Fprintf(&b, "<p>Deploy it again: <code>loods deploy -p %s</code></p>",
			html.EscapeString(r.Project))
	}
	writePage(w, http.StatusServiceUnavailable, "Deploy is not running", b.String())
}

// writeUpstreamDown answers a deploy whose process is supposed to be up but
// did not take the connection: it crashed, or it is still starting.
//
// It answers 503 and not the 502 this really is, because Cloudflare replaces
// an origin 502 or 504 with a generic page of its own — passing those through
// is an Enterprise feature — and the whole point of this page is that it says
// which upstream was tried. 503 reaches the visitor intact, and "temporarily
// not answering" is a fair reading of a deploy whose process is down.
func writeUpstreamDown(w http.ResponseWriter, r Route, err error) {
	var b strings.Builder
	fmt.Fprintf(&b, "<p>%s is routed to %s, which did not answer.</p>",
		code(r.Host), code(r.Upstream))
	if err != nil {
		fmt.Fprintf(&b, "<p class=dim>%s</p>", html.EscapeString(err.Error()))
	}
	if r.Project != "" {
		fmt.Fprintf(&b, "<p>What it said: <code>loods deploy log -p %s</code></p>",
			html.EscapeString(r.Project))
	}
	writePage(w, http.StatusServiceUnavailable, "Deploy is not answering", b.String())
}

// writeNotFound answers a path that is not in a static deploy's output.
func writeNotFound(w http.ResponseWriter, r Route, path string) {
	body := fmt.Sprintf("<p>%s has no %s.</p>", code(r.Host), code(path))
	writePage(w, http.StatusNotFound, "Not found", body)
}
