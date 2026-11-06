package web

import (
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// skipped elements carry no readable text (or only page chrome).
var skipped = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Svg: true, atom.Template: true,
	atom.Iframe: true, atom.Form: true, atom.Button: true, atom.Select: true, atom.Nav: true,
	atom.Head: true, atom.Object: true, atom.Canvas: true,
}

var (
	blankLines = regexp.MustCompile(`\n{3,}`)
	spaces     = regexp.MustCompile(`[ \t\r\f\v]+`)
)

// HTMLText turns a page into readable text, Markdown-like: headings as #,
// list items as -, links as [text](url) (absolute), code blocks fenced,
// table rows with | between cells. It prefers <main> or <article> when the
// page has one, leaving out navigation and other chrome.
func HTMLText(src string, base *url.URL) (title, text string) {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return "", src
	}
	var find func(*html.Node, atom.Atom) *html.Node
	find = func(n *html.Node, a atom.Atom) *html.Node {
		if n.Type == html.ElementNode && n.DataAtom == a {
			return n
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if f := find(c, a); f != nil {
				return f
			}
		}
		return nil
	}
	if t := find(doc, atom.Title); t != nil && t.FirstChild != nil {
		title = strings.TrimSpace(t.FirstChild.Data)
	}
	root := doc
	for _, a := range []atom.Atom{atom.Main, atom.Article} {
		if n := find(doc, a); n != nil {
			root = n
			break
		}
	}
	w := &writer{base: base}
	w.walk(root)
	text = blankLines.ReplaceAllString(strings.TrimSpace(w.b.String()), "\n\n")
	return title, text
}

type writer struct {
	b    strings.Builder
	base *url.URL
	pre  int
	list []string // "-" or a counter, per open list
}

func (w *writer) newline(n int) {
	s := w.b.String()
	have := len(s) - len(strings.TrimRight(s, "\n"))
	if len(s) == 0 {
		return
	}
	for ; have < n; have++ {
		w.b.WriteByte('\n')
	}
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func (w *writer) walk(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		if w.pre > 0 {
			w.b.WriteString(n.Data)
			return
		}
		t := spaces.ReplaceAllString(strings.ReplaceAll(n.Data, "\n", " "), " ")
		if strings.TrimSpace(t) == "" {
			if !strings.HasSuffix(w.b.String(), " ") && !strings.HasSuffix(w.b.String(), "\n") && w.b.Len() > 0 {
				w.b.WriteByte(' ')
			}
			return
		}
		if strings.HasSuffix(w.b.String(), "\n") {
			t = strings.TrimLeft(t, " ")
		}
		w.b.WriteString(t)
		return
	case html.ElementNode:
	default:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			w.walk(c)
		}
		return
	}
	if skipped[n.DataAtom] || attr(n, "hidden") != "" || attr(n, "aria-hidden") == "true" {
		return
	}
	children := func() {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			w.walk(c)
		}
	}
	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		w.newline(2)
		w.b.WriteString(strings.Repeat("#", int(n.Data[1]-'0')) + " ")
		children()
		w.newline(2)
	case atom.P, atom.Div, atom.Section, atom.Header, atom.Footer, atom.Blockquote, atom.Figure, atom.Dl, atom.Dt, atom.Dd, atom.Aside, atom.Details, atom.Summary:
		w.newline(1)
		children()
		w.newline(1)
	case atom.Br:
		w.b.WriteByte('\n')
	case atom.Hr:
		w.newline(1)
		w.b.WriteString("---")
		w.newline(1)
	case atom.Ul, atom.Ol:
		mark := "-"
		if n.DataAtom == atom.Ol {
			mark = "1."
		}
		w.list = append(w.list, mark)
		w.newline(1)
		children()
		w.list = w.list[:len(w.list)-1]
		w.newline(1)
	case atom.Li:
		w.newline(1)
		mark := "-"
		if len(w.list) > 0 {
			mark = w.list[len(w.list)-1]
		}
		w.b.WriteString(strings.Repeat("  ", max(len(w.list)-1, 0)) + mark + " ")
		children()
	case atom.Pre:
		w.newline(2)
		w.b.WriteString("```\n")
		w.pre++
		children()
		w.pre--
		w.newline(1)
		w.b.WriteString("```")
		w.newline(2)
	case atom.Code:
		if w.pre > 0 {
			children()
			return
		}
		w.b.WriteByte('`')
		children()
		w.b.WriteByte('`')
	case atom.A:
		href := attr(n, "href")
		start := w.b.Len()
		children()
		label := strings.TrimSpace(w.b.String()[start:])
		if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(strings.ToLower(href), "javascript:") || label == "" {
			return
		}
		if u, err := url.Parse(href); err == nil && w.base != nil {
			href = w.base.ResolveReference(u).String()
		}
		s := w.b.String()
		w.b.Reset()
		w.b.WriteString(s[:start] + "[" + label + "](" + href + ")")
	case atom.Img:
		if alt := strings.TrimSpace(attr(n, "alt")); alt != "" {
			w.b.WriteString("[image: " + alt + "]")
		}
	case atom.Tr:
		w.newline(1)
		w.b.WriteString("|")
		children()
	case atom.Td, atom.Th:
		w.b.WriteString(" ")
		children()
		w.b.WriteString(" |")
	case atom.Table:
		w.newline(2)
		children()
		w.newline(2)
	case atom.Strong, atom.B:
		w.b.WriteString("**")
		children()
		w.b.WriteString("**")
	default:
		children()
	}
}
