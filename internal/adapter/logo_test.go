package adapter

import "testing"

func TestCheckLogo(t *testing.T) {
	good := []string{
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M0 0h24v24z"/></svg>`,
		`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><mask id="m"><circle r="4" fill="#fff"/></mask>` +
			`<rect width="9" height="9" mask="url(#m)"/></svg>`,
	}
	for _, s := range good {
		if err := CheckLogo([]byte(s)); err != nil {
			t.Errorf("CheckLogo(%q) = %v, want nil", s, err)
		}
	}
	bad := []string{
		``,
		`<svg`,
		`<html><svg/></html>`,
		`<svg><script>alert(1)</script></svg>`,
		`<svg><foreignObject><div/></foreignObject></svg>`,
		`<svg><image href="x.png"/></svg>`,
		`<svg><style>@import url(x.css);</style></svg>`,
		`<svg onload="alert(1)"/>`,
		`<svg><use href="https://example.com/a.svg#x"/></svg>`,
		`<svg xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="other.svg#x"/></svg>`,
		`<svg><rect fill="url(https://example.com/p)"/></svg>`,
		`<svg><rect style="fill:red"/></svg>`,
		`<svg><a href="#x"><rect/></a></svg>`,
		`<svg><rect fill="data:image/png;base64,AA"/></svg>`,
		`<!DOCTYPE svg [<!ENTITY x "y">]><svg/>`,
		`<svg><animate attributeName="href" to="javascript:alert(1)"/></svg>`,
	}
	for _, s := range bad {
		if err := CheckLogo([]byte(s)); err == nil {
			t.Errorf("CheckLogo(%q) = nil, want an error", s)
		}
	}
}
