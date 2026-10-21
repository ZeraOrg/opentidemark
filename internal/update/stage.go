// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Release downloads and the staging directory (SPEC §17.7).
const (
	releaseDownloadBase = "https://github.com/ZeraOrg/opentidemark/releases/download/v"
	ChecksumsName       = "checksums.txt"
	SignatureName       = "checksums.txt.minisig"
	RequestFile         = "request.json"
	MaxAssetBytes       = 200 << 20
	MaxChecksumsBytes   = 64 << 10
	MaxSignatureBytes   = 4 << 10
	maxProtocolBytes    = 4 << 10 // request, confirmation and state files
	stagedAssetPrefix   = "tidemark_"
)

// ErrDownload is returned when a release file cannot be downloaded, and ErrInvalidRequest for
// an update request with a malformed id or version.
var (
	ErrDownload       = errors.New("release download failed")
	ErrInvalidRequest = errors.New("invalid update request")
)

// requestID is the shape of an update request id (e.g. a UUID).
var requestID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// Request asks the helper to install a staged release (request.json).
type Request struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// validate checks the id shape and that Version is a release version.
func (r Request) validate() (Version, error) {
	if !requestID.MatchString(r.ID) {
		return Version{}, fmt.Errorf("%w: id %q", ErrInvalidRequest, r.ID)
	}
	v, err := ParseVersion(r.Version)
	if err != nil {
		return Version{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	return v, nil
}

// HTTPDoer sends a request; *http.Client (the update client of httpx) implements it.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Stager downloads a release into the staging directory for the helper.
type Stager struct {
	client   HTTPDoer
	dir      string
	keys     []string
	platform Platform
}

// NewStager returns a stager that downloads through client into dir, trusts the minisign keys
// (TrustedSigningKeys) and stages the archive for platform.
func NewStager(client HTTPDoer, dir string, keys []string, platform Platform) *Stager {
	return &Stager{client: client, dir: dir, keys: keys, platform: platform}
}

// StageHooks observe a staging; both are optional.
type StageHooks struct {
	// Reserve is called before the archive is written with its announced size (MaxAssetBytes
	// when the server sends none); an error stops the staging.
	Reserve func(size int64) error
	// Progress reports the archive bytes downloaded so far and the announced size.
	Progress func(done, total int64)
}

// Stage downloads checksums.txt.minisig, checksums.txt and the archive of req.Version, verifies
// the signature and then the archive's SHA-256 before keeping anything, writes the three files
// and finally request.json (each through a rename). A previous request.json is left in place
// until the new one replaces it: deleting it would be a change of the file the Linux path unit
// watches, and a helper started by it could still be running when the new request lands, losing
// that trigger. Pairing the old request with the new files is harmless, because the helper binds
// the signature (trusted comment) and the archive name to the request's version, so such a pair
// only fails verification and changes nothing.
func (s *Stager) Stage(ctx context.Context, req Request, hooks StageHooks) error {
	if _, err := req.validate(); err != nil {
		return err
	}
	asset, err := AssetName(req.Version, s.platform)
	if err != nil {
		return err
	}
	base := releaseDownloadBase + req.Version + "/"
	sig, err := s.fetch(ctx, base+SignatureName, MaxSignatureBytes)
	if err != nil {
		return err
	}
	sums, err := s.fetch(ctx, base+ChecksumsName, MaxChecksumsBytes)
	if err != nil {
		return err
	}
	want, err := verifiedChecksum(sums, sig, req.Version, s.keys, asset)
	if err != nil {
		return err
	}
	tmp, err := s.fetchAsset(ctx, base+asset, want, hooks)
	if err != nil {
		return err
	}
	if err := s.keep(tmp, asset, sums, sig); err != nil {
		_ = os.Remove(tmp) // best effort: staging failed and is reported
		return err
	}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	return writeFileAtomic(s.dir, RequestFile, data, sharedFileMode)
}

// verifiedChecksum verifies the signature of checksums.txt and returns the asset's SHA-256.
func verifiedChecksum(sums, sig []byte, version string, keys []string, asset string) ([32]byte, error) {
	if err := VerifyChecksumsSignature(sums, sig, version, keys); err != nil {
		return [32]byte{}, err
	}
	parsed, err := ParseChecksums(sums)
	if err != nil {
		return [32]byte{}, err
	}
	want, ok := parsed.Lookup(asset)
	if !ok {
		return [32]byte{}, fmt.Errorf("%w: %s not listed", ErrChecksums, asset)
	}
	return want, nil
}

// keep moves the verified archive into place, writes the signed checksums next to it and
// removes archives of earlier stagings.
func (s *Stager) keep(tmp, asset string, sums, sig []byte) error {
	if err := writeFileAtomic(s.dir, ChecksumsName, sums, sharedFileMode); err != nil {
		return err
	}
	if err := writeFileAtomic(s.dir, SignatureName, sig, sharedFileMode); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, asset)); err != nil {
		return fmt.Errorf("stage %s: %w", asset, err)
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("read staging directory: %w", err)
	}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasPrefix(e.Name(), stagedAssetPrefix) && e.Name() != asset {
			if err := os.Remove(filepath.Join(s.dir, e.Name())); err != nil {
				return fmt.Errorf("remove stale %s: %w", e.Name(), err)
			}
		}
	}
	return nil
}

// fetch downloads url into memory, at most limit bytes.
func (s *Stager) fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	body, _, err := s.open(ctx, url, limit)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrDownload, path.Base(url), err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrDownload, path.Base(url), limit)
	}
	return data, nil
}

// fetchAsset downloads url into a temporary file of the staging directory and keeps it only
// when its SHA-256 is want.
func (s *Stager) fetchAsset(ctx context.Context, url string, want [32]byte, hooks StageHooks) (string, error) {
	body, size, err := s.open(ctx, url, MaxAssetBytes)
	if err != nil {
		return "", err
	}
	defer body.Close()
	if size < 0 {
		size = MaxAssetBytes
	}
	if hooks.Reserve != nil {
		if err := hooks.Reserve(size); err != nil {
			return "", err
		}
	}
	var src io.Reader = body
	if hooks.Progress != nil {
		src = &progressReader{r: body, total: size, report: hooks.Progress}
	}
	f, err := os.CreateTemp(s.dir, ".asset.tmp-*")
	if err != nil {
		return "", fmt.Errorf("stage archive: %w", err)
	}
	var sum [32]byte
	err = f.Chmod(sharedFileMode)
	if err == nil {
		sum, err = copyHashed(f, src, MaxAssetBytes)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && sum != want {
		err = fmt.Errorf("%w: %s", ErrChecksumMismatch, path.Base(url))
	}
	if err != nil {
		_ = os.Remove(f.Name()) // best effort: the download is rejected and reported
		return "", fmt.Errorf("%s: %w", path.Base(url), err)
	}
	return f.Name(), nil
}

// open sends GET url and returns the body of a 200 answer that does not announce more than
// limit bytes, and its announced length (-1 when unknown).
func (s *Stager) open(ctx context.Context, url string, limit int64) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrDownload, err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %s: %w", ErrDownload, path.Base(url), err)
	}
	if resp.StatusCode != http.StatusOK || resp.ContentLength > limit {
		_ = resp.Body.Close() // the answer is refused; its body is not needed
		return nil, 0, fmt.Errorf("%w: %s: status %d, %d bytes", ErrDownload, path.Base(url), resp.StatusCode, resp.ContentLength)
	}
	return resp.Body, resp.ContentLength, nil
}

// progressReader reports the bytes read so far.
type progressReader struct {
	r      io.Reader
	done   int64
	total  int64
	report func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		p.report(p.done, p.total)
	}
	return n, err
}
