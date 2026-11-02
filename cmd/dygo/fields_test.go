package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseFields(t *testing.T) {
	fs, err := parseFields("title, price:float published-at:time? contact:email")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range fs {
		got = append(got, f.EntityField())
	}
	want := "Title string `json:\"title\"`|Price float64 `json:\"price\"`|PublishedAt time.Time `json:\"publishedAt,omitempty\"`|Contact string `json:\"contact\"`"
	if strings.Join(got, "|") != want {
		t.Fatalf("got  %s\nwant %s", strings.Join(got, "|"), want)
	}
	for _, bad := range []string{"a:money", "id:int", "x x", "1a"} {
		if _, err := parseFields(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestPromptFields(t *testing.T) {
	// title: guessed string, required; price: "?" lists types, money is
	// refused, then float, optional; title again is refused; sku is new.
	in := strings.NewReader("title\n\n\nprice\n?\nmoney\n\ny\ntitle\nsku\n\n\n\n")
	var out bytes.Buffer
	fs, err := promptFields(in, &out, "Product", []string{"Sku"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 || fs[0].Name != "Title" || fs[1].Type != "float" || !fs[1].Optional {
		t.Fatalf("fields %+v\n%s", fs, out.String())
	}
	for _, want := range []string{"already exists", "Current fields: id, sku", "Field type (enter ? to see all types) [float]", "* time", "unknown type \"money\"", "title is already a field", "sku is already a field", "added: price float (optional)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
}
