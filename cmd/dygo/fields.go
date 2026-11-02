package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// fieldType is a type a generated entity field can have.
type fieldType struct {
	Go      string // the Go type
	Import  string // the package the Go type needs
	Sample  string // a valid Go value for tests
	Example string // the example tag for the OpenAPI document
}

var fieldTypes = map[string]fieldType{
	"string": {Go: "string", Sample: `"%s 1"`, Example: "%s 1"},
	"text":   {Go: "string", Sample: `"%s 1"`, Example: "%s 1"},
	"email":  {Go: "string", Sample: `"ada@example.com"`, Example: "ada@example.com"},
	"int":    {Go: "int", Sample: "1", Example: "1"},
	"int64":  {Go: "int64", Sample: "1", Example: "1"},
	"float":  {Go: "float64", Sample: "1.5", Example: "1.5"},
	"bool":   {Go: "bool", Sample: "true", Example: "true"},
	"time":   {Go: "time.Time", Import: "time", Sample: "time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)"},
}

// fieldTypeNames lists the types in the order the prompt shows them.
var fieldTypeNames = []string{"string", "text", "email", "int", "int64", "float", "bool", "time"}

// entityField is one field of a generated entity: name:type, with a trailing
// ? when it is optional.
type entityField struct {
	Name     string // Go name: CreatedAt
	JSON     string // JSON name: createdAt
	Type     string // a key of fieldTypes
	Optional bool
}

// parseFields parses "name:string email:email age:int?" (commas work too).
func parseFields(spec string) ([]entityField, error) {
	var out []entityField
	seen := map[string]bool{"ID": true}
	for _, item := range strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == ' ' }) {
		name, typ, _ := strings.Cut(item, ":")
		optional := strings.HasSuffix(name, "?") || strings.HasSuffix(typ, "?")
		name, typ = strings.TrimSuffix(name, "?"), strings.TrimSuffix(typ, "?")
		f, err := newField(name, typ, optional)
		if err != nil {
			return nil, err
		}
		if seen[f.Name] {
			return nil, fmt.Errorf("field %s given twice (id is always there)", f.JSON)
		}
		seen[f.Name] = true
		out = append(out, f)
	}
	return out, nil
}

func newField(name, typ string, optional bool) (entityField, error) {
	if typ == "" {
		typ = "string"
	}
	if _, ok := fieldTypes[typ]; !ok {
		return entityField{}, fmt.Errorf("field %s: unknown type %q (want %s)", name, typ, strings.Join(fieldTypeNames, ", "))
	}
	words := splitWords(name)
	if len(words) == 0 || words[0][0] < 'a' || words[0][0] > 'z' {
		return entityField{}, fmt.Errorf("invalid field name %q", name)
	}
	return entityField{Name: pascal(words), JSON: words[0] + pascal(words[1:]), Type: typ, Optional: optional}, nil
}

func (f entityField) ft() fieldType { return fieldTypes[f.Type] }

// EntityField is the field in the entity struct.
func (f entityField) EntityField() string {
	omit := ""
	if f.Optional {
		omit = ",omitempty"
	}
	return fmt.Sprintf("%s %s `json:\"%s%s\"`", f.Name, f.ft().Go, f.JSON, omit)
}

// CreateField is the field in the create body.
func (f entityField) CreateField() string {
	tag := fmt.Sprintf("json:\"%s\"", f.JSON)
	if f.Optional {
		tag = fmt.Sprintf("json:\"%s,omitempty\"", f.JSON)
	}
	if ex := f.ft().Example; ex != "" {
		if strings.Contains(ex, "%s") {
			ex = fmt.Sprintf(ex, f.JSON)
		}
		tag += fmt.Sprintf(" example:\"%s\"", ex)
	}
	return fmt.Sprintf("%s %s `%s`", f.Name, f.ft().Go, tag)
}

// UpdateField is the field in the update body: a pointer, nil when not sent.
func (f entityField) UpdateField() string {
	return fmt.Sprintf("%s *%s `json:\"%s,omitempty\"`", f.Name, f.ft().Go, f.JSON)
}

// CreateCheck is the create body's validation of the field, or "".
func (f entityField) CreateCheck(recv string) string {
	v := recv + "." + f.Name
	switch {
	case f.Type == "email" && f.Optional:
		return fmt.Sprintf("if %s != \"\" && !isEmail(%s) {\nv.Add(\"body\", %q, \"must be an email address\")\n}", v, v, f.JSON)
	case f.Type == "email":
		return fmt.Sprintf("if !isEmail(%s) {\nv.Add(\"body\", %q, \"must be an email address\")\n}", v, f.JSON)
	case f.Optional:
		return ""
	case f.Type == "string" || f.Type == "text":
		return fmt.Sprintf("if strings.TrimSpace(%s) == \"\" {\nv.Add(\"body\", %q, \"must not be empty\")\n}", v, f.JSON)
	case f.Type == "time":
		return fmt.Sprintf("if %s.IsZero() {\nv.Add(\"body\", %q, \"is required\")\n}", v, f.JSON)
	}
	return ""
}

// UpdateCheck is the update body's validation of the field, or "".
func (f entityField) UpdateCheck(recv string) string {
	v := recv + "." + f.Name
	switch {
	case f.Type == "email" && f.Optional:
		return fmt.Sprintf("if %s != nil && *%s != \"\" && !isEmail(*%s) {\nv.Add(\"body\", %q, \"must be an email address\")\n}", v, v, v, f.JSON)
	case f.Type == "email":
		return fmt.Sprintf("if %s != nil && !isEmail(*%s) {\nv.Add(\"body\", %q, \"must be an email address\")\n}", v, v, f.JSON)
	case f.Optional:
		return ""
	case f.Type == "string" || f.Type == "text":
		return fmt.Sprintf("if %s != nil && strings.TrimSpace(*%s) == \"\" {\nv.Add(\"body\", %q, \"must not be empty\")\n}", v, v, f.JSON)
	case f.Type == "time":
		return fmt.Sprintf("if %s != nil && %s.IsZero() {\nv.Add(\"body\", %q, \"must not be zero\")\n}", v, v, f.JSON)
	}
	return ""
}

// EntityElt copies the field from the create body into the new entity.
func (f entityField) EntityElt(recv string) string {
	return fmt.Sprintf("%s: %s.%s,", f.Name, recv, f.Name)
}

// ApplyLine copies the field from the update body when it was sent.
func (f entityField) ApplyLine(recv, entity string) string {
	return fmt.Sprintf("if %s.%s != nil {\n%s.%s = *%s.%s\n}", recv, f.Name, entity, f.Name, recv, f.Name)
}

// Sample is a valid value for tests.
func (f entityField) Sample() string {
	s := f.ft().Sample
	if strings.Contains(s, "%s") {
		s = fmt.Sprintf(s, f.JSON)
	}
	return s
}

// SampleElt is the field in a test's create body literal.
func (f entityField) SampleElt() string { return fmt.Sprintf("%s: %s,", f.Name, f.Sample()) }

// IsText reports a string field that a PATCH test can change.
func (f entityField) IsText() bool { return f.Type == "string" || f.Type == "text" }

// fieldSet is what the entity templates see about the fields.
type fieldSet []entityField

// Imports are the standard packages the entity file needs.
func (fs fieldSet) Imports() []string {
	var out []string
	for _, f := range fs {
		if f.CreateCheck("b")+f.UpdateCheck("b") != "" && f.Type != "email" && f.IsText() {
			out = append(out, "strings")
		}
		if f.Type == "email" {
			out = append(out, "net/mail")
		}
		if f.ft().Import != "" {
			out = append(out, f.ft().Import)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// HasEmail reports whether the entity file needs the isEmail helper.
func (fs fieldSet) HasEmail() bool {
	return slices.ContainsFunc(fs, func(f entityField) bool { return f.Type == "email" })
}

// HasRequired reports whether an empty create body fails validation.
func (fs fieldSet) HasRequired() bool {
	return slices.ContainsFunc(fs, func(f entityField) bool { return !f.Optional && f.CreateCheck("b") != "" })
}

// TestImports are the packages the test's sample values need.
func (fs fieldSet) TestImports() []string {
	var out []string
	for _, f := range fs {
		if f.ft().Import != "" {
			out = append(out, f.ft().Import)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// PatchField is the first text field, which the generated test changes.
func (fs fieldSet) PatchField() *entityField {
	for i := range fs {
		if fs[i].IsText() {
			return &fs[i]
		}
	}
	return nil
}

// isTerminal reports whether f is an interactive terminal.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// fieldTypeHelp describes each type for the prompt's "?" answer.
var fieldTypeHelp = map[string]string{
	"string": "short text, required means not empty",
	"text":   "long text (same Go type, no example length)",
	"email":  "a string that must be an email address",
	"int":    "whole number",
	"int64":  "large whole number",
	"float":  "decimal number (float64)",
	"bool":   "true or false",
	"time":   "date and time (time.Time, RFC 3339 in JSON)",
}

// guessType suggests a type from the field name.
func guessType(name string) string {
	words := splitWords(name)
	if len(words) == 0 {
		return "string"
	}
	first, last := words[0], words[len(words)-1]
	switch {
	case last == "at" || last == "date" || last == "time" || last == "until" || last == "since":
		return "time"
	case last == "email":
		return "email"
	case len(words) > 1 && (first == "is" || first == "has" || first == "can"), last == "active", last == "enabled":
		return "bool"
	case slices.Contains([]string{"count", "qty", "quantity", "stock", "age", "position", "rank", "year"}, last):
		return "int"
	case slices.Contains([]string{"price", "amount", "total", "rate", "score", "weight", "lat", "lng", "latitude", "longitude"}, last):
		return "float"
	case slices.Contains([]string{"description", "content", "body", "notes", "bio", "summary"}, last):
		return "text"
	}
	return "string"
}

// promptFields asks for fields in a loop until the user
// presses Enter on an empty name. existing are the Go names already in the
// entity, when fields are being added to it.
func promptFields(in io.Reader, out io.Writer, entity string, existing []string) ([]entityField, error) {
	sc := bufio.NewScanner(in)
	ask := func(q, def string) (string, bool) {
		if def != "" {
			q += " [" + def + "]"
		}
		fmt.Fprintf(out, " %s:\n > ", q)
		if !sc.Scan() {
			fmt.Fprintln(out)
			return "", false
		}
		if a := strings.TrimSpace(sc.Text()); a != "" {
			return a, true
		}
		return def, true
	}
	seen := map[string]bool{"ID": true}
	for _, n := range existing {
		seen[n] = true
	}
	if existing != nil {
		fmt.Fprintf(out, "\n Your entity %s already exists, so let's add some new fields!\n Current fields: %s\n", entity, strings.Join(append([]string{"id"}, jsonNames(existing)...), ", "))
	} else {
		fmt.Fprintf(out, "\n New entity %s. It gets an id field for you.\n", entity)
	}
	var fields []entityField
	for {
		q := "New field name (press <return> to stop adding fields)"
		if len(fields) > 0 {
			q = "Add another field? Enter the field name (or press <return> to stop adding fields)"
		}
		fmt.Fprintln(out)
		name, ok := ask(q, "")
		if !ok || name == "" {
			return fields, sc.Err()
		}
		probe, err := newField(name, "string", false)
		if err != nil {
			fmt.Fprintf(out, " [ERROR] %v\n", err)
			continue
		}
		if seen[probe.Name] {
			fmt.Fprintf(out, " [ERROR] %s is already a field\n", probe.JSON)
			continue
		}
		var typ string
		for {
			if typ, ok = ask("Field type (enter ? to see all types)", guessType(name)); !ok {
				return fields, sc.Err()
			}
			if typ == "?" {
				fmt.Fprintln(out, "\n Types:")
				for _, t := range fieldTypeNames {
					fmt.Fprintf(out, "  * %-7s %s\n", t, fieldTypeHelp[t])
				}
				fmt.Fprintln(out)
				continue
			}
			if _, known := fieldTypes[typ]; known {
				break
			}
			fmt.Fprintf(out, " [ERROR] unknown type %q, enter ? to see all types\n", typ)
		}
		opt, ok := ask("Can this field be empty (optional) (yes/no)", "no")
		if !ok {
			return fields, sc.Err()
		}
		f, _ := newField(name, typ, strings.HasPrefix(strings.ToLower(opt), "y"))
		seen[f.Name] = true
		fields = append(fields, f)
		req := "required"
		if f.Optional {
			req = "optional"
		}
		fmt.Fprintf(out, " added: %s %s (%s)\n", f.JSON, f.Type, req)
	}
}

// jsonNames turns Go field names into their JSON spelling.
func jsonNames(goNames []string) []string {
	out := make([]string, len(goNames))
	for i, n := range goNames {
		w := splitWords(n)
		out[i] = w[0] + pascal(w[1:])
	}
	return out
}
