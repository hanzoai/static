package static

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// The Hanzo tag is one line of script, and every document this server sends goes
// out carrying it. Putting it here rather than in each site's HTML is what makes
// it true of ALL of them: a site gets analytics by being served, not by
// remembering to paste something, and a site that is rebuilt from a template
// cannot lose it.
//
// The script itself is hosted at the other end and does the rest — it resolves
// the site's connected pixels from /v1/tags and posts to /v1/event first-party.
// The only thing the edge knows that the browser cannot work out for itself is
// WHICH site this is, so the key is the whole of what gets injected.
const tagSrc = "https://api.hanzo.ai/v1/event.js"

// maxDoc bounds what will be read into memory to be tagged. A document is an
// entry point — index.html and its siblings — and those are kilobytes. Anything
// past this is not a page anyone wrote by hand, so it streams through untouched
// rather than being buffered on the chance it has a <head>.
const maxDoc = 1 << 20

// tag renders the element for key.
func tag(key string) string {
	return `<script defer src="` + tagSrc + `" data-key="` + key + `"></script>`
}

// publishable reports whether key is a publishable key, which is both halves of
// the question worth asking about it.
//
// The `pk-` prefix is the tag's own admission rule — it refuses anything else and
// stays inert — so injecting a value without it produces a tag that does nothing.
// The character set is the other half: the key is written into an HTML attribute,
// and one restricted to letters, digits, `-` and `_` cannot close the quote it
// sits in. A misconfigured key therefore CANNOT rewrite the page; it can only
// fail to appear.
func publishable(key string) bool {
	if !strings.HasPrefix(key, "pk-") || len(key) == len("pk-") {
		return false
	}

	for i := 0; i < len(key); i++ {
		c := key[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			continue
		}

		return false
	}

	return true
}

// isHTML reports whether contentType names an HTML document.
//
// The content type, not the extension: it is what the browser will believe, so
// it is the only thing that decides whether these bytes are going to be parsed
// as HTML. A .json file that happens to contain the text `</head>` is not.
func isHTML(contentType string) bool {
	return strings.HasPrefix(contentType, "text/html")
}

// inject returns doc with the tag before its closing </head>, or doc itself.
//
// Every reason to leave the document alone is a plain no: no usable key, a
// document that already carries the tag, no head to put it in. The bytes are
// returned unchanged in each case, so the worst this can do to a page is
// nothing.
func inject(doc []byte, key string) []byte {
	if !publishable(key) || bytes.Contains(doc, []byte(tagSrc)) {
		return doc
	}

	end := indexEndHead(doc)
	if end < 0 {
		return doc
	}

	el := tag(key)
	out := make([]byte, 0, len(doc)+len(el))
	out = append(out, doc[:end]...)
	out = append(out, el...)

	return append(out, doc[end:]...)
}

// indexEndHead returns the offset of the closing </head> tag at any casing, or
// -1. HTML tag names are ASCII, so ASCII folding is the whole of the matching.
func indexEndHead(doc []byte) int {
	const end = "</head>"

	for i := 0; i+len(end) <= len(doc); i++ {
		j := bytes.IndexByte(doc[i:], '<')
		if j < 0 {
			return -1
		}
		i += j
		if i+len(end) > len(doc) {
			return -1
		}
		if foldEqual(doc[i:i+len(end)], end) {
			return i
		}
	}

	return -1
}

// foldEqual compares b to the ASCII-lowercase s, folding b as it goes.
func foldEqual(b []byte, s string) bool {
	for i := 0; i < len(s); i++ {
		c := b[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != s[i] {
			return false
		}
	}

	return true
}

// body returns the bytes to serve for one file: the document with the tag in it,
// or the file exactly as it is.
//
// The file on the backend is never touched — the tag exists only in the response
// — and every path that is not a small HTML document returns the reader it was
// given, so nothing else is read into memory or copied.
func (h *Handler) body(w http.ResponseWriter, f io.ReadSeeker, size int64, contentType string) io.ReadSeeker {
	if !publishable(h.key) || !isHTML(contentType) || size > maxDoc {
		return f
	}

	doc, err := io.ReadAll(f)
	if err != nil {
		// Rewind what was consumed and hand back the file. A file that cannot be
		// read fails in ServeContent exactly as it did before there was a tag.
		_, _ = f.Seek(0, io.SeekStart)

		return f
	}

	out := inject(doc, h.key)
	if len(out) != len(doc) {
		admit(w.Header())
	}

	return bytes.NewReader(out)
}

// tagOrigin is the one origin a page has to be allowed to reach for the tag to
// work: the script is loaded from it and every event is sent back to it.
var tagOrigin = origin(tagSrc)

// origin returns the scheme-and-host of an absolute https URL.
func origin(u string) string {
	const scheme = "https://"

	if i := strings.Index(u[len(scheme):], "/"); i >= 0 {
		return u[:len(scheme)+i]
	}

	return u
}

// reach names the two directives the tag's origin has to appear in: one to load
// the script, one for the events it sends back.
var reach = []string{"script-src", "connect-src"}

// admit lets the tag's origin through the content policy this response is
// already carrying.
//
// The server that puts the script in the page is the only thing that knows it is
// there, so it is also the only thing that can say so. Measured against the live
// policies on docs, cloud and console: none of them names api.hanzo.ai, so an
// injected tag would be refused by the browser before it ran — the alternative
// to this function is editing every site's policy by hand, forever, which is the
// per-site work the injection exists to abolish.
//
// It only ever ADDS one exact origin to the directives the tag needs, and only
// when a tag was actually injected. A response with no policy is left alone:
// nothing is being refused, so there is nothing to admit.
func admit(h http.Header) {
	policy := h.Get("Content-Security-Policy")
	if policy == "" {
		return
	}

	parts := strings.Split(policy, ";")

	// Where each directive is FIRST declared. A repeated one is ignored by the
	// browser after its first appearance, so amending a later copy would allow
	// nothing while reading as though it had.
	at := make(map[string]int, len(parts))
	for i, p := range parts {
		name := strings.ToLower(directive(p))
		if _, seen := at[name]; name == "" || seen {
			continue
		}
		at[name] = i
	}

	changed := false
	for _, d := range reach {
		i, own := at[d]
		if !own {
			// Absent, so default-src governs it. With no default-src either, nothing
			// is refusing the tag and there is nothing to say.
			fallback, ok := at["default-src"]
			if !ok {
				continue
			}
			i = fallback
		}
		if covers(parts[i]) {
			continue
		}

		if own {
			parts[i] = lead(parts[i]) + d + allow(parts[i])
		} else {
			// Spelled out from what it inherits, so naming the directive neither
			// widens nor narrows the decision the site already made.
			parts = append(parts, " "+d+allow(parts[i]))
		}
		changed = true
	}

	if changed {
		h.Set("Content-Security-Policy", strings.Join(parts, ";"))
	}
}

// allow returns a policy part's source list with the tag's origin added, each
// source led by a space.
//
// 'none' drops out. It means "nothing", and is defined only as the whole of a
// list — once one origin is allowed it is no longer the whole of anything, and
// leaving it in writes a policy that reads as a contradiction.
func allow(part string) string {
	out := ""
	for _, src := range list(part) {
		if src == "'none'" {
			continue
		}
		out += " " + src
	}

	return out + " " + tagOrigin
}

// lead returns the whitespace a policy part starts with, so rewriting one does
// not disturb how the policy was spelled.
func lead(part string) string {
	return part[:len(part)-len(strings.TrimLeft(part, " "))]
}

// directive returns the name a policy part declares, or "".
func directive(part string) string {
	if f := strings.Fields(part); len(f) > 0 {
		return f[0]
	}

	return ""
}

// list returns a policy part's sources — the fields after the directive name.
func list(part string) []string {
	if f := strings.Fields(part); len(f) > 1 {
		return f[1:]
	}

	return nil
}

// covers reports whether a policy part already permits the tag's origin, whether
// named outright, by scheme, by `*`, or by a wildcard host it falls under.
func covers(part string) bool {
	for _, src := range list(part) {
		switch src {
		case tagOrigin, "*", "https:":
			return true
		}
		// https://*.hanzo.ai — the host after the star must be a suffix of ours at a
		// label boundary, so a wildcard cannot be matched by a name that merely ends
		// in the same letters.
		if star := strings.Index(src, "://*."); star >= 0 &&
			src[:star] == "https" &&
			strings.HasSuffix(tagOrigin, src[star+len("://*"):]) {
			return true
		}
	}

	return false
}
