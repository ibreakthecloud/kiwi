package sandbox

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZipAndUnzip(t *testing.T) {
	// Create a temporary directory for testing source
	srcDir, err := os.MkdirTemp("", "kiwi-sync-test-src-*")
	if err != nil {
		t.Fatalf("failed to create temp src dir: %v", err)
	}
	defer os.RemoveAll(srcDir)

	// Create a nested file structure
	filesToCreate := map[string]string{
		"file1.txt":            "hello world",
		"sub/file2.txt":        "nested hello",
		"sub/nested/file3.txt": "deep nested hello",
		".git/config":          "should be ignored",
		"kiwi":                 "binary ignored",
		"kiwid":                "binary ignored",
		"temp.tmp":             "temp ignored",
		"backup~":              "backup ignored",
		".~lock":               "lock ignored",
		"some.swp":             "swp ignored",
		"output.out":           "out ignored",
		"test.test":            "test ignored",
		".DS_Store":            "ds store ignored",
		"task_state.json":      "task state ignored",
		"secrets.json":         "secrets ignored",
	}

	for relPath, content := range filesToCreate {
		fullPath := filepath.Join(srcDir, relPath)
		err := os.MkdirAll(filepath.Dir(fullPath), 0755)
		if err != nil {
			t.Fatalf("failed to create parent dir: %v", err)
		}
		err = os.WriteFile(fullPath, []byte(content), 0644)
		if err != nil {
			t.Fatalf("failed to write file %s: %v", relPath, err)
		}
	}

	// Zip the directory
	zipData, err := ZipDir(srcDir)
	if err != nil {
		t.Fatalf("ZipDir failed: %v", err)
	}

	if len(zipData) == 0 {
		t.Fatalf("ZipDir returned empty bytes")
	}

	// Create a temporary directory for testing destination
	destDir, err := os.MkdirTemp("", "kiwi-sync-test-dest-*")
	if err != nil {
		t.Fatalf("failed to create temp dest dir: %v", err)
	}
	defer os.RemoveAll(destDir)

	// Unzip the bytes
	err = UnzipBytes(destDir, zipData)
	if err != nil {
		t.Fatalf("UnzipBytes failed: %v", err)
	}

	// Verify files that SHOULD exist
	expectedFiles := map[string]string{
		"file1.txt":            "hello world",
		"sub/file2.txt":        "nested hello",
		"sub/nested/file3.txt": "deep nested hello",
	}

	for relPath, expectedContent := range expectedFiles {
		fullPath := filepath.Join(destDir, relPath)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			t.Errorf("expected file %s does not exist or cannot be read: %v", relPath, err)
			continue
		}
		if string(data) != expectedContent {
			t.Errorf("file %s has content %q, expected %q", relPath, string(data), expectedContent)
		}
	}

	// Verify files that SHOULD NOT exist
	ignoredFiles := []string{
		".git/config",
		"kiwi",
		"kiwid",
		"temp.tmp",
		"backup~",
		".~lock",
		"some.swp",
		"output.out",
		"test.test",
		".DS_Store",
		"task_state.json",
		"secrets.json",
	}

	for _, relPath := range ignoredFiles {
		fullPath := filepath.Join(destDir, relPath)
		if _, err := os.Stat(fullPath); !os.IsNotExist(err) {
			t.Errorf("file %s should have been ignored, but it exists", relPath)
		}
	}
}

func TestUnzipZipSlipProtection(t *testing.T) {
	// Create an in-memory zip file that attempts a Zip Slip
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	// Create a file entry that goes up to parent directory
	header := &zip.FileHeader{
		Name:   "../escaped.txt",
		Method: zip.Store,
	}
	writer, err := zw.CreateHeader(header)
	if err != nil {
		t.Fatalf("failed to create header: %v", err)
	}
	_, err = writer.Write([]byte("escaped!"))
	if err != nil {
		t.Fatalf("failed to write content: %v", err)
	}
	zw.Close()

	destDir, err := os.MkdirTemp("", "kiwi-sync-test-slip-*")
	if err != nil {
		t.Fatalf("failed to create temp dest dir: %v", err)
	}
	defer os.RemoveAll(destDir)

	err = UnzipBytes(destDir, buf.Bytes())
	if err == nil {
		t.Errorf("expected UnzipBytes to fail with Zip Slip file, but it succeeded")
	} else if !strings.Contains(err.Error(), "illegal file path in zip") {
		t.Errorf("expected Zip Slip error message, got: %v", err)
	}

	// Verify the file was not written outside the temp directory
	parentEscapedPath := filepath.Clean(filepath.Join(destDir, "../escaped.txt"))
	if _, err := os.Stat(parentEscapedPath); !os.IsNotExist(err) {
		t.Errorf("Zip Slip file was successfully written outside target directory!")
		os.Remove(parentEscapedPath)
	}
}
