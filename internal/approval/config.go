// Package approval implements durable approval evidence and fresh-page dispatch.
package approval

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	wire "github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/approval"
)

type Config struct {
	Mode, OrgID, Revision, JournalDir, Endpoint string
	BearerToken                                 string
	MinInterval, MaxInterval, Timeout           time.Duration
	BackoffFactor                               float64
}

func (c Config) String() string {
	return fmt.Sprintf("approval{mode=%s org=%s revision=%s}", c.Mode, c.OrgID, c.Revision)
}
func (c Config) Validate() error {
	if c.Mode != "external" && c.Mode != "auto_approve" {
		return errors.New("approval mode must be external or auto_approve")
	}
	for _, s := range []string{c.OrgID, c.Revision} {
		if !wire.ValidReference(s) {
			return errors.New("approval identity and revision must be bounded references")
		}
	}
	if !filepath.IsAbs(c.JournalDir) || filepath.Clean(c.JournalDir) == "/" {
		return errors.New("approval journal must be an absolute private directory")
	}
	if c.MinInterval <= 0 || c.MaxInterval < c.MinInterval || c.Timeout <= 0 || math.IsNaN(c.BackoffFactor) || math.IsInf(c.BackoffFactor, 0) || c.BackoffFactor < 1 {
		return errors.New("invalid approval timeout or backoff configuration")
	}
	if c.Mode == "external" {
		if !validEndpoint(c.Endpoint) || strings.TrimSpace(c.BearerToken) == "" || strings.ContainsAny(c.BearerToken, "\r\n") {
			return errors.New("approval external HTTPS endpoint and dedicated bearer credential are required")
		}
	}
	return nil
}

// ValidateDirectories resolves existing ancestors, including symlink aliases.
func ValidateDirectories(journal string, shares ...string) error {
	canonical := func(path string) (string, error) {
		var tail []string
		for {
			resolved, err := filepath.EvalSymlinks(path)
			if err == nil {
				for i := len(tail) - 1; i >= 0; i-- {
					resolved = filepath.Join(resolved, tail[i])
				}
				return resolved, nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
			parent := filepath.Dir(path)
			if parent == path {
				return "", err
			}
			tail = append(tail, filepath.Base(path))
			path = parent
		}
	}
	j, err := canonical(filepath.Clean(journal))
	if err != nil {
		return err
	}
	for _, s := range shares {
		p, e := canonical(s)
		if e != nil {
			return e
		}
		if within(j, p) || within(p, j) {
			return errors.New("approval journal overlaps a share directory")
		}
	}
	return nil
}
func within(path, parent string) bool {
	rel, e := filepath.Rel(parent, path)
	return e == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func validEndpoint(value string) bool {
	u, e := url.Parse(value)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}
