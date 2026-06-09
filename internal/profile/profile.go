// Package profile turns a Chromium user-data-dir (a tree of cookies/storage/leveldb)
// into a single tar blob so it can be sealed as one AES-256-GCM ciphertext, and back.
// Caches and other regenerable cruft are excluded to keep the seal small. Pure stdlib.
package profile

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// excludedDirs are profile subpaths Chromium regenerates — dropping them shrinks the
// seal dramatically and is safe (they hold no identity/session state).
var excludedDirs = []string{
	"Cache", "Code Cache", "GPUCache", "ShaderCache", "GrShaderCache", "DawnCache",
	"DawnGraphiteCache", "DawnWebGPUCache", "blob_storage", "Crashpad",
	filepath.Join("Service Worker", "CacheStorage"),
	filepath.Join("Service Worker", "ScriptCache"),
}

func excluded(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, ex := range excludedDirs {
		ex = filepath.ToSlash(ex)
		if rel == ex || strings.Contains(rel, "/"+ex+"/") || strings.HasPrefix(rel, ex+"/") {
			return true
		}
	}
	// transient lock/temp files
	base := filepath.Base(rel)
	if base == "SingletonLock" || base == "SingletonSocket" || base == "SingletonCookie" ||
		strings.HasSuffix(base, ".tmp") {
		return true
	}
	return false
}

// Tar walks dir and returns an uncompressed tar of its contents (paths relative to
// dir), skipping excluded subtrees, symlinks, sockets, and other non-regular files.
func Tar(dir string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			// A file may vanish mid-walk (Chromium churn) — skip it, don't fail the seal.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if excluded(rel) {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if fi.IsDir() {
			hdr := &tar.Header{Name: filepath.ToSlash(rel) + "/", Mode: 0o700, Typeflag: tar.TypeDir}
			return tw.WriteHeader(hdr)
		}
		if !fi.Mode().IsRegular() {
			return nil // skip symlinks/sockets/devices
		}
		hdr := &tar.Header{Name: filepath.ToSlash(rel), Mode: 0o600, Size: fi.Size(), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, oerr := os.Open(path)
		if oerr != nil {
			if os.IsNotExist(oerr) {
				return nil
			}
			return oerr
		}
		defer f.Close()
		// Copy at most hdr.Size bytes so a file that grows mid-walk doesn't corrupt the tar.
		_, cerr := io.CopyN(tw, f, fi.Size())
		if cerr == io.EOF {
			return nil
		}
		return cerr
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Untar writes a tar blob's contents into dir (created if missing), guarding against
// path traversal.
func Untar(blob []byte, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tr := tar.NewReader(bytes.NewReader(blob))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.FromSlash(hdr.Name)
		target := filepath.Join(dir, name)
		// path-traversal guard: target must stay within dir
		if rel, rerr := filepath.Rel(dir, target); rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
}
