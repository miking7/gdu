package device

import (
	"path/filepath"
	"strings"
)

// Device struct
type Device struct {
	Name       string
	MountPoint string
	Fstype     string
	Size       int64
	Free       int64
}

// GetUsage returns used size of device
func (d Device) GetUsage() int64 {
	return d.Size - d.Free
}

// DevicesInfoGetter is type for GetDevicesInfo function
type DevicesInfoGetter interface {
	GetMounts() (Devices, error)
	GetDevicesInfo() (Devices, error)
}

// Devices if slice of Device items
type Devices []*Device

// ByUsedSize sorts devices by used size
type ByUsedSize Devices

func (f ByUsedSize) Len() int      { return len(f) }
func (f ByUsedSize) Swap(i, j int) { f[i], f[j] = f[j], f[i] }
func (f ByUsedSize) Less(i, j int) bool {
	return f[i].GetUsage() < f[j].GetUsage()
}

// ByName sorts devices by device name
type ByName Devices

func (f ByName) Len() int      { return len(f) }
func (f ByName) Swap(i, j int) { f[i], f[j] = f[j], f[i] }
func (f ByName) Less(i, j int) bool {
	return f[i].Name < f[j].Name
}

// systemVolumePrefix is the macOS mount-point prefix under which the OS exposes
// its synthetic system volumes (Preboot, Update, VM, Data, xarts, …) — noise a
// user browsing disk usage never wants to pick.
const systemVolumePrefix = "/System/Volumes/"

// HideSystemVolumes returns devices with macOS system volumes (mounted under
// /System/Volumes/) removed. It is a **display-only** filter for the launcher —
// never use it for nested-mount ignore computation, which needs the full list
// (a filtered list would let a "/" scan descend into /System/Volumes/Data and
// double-count the disk). It is a no-op off macOS, where nothing mounts there.
func HideSystemVolumes(devices Devices) Devices {
	filtered := make(Devices, 0, len(devices))
	for _, d := range devices {
		if strings.HasPrefix(d.MountPoint, systemVolumePrefix) {
			continue
		}
		filtered = append(filtered, d)
	}
	return filtered
}

// ForPath returns the device whose mount point is the longest path-prefix of p
// — the disk p lives on — or nil when no mount point covers it. It is the mount
// lookup shared by the launcher and the snapshot-covering logic; it
// deliberately does path-boundary matching (not a bare string prefix) so /Vol
// never matches /Volumes.
func ForPath(devices Devices, p string) *Device {
	var best *Device
	for _, d := range devices {
		if pathWithinMount(d.MountPoint, p) && (best == nil || len(d.MountPoint) > len(best.MountPoint)) {
			best = d
		}
	}
	return best
}

// pathWithinMount reports whether p is at or below mount, matching on path
// separators and tolerating a mount that already ends in one (a volume root
// like "/"). It mirrors report.PathCoveredBy, kept here so device stays free of
// the report import (report already depends on device).
func pathWithinMount(mount, p string) bool {
	if p == mount {
		return true
	}
	sep := string(filepath.Separator)
	if !strings.HasSuffix(mount, sep) {
		mount += sep
	}
	return strings.HasPrefix(p, mount)
}

// GetNestedMountpointsPaths returns paths of nested mount points
func GetNestedMountpointsPaths(path string, mounts Devices) []string {
	paths := make([]string, 0, len(mounts))

	for _, mount := range mounts {
		if strings.HasPrefix(mount.MountPoint, path) && mount.MountPoint != path {
			paths = append(paths, mount.MountPoint)
		}
	}
	return paths
}
