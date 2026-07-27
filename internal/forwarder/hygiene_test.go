package forwarder

// hygiene_test.go asserts repository-level facts that the property tests cannot
// express: that removed metric names, the deleted ARN-trimming helper, Datadog
// credential/endpoint configuration, AWS SDK or SQS clients, and any
// re-marshalling of the Event_Payload are all absent from the module's Go
// sources.
//
// Scan scope: the whole module, not just this package. The scanner locates the
// module root by walking up to go.mod and then walks every *.go file beneath it,
// so the root main.go entry point and any future package are covered by the same
// rules as this one. Requirements 1.4, 2.4, and 7.5 are obligations on the
// Forwarder's source, wherever in the module it lives.
//
// Self-reference: a source scanner that lives in the package it scans would
// normally flag itself, because it has to name the very literals it forbids.
// Rather than skipping this file by name — which would leave a blind spot
// exactly where forbidden text is most likely to be copied from — every
// forbidden literal below is assembled at run time from fragments by
// literalOf(). No forbidden string ever appears verbatim in this source, so
// every Go file in the package, this one included, is scanned under the same
// rules.
//
// Scope note: the removed-metric-name, ARN-helper, and Datadog-configuration
// checks cover every *.go file, source and test alike (Requirements 1.4, 2.4,
// 7.5). The AWS SDK / SQS import check, the environment-read check, and the
// payload re-marshalling check cover the non-test sources, because they are
// obligations on the code paths the Forwarder actually runs (Requirements 3.4,
// 7.5, DR-001); test helpers are free to construct payload bytes however they
// need to.

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// literalOf joins fragments into a forbidden literal at run time, so the
// literal is never present verbatim in this file's own bytes.
func literalOf(fragments ...string) string {
	return strings.Join(fragments, "")
}

// goSource is one scanned file: its module-relative path and its full contents.
type goSource struct {
	Name    string
	Content string
}

// moduleRoot walks up from the test's working directory until it finds the
// directory holding go.mod, so the scan covers the module rather than whichever
// package the test happens to live in.
func moduleRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolving working directory: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found in any parent directory; cannot locate the module root")
		}
		dir = parent
	}
}

// packageGoSources reads every *.go file in the module, keyed by its path
// relative to the module root. Build output and vendored code are skipped.
func packageGoSources(t *testing.T) []goSource {
	t.Helper()

	root := moduleRoot(t)

	var sources []goSource
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "vendor", "dist", ".git":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sources = append(sources, goSource{Name: relative, Content: string(content)})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module for Go sources: %v", err)
	}

	if len(sources) == 0 {
		t.Fatal("no *.go files found in the module; the hygiene scan would pass vacuously")
	}
	// The scanner must at least see the entry point and the handler.
	for _, required := range []string{"main.go", filepath.Join("internal", "forwarder", "handler.go")} {
		if !containsFile(sources, required) {
			t.Fatalf("%s not among scanned files: %v", required, fileNames(sources))
		}
	}
	return sources
}

func containsFile(sources []goSource, name string) bool {
	for _, source := range sources {
		if source.Name == name {
			return true
		}
	}
	return false
}

func fileNames(sources []goSource) []string {
	names := make([]string, 0, len(sources))
	for _, source := range sources {
		names = append(names, source.Name)
	}
	return names
}

func nonTestSources(sources []goSource) []goSource {
	var production []goSource
	for _, source := range sources {
		if strings.HasSuffix(source.Name, "_test.go") {
			continue
		}
		production = append(production, source)
	}
	return production
}

// occurrences reports the 1-based line numbers on which literal appears.
func occurrences(content, literal string) []int {
	var lines []int
	for i, line := range strings.Split(content, "\n") {
		if strings.Contains(line, literal) {
			lines = append(lines, i+1)
		}
	}
	return lines
}

// assertAbsent fails the test once per (file, literal) pair that matches,
// naming the literal by its description so the failure message itself does not
// reintroduce the forbidden text.
func assertAbsent(t *testing.T, sources []goSource, description, literal string) {
	t.Helper()
	for _, source := range sources {
		if lines := occurrences(source.Content, literal); len(lines) > 0 {
			t.Errorf("%s must not appear in Go sources, found in %s at line(s) %v",
				description, source.Name, lines)
		}
	}
}

// TestRemovedMetricNamesAreAbsentFromSources checks that the three retired
// metric names survive nowhere in the package's source or test files, so
// aws.health.events.duration is the only custom metric name present.
//
// Requirements: 1.4
func TestRemovedMetricNamesAreAbsentFromSources(t *testing.T) {
	sources := packageGoSources(t)

	prefix := literalOf("aws.", "health.", "issue.")
	removed := map[string]string{
		"the removed received metric name":          literalOf(prefix, "received"),
		"the removed alert_status metric name":      literalOf(prefix, "alert_status"),
		"the removed duration_seconds metric name":  literalOf(prefix, "duration_seconds"),
		"the removed aws.health.issue metric group": prefix,
	}
	for description, literal := range removed {
		assertAbsent(t, sources, description, literal)
	}

	// Guard against a vacuous pass: the retained metric name must be present.
	retained := literalOf("aws.", "health.", "events.", "duration")
	found := false
	for _, source := range sources {
		if strings.Contains(source.Content, retained) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("the retained duration metric name is absent from all Go sources; the scan cannot be trusted")
	}
}

// TestArnTrimmingHelperIsAbsentFromSources checks that the deleted
// ARN-shortening helper is gone from every Go file, so nothing in the package
// can shorten detail.eventArn.
//
// Requirements: 2.4
func TestArnTrimmingHelperIsAbsentFromSources(t *testing.T) {
	sources := packageGoSources(t)

	assertAbsent(t, sources, "the deleted ARN-trimming helper", literalOf("eventArn", "TagValue"))
}

// TestNoDatadogCredentialsOrEndpointInSources checks that no Datadog API key,
// site, or metrics-endpoint value appears in the package's Go files, and that
// no non-test source reads such a value from the environment. Datadog
// authentication and client configuration come entirely from the Datadog Lambda
// layer.
//
// Requirements: 7.5
func TestNoDatadogCredentialsOrEndpointInSources(t *testing.T) {
	sources := packageGoSources(t)

	forbiddenValues := map[string]string{
		"the Datadog API key environment variable": literalOf("DD_", "API_", "KEY"),
		"the Datadog site environment variable":    literalOf("DD_", "SITE"),
		"a Datadog site or endpoint host":          literalOf("datadoghq", "."),
		"the DogStatsD metrics endpoint":           literalOf("127.0.0.1", ":8125"),
	}
	for description, literal := range forbiddenValues {
		assertAbsent(t, sources, description, literal)
	}

	production := nonTestSources(sources)
	environmentReads := map[string]string{
		"an environment variable read":   literalOf("os.", "Getenv"),
		"an environment variable lookup": literalOf("os.", "LookupEnv"),
		"an environment listing":         literalOf("os.", "Environ"),
	}
	for description, literal := range environmentReads {
		assertAbsent(t, production, description, literal)
	}
}

// TestNoSQSOrAWSSDKClientImports parses the import declarations of every
// non-test source and asserts that neither an AWS SDK module nor an SQS client
// is imported. DLQ delivery is a property of the invocation result, not of code
// the Forwarder runs.
//
// Requirements: 3.4
func TestNoSQSOrAWSSDKClientImports(t *testing.T) {
	forbiddenImports := map[string]string{
		"an AWS SDK for Go module": literalOf("aws-", "sdk-go"),
		"an SQS client package":    literalOf("sq", "s"),
	}

	fset := token.NewFileSet()
	for _, source := range nonTestSources(packageGoSources(t)) {
		file, err := parser.ParseFile(fset, source.Name, source.Content, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing imports of %s: %v", source.Name, err)
		}
		for _, imported := range file.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("unquoting import path in %s: %v", source.Name, err)
			}
			for description, fragment := range forbiddenImports {
				if strings.Contains(strings.ToLower(path), fragment) {
					t.Errorf("%s must not be imported, found %q in %s", description, path, source.Name)
				}
			}
		}
	}
}

// TestNoPayloadReMarshallingInProductionSources checks that no non-test source
// serializes anything: the Event_Payload reaches the DLQ byte-for-byte because
// there is no code path able to re-encode it.
//
// Requirements: 3.4
func TestNoPayloadReMarshallingInProductionSources(t *testing.T) {
	production := nonTestSources(packageGoSources(t))

	forbidden := map[string]string{
		"JSON marshalling":         literalOf("json.", "Marshal"),
		"a JSON encoder":           literalOf("json.", "NewEncoder"),
		"any encoder construction": literalOf("New", "Encoder("),
		"an encode call":           literalOf(".Encode", "("),
	}
	for description, literal := range forbidden {
		assertAbsent(t, production, description, literal)
	}
}
