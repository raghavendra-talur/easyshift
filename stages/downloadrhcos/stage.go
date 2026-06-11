// Package downloadrhcos fetches the RHCOS live ISO that the installer pins,
// into the shared per-version cache.
package downloadrhcos

import (
	"context"
	"fmt"
	"os"

	"github.com/TheEasyShift/easyshift/interfaces"
)

// Stage downloads the RHCOS live ISO.
type Stage struct {
	installer interfaces.Installer
	dl        interfaces.Downloader
}

// New returns the download-rhcos stage.
func New(installer interfaces.Installer, dl interfaces.Downloader) *Stage {
	return &Stage{installer: installer, dl: dl}
}

func (*Stage) Name() string { return "download-rhcos" }

func (s *Stage) Apply(ctx context.Context, sc *interfaces.StageContext) error {
	dest := sc.RHCOSLiveISOPath()
	if _, err := os.Stat(dest); err == nil {
		return nil // already cached
	}
	// Ask openshift-install which RHCOS live ISO it pins (authoritative).
	url, err := s.installer.CoreOSLiveISOURL(ctx, sc.InstallerSpec())
	if err != nil {
		return fmt.Errorf("determine RHCOS live ISO url: %w", err)
	}
	return s.dl.Download(ctx, url, dest)
}

// Rollback is a no-op: the cache is shared across clusters.
func (*Stage) Rollback(_ context.Context, _ *interfaces.StageContext) error { return nil }
