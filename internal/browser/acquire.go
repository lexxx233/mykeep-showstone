package browser

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// PinnedChromeVersion is the exact Chrome-for-Testing build Showstone uses. CfT builds
// never auto-update; bump this deliberately per release (and refresh pinnedSHA256).
const PinnedChromeVersion = "149.0.7827.55"

// platform maps GOOS/GOARCH → the CfT platform token + the chrome binary path inside
// the extracted zip. CfT publishes no linux/arm64 or win/arm64 build; win/arm64 runs
// the win64 build under emulation, linux/arm64 has no option (hard error below).
var platform = map[string]struct{ token, binRel string }{
	"linux/amd64":   {"linux64", "chrome-linux64/chrome"},
	"windows/amd64": {"win64", "chrome-win64/chrome.exe"},
	"windows/arm64": {"win64", "chrome-win64/chrome.exe"}, // emulated
	"darwin/amd64":  {"mac-x64", "chrome-mac-x64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"},
	"darwin/arm64":  {"mac-arm64", "chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"},
}

// pinnedSHA256 is the per-token SHA-256 of the CfT zip, verified when present. When a
// token's entry is empty the download is trusted on TLS alone (from the fixed Google
// URL) and ensureDownloaded prints a warning to stderr. Populate these at release time
// to harden the supply chain.
var pinnedSHA256 = map[string]string{
	// "linux64": "<sha256 of chrome-linux64.zip for PinnedChromeVersion>",
}

func zipURL(version, token string) string {
	return fmt.Sprintf("https://storage.googleapis.com/chrome-for-testing-public/%s/%s/chrome-%s.zip", version, token, token)
}

// ResolveChrome returns an absolute path to a launchable Chrome-for-Testing binary,
// downloading the pinned build into chromeDir on first use. Resolution order:
// SHOWSTONE_CHROME → bundled beside the binary → download. onProgress (may be nil) gets
// 0..100 during a download.
func ResolveChrome(ctx context.Context, chromeDir string, onProgress func(pct int)) (string, error) {
	if p := os.Getenv("SHOWSTONE_CHROME"); p != "" {
		if isExecutable(p) {
			return p, nil
		}
		return "", fmt.Errorf("SHOWSTONE_CHROME=%q is not an executable", p)
	}
	plat, ok := platform[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("showstone: no Chrome-for-Testing build for %s/%s — set SHOWSTONE_CHROME to a local Chromium",
			runtime.GOOS, runtime.GOARCH)
	}
	if base, ok := binaryDir(); ok {
		if p := filepath.Join(base, "showstone-chrome", plat.token, plat.binRel); isExecutable(p) {
			return p, nil
		}
	}
	return ensureDownloaded(ctx, chromeDir, plat.token, plat.binRel, onProgress)
}

func ensureDownloaded(ctx context.Context, chromeDir, token, binRel string, onProgress func(int)) (string, error) {
	dst := filepath.Join(chromeDir, PinnedChromeVersion)
	bin := filepath.Join(dst, binRel)
	if fileExists(filepath.Join(dst, ".ok")) && isExecutable(bin) {
		return bin, nil
	}
	if err := os.MkdirAll(chromeDir, 0o700); err != nil {
		return "", err
	}
	tmpZip, sum, err := download(ctx, zipURL(PinnedChromeVersion, token), onProgress)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpZip)
	if want := pinnedSHA256[token]; want != "" {
		if want != sum {
			return "", fmt.Errorf("showstone: chrome zip checksum mismatch for %s", token)
		}
	} else {
		fmt.Fprintf(os.Stderr, "showstone: warning: no pinned checksum for %s — trusting TLS for the ~150 MB engine download\n", token)
	}
	// extract into a temp dir then rename, so a crash mid-extract never leaves a half dir.
	stage := dst + ".dl"
	_ = os.RemoveAll(stage)
	if err := unzipTo(tmpZip, stage); err != nil {
		return "", err
	}
	_ = os.RemoveAll(dst)
	if err := os.Rename(stage, dst); err != nil {
		return "", err
	}
	if err := os.Chmod(bin, 0o755); err != nil { // zip drops the +x bit on unix
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dst, ".ok"), []byte(sum), 0o600); err != nil {
		return "", err
	}
	return bin, nil
}

// download streams url to a temp file, returns its path + hex SHA-256.
func download(ctx context.Context, url string, onProgress func(int)) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("showstone: chrome download %s: HTTP %d", url, resp.StatusCode)
	}
	tmp, err := os.CreateTemp("", "showstone-chrome-*.zip")
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	pr := &progressReader{r: resp.Body, total: resp.ContentLength, cb: onProgress}
	if _, err := io.Copy(tmp, io.TeeReader(pr, h)); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		return "", "", err
	}
	return tmp.Name(), hex.EncodeToString(h.Sum(nil)), nil
}

type progressReader struct {
	r     io.Reader
	total int64
	read  int64
	last  int
	cb    func(int)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read += int64(n)
	if p.cb != nil && p.total > 0 {
		pct := int(p.read * 100 / p.total)
		if pct != p.last {
			p.last = pct
			p.cb(pct)
		}
	}
	return n, err
}

func unzipTo(zipPath, dst string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		target := filepath.Join(dst, f.Name) //nolint:gosec — guarded below
		if rel, rerr := filepath.Rel(dst, target); rerr != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode()|0o600)
		if err != nil {
			rc.Close()
			return err
		}
		_, cerr := io.Copy(out, rc) //nolint:gosec — CfT zips are trusted, size bounded by Google
		rc.Close()
		out.Close()
		if cerr != nil {
			return cerr
		}
	}
	return nil
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func binaryDir() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	if strings.HasPrefix(exe, os.TempDir()) || os.Getenv("MYKEEP_DEV") != "" {
		return "", false
	}
	return filepath.Dir(exe), true
}
