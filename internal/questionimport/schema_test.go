package questionimport

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

// TestSchemaAgreesWithValidator keeps schema/question_schema.json, the
// published contract for hand-written files, in line with the Go rules.
// Known gap: JSON Schema treats 10.0 as an integer, Parse does not, so no
// fixture uses it.
func TestSchemaAgreesWithValidator(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "schema", "question_schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	const url = "mem://question_schema.json"
	c := jsonschema.NewCompiler()
	if err := c.AddResource(url, bytes.NewReader(b)); err != nil {
		t.Fatal(err)
	}
	schema, err := c.Compile(url)
	if err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{"valid", "invalid"} {
		files, err := filepath.Glob(filepath.Join("testdata", dir, "*.json"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no fixtures in testdata/%s: %v", dir, err)
		}
		for _, f := range files {
			t.Run(dir+"/"+filepath.Base(f), func(t *testing.T) {
				data, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				var v any
				if err := json.Unmarshal(data, &v); err != nil {
					t.Fatal(err)
				}
				schemaValid := schema.Validate(v) == nil
				_, errs := Parse(data)
				goValid := len(errs) == 0
				want := dir == "valid"
				if schemaValid != want || goValid != want {
					t.Errorf("schema valid = %v, Go valid = %v (errors %v), want %v", schemaValid, goValid, keys(errs), want)
				}
			})
		}
	}
}
