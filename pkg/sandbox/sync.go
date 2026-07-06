package sandbox

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ZipDir reads all files in the directory recursively (ignoring '.git',
// binaries like 'kiwi' or 'kiwid', and temporary files), compresses them
// into a ZIP archive, and returns the raw archive bytes.
func ZipDir(srcDir string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Always skip .git directory entirely
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}

		// Calculate relative path
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		name := d.Name()

		// Ignore binaries like kiwi or kiwid
		if name == "kiwi" || name == "kiwid" || name == "kiwi.exe" || name == "kiwid.exe" {
			return nil
		}

		// Ignore temporary, build, and workspace/debug files
		if strings.HasSuffix(name, ".tmp") ||
			strings.HasSuffix(name, "~") ||
			strings.HasPrefix(name, ".~") ||
			strings.HasSuffix(name, ".swp") ||
			strings.HasSuffix(name, ".out") ||
			strings.HasSuffix(name, ".test") ||
			name == ".DS_Store" ||
			name == "task_state.json" ||
			name == "secrets.json" {
			return nil
		}

		// Get file info for headers
		info, err := d.Info()
		if err != nil {
			return err
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}

		// Use the relative path inside the zip file
		header.Name = filepath.ToSlash(relPath)
		if d.IsDir() {
			header.Name += "/"
			header.Method = zip.Store
		} else {
			header.Method = zip.Deflate
		}

		writer, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}

		if !d.IsDir() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()

			_, err = io.Copy(writer, file)
			if err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// UnzipBytes reads the ZIP bytes and extracts all files recursively
// into the destination directory (creating folders as needed).
func UnzipBytes(destDir string, zipData []byte) error {
	reader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return err
	}

	cleanDestDir := filepath.Clean(destDir)

	for _, f := range reader.File {
		// Clean and join paths to construct the target path
		destPath := filepath.Join(cleanDestDir, f.Name)

		// Prevent Zip Slip (directory traversal attack)
		cleanDestPath := filepath.Clean(destPath)
		absDestDir, err := filepath.Abs(cleanDestDir)
		if err != nil {
			return err
		}
		absDestPath, err := filepath.Abs(cleanDestPath)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(absDestPath, absDestDir+string(filepath.Separator)) && absDestPath != absDestDir {
			return fmt.Errorf("illegal file path in zip: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			mode := f.Mode()
			if mode.Perm() == 0 {
				mode = 0755
			}
			err := os.MkdirAll(cleanDestPath, mode)
			if err != nil {
				return err
			}
			continue
		}

		// Create parent directories if they don't exist
		err = os.MkdirAll(filepath.Dir(cleanDestPath), 0755)
		if err != nil {
			return err
		}

		// Open and extract file
		rc, err := f.Open()
		if err != nil {
			return err
		}

		// Create destination file with permissions from zip
		mode := f.Mode()
		if mode.Perm() == 0 {
			mode = 0644
		}
		outFile, err := os.OpenFile(cleanDestPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}

	return nil
}
