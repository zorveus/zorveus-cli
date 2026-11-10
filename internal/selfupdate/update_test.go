package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (transport releaseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func releaseFixture(t *testing.T) (*Updater, string, map[string][]byte, *[]string) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "custom tools")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	directory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "zorveus")
	if err := os.WriteFile(executable, []byte("previous version"), 0755); err != nil {
		t.Fatal(err)
	}
	artifact, err := artifactName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("verified release fixture; never execute this file\n")
	checksum := sha256.Sum256(payload)
	files := map[string][]byte{
		"/cli/latest":             []byte("v1.2.3\n"),
		"/cli/v1.2.3/SHA256SUMS":  []byte(fmt.Sprintf("%x  %s\n", checksum, artifact)),
		"/cli/v1.2.3/" + artifact: payload,
	}
	requests := []string{}
	updater := New()
	updater.Executable = func() (string, error) { return executable, nil }
	updater.HTTP.Transport = releaseTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Host != "zorveus.com" || request.Header.Get("Authorization") != "" {
			t.Error("release download used the wrong origin or management credentials")
		}
		requests = append(requests, request.URL.Path)
		data, exists := files[request.URL.Path]
		status := http.StatusOK
		if !exists {
			status = http.StatusNotFound
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})
	return updater, executable, files, &requests
}

func TestUpdateInstallsVerifiedReleaseAndSkipsAnIdenticalBinary(t *testing.T) {
	updater, executable, files, requests := releaseFixture(t)
	result, err := updater.Run(context.Background(), "")
	if err != nil || result.Version != "v1.2.3" || result.Executable != executable || result.AlreadyCurrent {
		t.Fatalf("update failed: %+v %v", result, err)
	}
	artifact, _ := artifactName(runtime.GOOS, runtime.GOARCH)
	installed, _ := os.ReadFile(executable)
	if !bytes.Equal(installed, files["/cli/v1.2.3/"+artifact]) {
		t.Fatal("the verified release was not installed")
	}
	result, err = updater.Run(context.Background(), "")
	if err != nil || !result.AlreadyCurrent || len(*requests) != 5 {
		t.Fatalf("identical release was downloaded again: %+v %v requests=%v", result, err, *requests)
	}
	staged, _ := filepath.Glob(filepath.Join(filepath.Dir(executable), ".zorveus-download-*"))
	if len(staged) != 0 {
		t.Fatal("staged download was not cleaned up")
	}
}

func TestUpdateFailuresPreserveCurrentExecutable(t *testing.T) {
	for _, failure := range []string{"download", "checksum", "duplicate checksum", "missing checksum", "invalid latest", "oversized latest", "network"} {
		t.Run(failure, func(t *testing.T) {
			updater, executable, files, _ := releaseFixture(t)
			artifact, _ := artifactName(runtime.GOOS, runtime.GOARCH)
			switch failure {
			case "download":
				delete(files, "/cli/v1.2.3/"+artifact)
			case "checksum":
				files["/cli/v1.2.3/"+artifact] = []byte("unverified replacement")
			case "duplicate checksum":
				files["/cli/v1.2.3/SHA256SUMS"] = bytes.Repeat(files["/cli/v1.2.3/SHA256SUMS"], 2)
			case "missing checksum":
				files["/cli/v1.2.3/SHA256SUMS"] = []byte("not a release checksum")
			case "invalid latest":
				files["/cli/latest"] = []byte("<html>Website</html>")
			case "oversized latest":
				files["/cli/latest"] = bytes.Repeat([]byte("1"), 129)
			case "network":
				updater.HTTP.Transport = releaseTransport(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("private transport failure")
				})
			}
			_, err := updater.Run(context.Background(), "")
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("unexpected download failure: %v", err)
			}
			installed, _ := os.ReadFile(executable)
			if string(installed) != "previous version" {
				t.Fatal("failed update replaced the installed binary")
			}
			staged, _ := filepath.Glob(filepath.Join(filepath.Dir(executable), ".zorveus-download-*"))
			if len(staged) != 0 {
				t.Fatal("failed download was not cleaned up")
			}
		})
	}
}

func TestPinnedUpdateAndInvalidVersions(t *testing.T) {
	updater, _, _, requests := releaseFixture(t)
	if _, err := updater.Run(context.Background(), "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	for _, path := range *requests {
		if path == "/cli/latest" {
			t.Fatal("pinned update fetched the latest-version pointer")
		}
	}
	updater.Executable = func() (string, error) { t.Fatal("invalid version inspected the executable"); return "", nil }
	for _, version := range []string{"../../bad", "v1.2.3\nv4.5.6", "v1.2", "latest"} {
		_, err := updater.Run(context.Background(), version)
		if problem.Safe(err).Code != "invalid_version" {
			t.Fatalf("accepted invalid version %q: %v", version, err)
		}
	}
}

func TestUpdateResolvesSymlinksToTheInvokedInstallation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating Windows symlinks may require additional privileges")
	}
	updater, executable, _, _ := releaseFixture(t)
	link := filepath.Join(t.TempDir(), "zorveus")
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	updater.Executable = func() (string, error) { return link, nil }
	result, err := updater.Run(context.Background(), "")
	if err != nil || result.Executable != executable {
		t.Fatalf("symlink updated the wrong installation: %+v %v", result, err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("update replaced the symlink instead of its target")
	}
}

func TestReleasePlatformsAndChecksumValidation(t *testing.T) {
	for _, system := range []string{"darwin", "linux", "windows"} {
		for _, architecture := range []string{"amd64", "arm64"} {
			name, err := artifactName(system, architecture)
			want := "zorveus-" + system + "-" + architecture
			if system == "windows" {
				want += ".exe"
			}
			if err != nil || name != want {
				t.Fatalf("incorrect release artifact: %q %v", name, err)
			}
		}
	}
	if _, err := artifactName("linux", "arm"); err == nil {
		t.Fatal("accepted an unpublished architecture")
	}
	if _, err := artifactName("freebsd", "amd64"); err == nil {
		t.Fatal("accepted an unpublished operating system")
	}
	for _, checksum := range []string{"", strings.Repeat("0", 63), strings.Repeat("z", 64)} {
		if _, err := releaseChecksum(checksum+"  binary\n", "binary"); err == nil {
			t.Fatal("accepted an invalid release checksum")
		}
	}
	valid := hex.EncodeToString(make([]byte, sha256.Size))
	if actual, err := releaseChecksum(valid+"  binary\n", "binary"); err != nil || actual != valid {
		t.Fatal("rejected a valid release checksum")
	}
}

func TestDownloadsRefuseHTTPSDowngrades(t *testing.T) {
	updater := New()
	requests := 0
	updater.HTTP.Transport = releaseTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.Scheme != "https" {
			t.Fatal("release download followed an insecure redirect")
		}
		return &http.Response{StatusCode: http.StatusFound, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": {"http://insecure.example.test/latest"}}}, nil
	})
	var output bytes.Buffer
	if err := updater.download(context.Background(), "/latest", &output, 128); err == nil || requests != 1 {
		t.Fatal("insecure release redirect was accepted")
	}
}

func TestWindowsReplacementRestoresPreviousExecutableOnFailure(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "zorveus.exe")
	if err := os.WriteFile(executable, []byte("previous version"), 0755); err != nil {
		t.Fatal(err)
	}
	err := replaceExecutable(executable, filepath.Join(directory, "missing-download"), "windows")
	if err == nil {
		t.Fatal("replacement unexpectedly succeeded")
	}
	previous, readErr := os.ReadFile(executable)
	if readErr != nil || string(previous) != "previous version" {
		t.Fatal("failed replacement did not restore the previous executable")
	}
}

func TestUpdateRunningExecutable(t *testing.T) {
	if os.Getenv("ZORVEUS_TEST_UPDATE_WORKER") == "1" {
		updater, _, _, _ := releaseFixture(t)
		updater.Executable = os.Executable
		if _, err := updater.Run(context.Background(), ""); err != nil {
			t.Fatal(err)
		}
		return
	}
	original, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "running-zorveus")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	if err := os.WriteFile(path, data, 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path, "-test.run=^TestUpdateRunningExecutable$")
	command.Env = append(os.Environ(), "ZORVEUS_TEST_UPDATE_WORKER=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("running executable could not update itself: %v\n%s", err, output)
	}
	installed, err := os.ReadFile(path)
	if err != nil || string(installed) != "verified release fixture; never execute this file\n" {
		t.Fatal("running copy did not install the verified replacement")
	}
}
