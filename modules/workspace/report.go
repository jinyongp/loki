package workspace

import (
	"encoding/xml"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"loki/internal/fault"
)

func (f *Files) TestReport(path string) (string, map[string]any, error) {
	data, _, err := f.read(path, min(f.Config.MaxFileBytes, 1024*1024))
	if err != nil {
		return "", nil, err
	}
	if !utf8.Valid(data) {
		return "", nil, fault.Error("test report must be UTF-8 text")
	}
	content := strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	if ext == "" {
		ext = "text"
	}
	stats := map[string]any{"format": ext}
	if ext != "xml" && !strings.HasPrefix(strings.TrimSpace(content), "<testsuite") {
		return content, stats, nil
	}
	stats = map[string]any{"format": "junit", "tests": 0, "failures": 0, "errors": 0, "skipped": 0}
	decoder := xml.NewDecoder(strings.NewReader(content))
	depth := 0
	rootSuite := false
	roots := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, fault.Error("invalid JUnit XML")
		}
		switch element := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				rootSuite = element.Name.Space == "" && element.Name.Local == "testsuite"
			}
			depth++
			if element.Name.Space != "" || element.Name.Local != "testsuite" || rootSuite && depth != 1 {
				continue
			}
			for _, attr := range element.Attr {
				if attr.Name.Space != "" {
					continue
				}
				switch attr.Name.Local {
				case "tests", "failures", "errors", "skipped":
					value, err := strconv.Atoi(attr.Value)
					if err != nil {
						return "", nil, fault.Error("invalid JUnit count")
					}
					sum := stats[attr.Name.Local].(int)
					if value > 0 && sum+value < sum || value < 0 && sum+value > sum {
						return "", nil, fault.Error("JUnit count exceeds limit")
					}
					stats[attr.Name.Local] = sum + value
				}
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(element)) != "" {
				return "", nil, fault.Error("invalid JUnit XML")
			}
		}
	}
	if roots != 1 || depth != 0 {
		return "", nil, fault.Error("invalid JUnit XML")
	}
	return content, stats, nil
}
