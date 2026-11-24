package adapter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Branded is an optional interface for an adapter that ships its app's logo: a one-colour SVG whose alpha is the
// glyph (the UI paints it with the text colour through a CSS mask). Logo returns the SVG, or nil for none.
type Branded interface {
	Logo() []byte
}

// LogoPath is where the UI fetches the logo of an adapter type, the same for every type.
func LogoPath(typ string) string { return "/api/icons/" + typ + ".svg" }

// logoVersion is a short hash of a logo, added to its URL so the URL changes whenever the logo does and the
// browser may keep it for long.
func logoVersion(svg []byte) string {
	sum := sha256.Sum256(svg)
	return hex.EncodeToString(sum[:4])
}

// CheckLogo reports why svg is not a safe, self-contained SVG image: it must parse as XML with an <svg> root and
// hold no script, foreign content, embedded image, style sheet, event handler, or reference to anything outside
// itself (only "#id" references are allowed).
func CheckLogo(svg []byte) error {
	if len(svg) == 0 {
		return errors.New("empty")
	}
	dec := xml.NewDecoder(bytes.NewReader(svg))
	dec.Strict = true
	root := true
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("not well-formed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			if root && name != "svg" {
				return fmt.Errorf("root element is <%s>, not <svg>", t.Name.Local)
			}
			root = false
			switch name {
			case "script", "foreignobject", "image", "style", "iframe", "object", "embed", "a", "animate", "set",
				"animatetransform", "animatemotion", "handler", "feimage":
				return fmt.Errorf("forbidden element <%s>", t.Name.Local)
			}
			for _, a := range t.Attr {
				attr, val := strings.ToLower(a.Name.Local), strings.ToLower(a.Value)
				if strings.HasPrefix(attr, "on") {
					return fmt.Errorf("event handler attribute %s", a.Name.Local)
				}
				if (attr == "href" || attr == "src") && !strings.HasPrefix(val, "#") {
					return fmt.Errorf("reference to %q", a.Value)
				}
				if attr == "style" {
					return errors.New("style attribute")
				}
				if strings.Contains(val, "url(") && !onlyLocalURLs(val) {
					return fmt.Errorf("external url() in %s", a.Name.Local)
				}
				if strings.Contains(val, "javascript:") || strings.Contains(val, "data:") || strings.Contains(val, "@import") {
					return fmt.Errorf("forbidden value in %s", a.Name.Local)
				}
			}
		case xml.ProcInst:
			if t.Target != "xml" {
				return fmt.Errorf("processing instruction %q", t.Target)
			}
		case xml.Directive:
			return errors.New("DOCTYPE or other directive")
		}
	}
	if root {
		return errors.New("no <svg> element")
	}
	return nil
}

// onlyLocalURLs reports whether every url(...) in v points inside the document (url(#id)).
func onlyLocalURLs(v string) bool {
	for {
		i := strings.Index(v, "url(")
		if i < 0 {
			return true
		}
		v = strings.TrimLeft(v[i+len("url("):], " '\"")
		if !strings.HasPrefix(v, "#") {
			return false
		}
	}
}
