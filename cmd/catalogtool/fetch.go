package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/furacca/someone-s-pc-catalog/internal/catalogdata"
)

const fetchTimeout = 10 * time.Minute

func runFetch(args []string) error {
	flags := flag.NewFlagSet("fetch", flag.ExitOnError)
	lockPath := flags.String("lock", "catalog.lock.json", "path of the lock that identifies the artifact")
	out := flags.String("out", "", "required path to stage the verified artifact at")
	from := flags.String("from", "", "path of a local preverified artifact to stage instead of downloading")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("fetch: -out is required")
	}

	lockBytes, err := os.ReadFile(*lockPath)
	if err != nil {
		return fmt.Errorf("fetch: read lock: %w", err)
	}
	lock, err := catalogdata.ParseLock(lockBytes)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	if staged, err := alreadyStaged(*out, lock); err != nil {
		return fmt.Errorf("fetch: %w", err)
	} else if staged {
		fmt.Printf("catalog %s revision %d already staged at %s\n", lock.Kind, lock.Revision, *out)
		return nil
	}

	target, err := os.CreateTemp(filepath.Dir(*out), ".catalog-fetch-*")
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	stagedPath := target.Name()
	defer os.Remove(stagedPath)

	var source io.ReadCloser
	if strings.TrimSpace(*from) != "" {
		source, err = os.Open(*from)
		if err != nil {
			target.Close()
			return fmt.Errorf("fetch: %w", err)
		}
	} else {
		source, err = download(lock.ArtifactURL)
		if err != nil {
			target.Close()
			return fmt.Errorf("fetch: %w", err)
		}
	}

	digest := sha256.New()
	// Read one extra byte to detect oversized artifacts without truncating them.
	written, copyErr := io.Copy(io.MultiWriter(target, digest), io.LimitReader(source, lock.ArchiveBytes+1))
	closeErr := source.Close()
	if err := target.Close(); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if copyErr != nil {
		return fmt.Errorf("fetch: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("fetch: %w", closeErr)
	}
	if written != lock.ArchiveBytes {
		return fmt.Errorf("fetch: artifact is %d bytes, the lock expects %d", written, lock.ArchiveBytes)
	}
	if sum := hex.EncodeToString(digest.Sum(nil)); sum != lock.ArchiveSHA256 {
		return fmt.Errorf("fetch: artifact checksum %s does not match the locked %s", sum, lock.ArchiveSHA256)
	}

	archive, err := catalogdata.OpenFile(stagedPath, lock)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	archive.Close()

	if err := os.Rename(stagedPath, *out); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	fmt.Printf("catalog %s revision %d staged at %s (%d bytes)\n", lock.Kind, lock.Revision, *out, lock.ArchiveBytes)
	return nil
}

func alreadyStaged(path string, lock catalogdata.Lock) (bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Size() != lock.ArchiveBytes {
		return false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return false, err
	}
	return hex.EncodeToString(digest.Sum(nil)) == lock.ArchiveSHA256, nil
}

func download(artifactURL string) (io.ReadCloser, error) {
	artifactURL = strings.TrimSpace(artifactURL)
	if artifactURL == "" {
		return nil, fmt.Errorf("the lock has no artifact_url; supply -from with a local preverified artifact")
	}
	parsed, err := url.Parse(artifactURL)
	if err != nil {
		return nil, fmt.Errorf("artifact_url is not a URL: %w", err)
	}
	if err := validateArtifactURL(parsed); err != nil {
		return nil, err
	}
	client := &http.Client{
		Timeout: fetchTimeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return validateArtifactURL(request.URL)
		},
	}
	response, err := client.Get(artifactURL)
	if err != nil {
		return nil, fmt.Errorf("download artifact: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("download artifact: unexpected status %d", response.StatusCode)
	}
	return response.Body, nil
}

func validateArtifactURL(parsed *url.URL) error {
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("artifact_url scheme %q is not allowed", parsed.Scheme)
	}
	if parsed.User != nil {
		return fmt.Errorf("artifact_url must not embed credentials")
	}
	return nil
}
