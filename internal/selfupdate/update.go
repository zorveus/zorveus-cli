package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

const releaseOrigin = "https://zorveus.com/cli"

var releaseVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$`)

type Result struct {
	Version        string `json:"version"`
	Executable     string `json:"executable"`
	AlreadyCurrent bool   `json:"already_current"`
}

type Updater struct {
	HTTP       *http.Client
	Executable func() (string, error)
}

func New() *Updater {
	return &Updater{HTTP: &http.Client{Timeout: 5 * time.Minute}, Executable: os.Executable}
}

func (updater *Updater) Run(ctx context.Context, version string) (Result, error) {
	if version != "" && !releaseVersion.MatchString(version) {
		return Result{}, problem.New("invalid_version", "Use a release version such as v0.1.0 or v0.2.0-rc.1.", 2)
	}
	artifact, err := artifactName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return Result{}, err
	}
	executable, err := updater.Executable()
	if err != nil {
		return Result{}, pathError()
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return Result{}, pathError()
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() {
		return Result{}, pathError()
	}
	// Update the copy that was invoked, including installations reached by symlink.
	lock := flock.New(filepath.Join(filepath.Dir(executable), "."+filepath.Base(executable)+".update.lock"), flock.SetPermissions(0600))
	defer lock.Close()
	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil || !locked {
		return Result{}, problem.New("update_locked", "Could not lock this installation for update. Check directory permissions and close other update commands, then retry.", 1)
	}
	if version == "" {
		var latest bytes.Buffer
		if err := updater.download(ctx, "/latest", &latest, 128); err != nil {
			return Result{}, err
		}
		version = strings.TrimSpace(latest.String())
		if !releaseVersion.MatchString(version) {
			return Result{}, problem.New("invalid_release", "The release server did not return a valid version. Check that a CLI release is published. Your executable was not changed.", 1)
		}
	}
	var checksums bytes.Buffer
	if err := updater.download(ctx, "/"+version+"/SHA256SUMS", &checksums, 64<<10); err != nil {
		return Result{}, err
	}
	expected, err := releaseChecksum(checksums.String(), artifact)
	if err != nil {
		return Result{}, err
	}
	current, err := fileChecksum(executable)
	if err != nil {
		return Result{}, pathError()
	}
	result := Result{Version: version, Executable: executable}
	if current == expected {
		result.AlreadyCurrent = true
		return result, nil
	}
	// Stage beside the executable so the final replacement stays on one filesystem.
	staged, err := os.CreateTemp(filepath.Dir(executable), ".zorveus-download-*")
	if err != nil {
		return Result{}, replacementError()
	}
	defer os.Remove(staged.Name())
	defer staged.Close()
	if err := updater.download(ctx, "/"+version+"/"+artifact, staged, 256<<20); err != nil {
		return Result{}, err
	}
	if err := staged.Sync(); err != nil {
		return Result{}, replacementError()
	}
	if err := staged.Close(); err != nil {
		return Result{}, replacementError()
	}
	actual, err := fileChecksum(staged.Name())
	if err != nil {
		return Result{}, replacementError()
	}
	if actual != expected {
		return Result{}, problem.New("update_checksum_mismatch", "Checksum verification failed. Your executable was not changed.", 1)
	}
	if err := os.Chmod(staged.Name(), info.Mode().Perm()); err != nil {
		return Result{}, replacementError()
	}
	if ctx.Err() != nil {
		return Result{}, problem.New("update_canceled", "Update canceled. Your executable was not changed.", 1)
	}
	if err := replaceExecutable(executable, staged.Name(), runtime.GOOS); err != nil {
		return Result{}, err
	}
	return result, nil
}

func artifactName(system, architecture string) (string, error) {
	if system != "darwin" && system != "linux" && system != "windows" || architecture != "amd64" && architecture != "arm64" {
		return "", problem.New("unsupported_platform", "Updates support macOS, Linux, and Windows on x86-64 and ARM64.", 2)
	}
	name := "zorveus-" + system + "-" + architecture
	if system == "windows" {
		name += ".exe"
	}
	return name, nil
}

func (updater *Updater) download(ctx context.Context, path string, destination io.Writer, limit int64) error {
	client := *updater.HTTP
	client.CheckRedirect = func(request *http.Request, previous []*http.Request) error {
		if request.URL.Scheme != "https" || len(previous) >= 10 {
			return errors.New("release redirect refused")
		}
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseOrigin+path, nil)
	if err != nil {
		return downloadError()
	}
	response, err := client.Do(request)
	if err != nil {
		return downloadError()
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return downloadError()
	}
	written, err := io.Copy(destination, io.LimitReader(response.Body, limit+1))
	if err != nil || written > limit {
		return downloadError()
	}
	return nil
}

func releaseChecksum(contents, artifact string) (string, error) {
	checksum, matches := "", 0
	for _, line := range strings.Split(contents, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != artifact {
			continue
		}
		checksum = fields[0]
		matches++
	}
	decoded, err := hex.DecodeString(checksum)
	if matches != 1 || err != nil || len(decoded) != sha256.Size {
		return "", problem.New("invalid_release_checksum", "The release has no valid, unique checksum for this platform. Your executable was not changed.", 1)
	}
	return hex.EncodeToString(decoded), nil
}

func fileChecksum(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	checksum := sha256.New()
	if _, err := io.Copy(checksum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(checksum.Sum(nil)), nil
}

func pathError() error {
	return problem.New("update_path_unavailable", "Could not locate or read the running executable. Reinstall using the installer from zorveus.com.", 1)
}

func downloadError() error {
	return problem.New("update_download_failed", "Could not download the published CLI release. Check your connection and the release downloads on zorveus.com. Your executable was not changed.", 1)
}

func replacementError() error {
	return problem.New("update_replace_failed", "Could not replace this executable. Check installation-directory permissions and close other running Zorveus commands, then retry.", 1)
}
