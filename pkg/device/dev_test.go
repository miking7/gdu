package device

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNested(t *testing.T) {
	item := &Device{
		MountPoint: "/xxx",
	}
	nested := &Device{
		MountPoint: "/xxx/yyy",
	}
	notNested := &Device{
		MountPoint: "/zzz/yyy",
	}

	mounts := Devices{item, nested, notNested}

	mountsNested := GetNestedMountpointsPaths("/xxx", mounts)

	assert.Len(t, mountsNested, 1)
	assert.Equal(t, "/xxx/yyy", mountsNested[0])
}

func TestNestedMatchesOnPathBoundaries(t *testing.T) {
	sd := &Device{MountPoint: "/Volumes/SD"}
	sdCard := &Device{MountPoint: "/Volumes/SDCard"}
	inside := &Device{MountPoint: "/Volumes/SD/nested"}

	nested := GetNestedMountpointsPaths("/Volumes/SD", Devices{sd, sdCard, inside})

	assert.Equal(t, []string{"/Volumes/SD/nested"}, nested,
		"a sibling volume sharing a name prefix is not nested")
}

func TestNestedUnderRoot(t *testing.T) {
	root := &Device{MountPoint: "/"}
	data := &Device{MountPoint: "/System/Volumes/Data"}

	nested := GetNestedMountpointsPaths("/", Devices{root, data})

	assert.Equal(t, []string{"/System/Volumes/Data"}, nested,
		"a / scan is bounded by every other mount, but not by itself")
}

func TestSortByName(t *testing.T) {
	item := &Device{
		Name: "/xxx",
	}
	nested := &Device{
		Name: "/xxx/yyy",
	}
	notNested := &Device{
		Name: "/zzz/yyy",
	}

	devices := Devices{item, nested, notNested}

	sort.Sort(sort.Reverse(ByName(devices)))

	assert.Equal(t, "/zzz/yyy", devices[0].Name)
	assert.Equal(t, "/xxx/yyy", devices[1].Name)
	assert.Equal(t, "/xxx", devices[2].Name)
}

func TestSortByUsedSize(t *testing.T) {
	item := &Device{
		Name: "xxx",
		Size: 1e12,
		Free: 1e3,
	}
	nested := &Device{
		Name: "yyy",
		Size: 1e12,
		Free: 1e6,
	}
	notNested := &Device{
		Name: "zzz",
		Size: 1e12,
		Free: 1e12,
	}

	devices := Devices{item, nested, notNested}

	sort.Sort(ByUsedSize(devices))

	assert.Equal(t, "zzz", devices[0].Name)
	assert.Equal(t, "yyy", devices[1].Name)
	assert.Equal(t, "xxx", devices[2].Name)
}

func TestHideSystemVolumes(t *testing.T) {
	devices := Devices{
		{MountPoint: "/"},
		{MountPoint: "/System/Volumes/Data"},
		{MountPoint: "/System/Volumes/VM"},
		{MountPoint: "/Volumes/SD"},
		{MountPoint: "/nix"},
	}
	got := HideSystemVolumes(devices)

	var mounts []string
	for _, d := range got {
		mounts = append(mounts, d.MountPoint)
	}
	assert.Equal(t, []string{"/", "/Volumes/SD", "/nix"}, mounts,
		"only /System/Volumes/* is hidden; /, /Volumes/*, and /nix survive")
}

func TestForPath(t *testing.T) {
	devs := Devices{
		{MountPoint: "/"},
		{MountPoint: "/Volumes/SD"},
		{MountPoint: "/Users/me/mnt"},
	}
	cases := []struct {
		path, wantMount string
	}{
		{"/Users/me/proj", "/"},
		{"/Volumes/SD", "/Volumes/SD"},        // path == mount
		{"/Volumes/SD/photos", "/Volumes/SD"}, // longest-prefix mount wins
		{"/Users/me/mnt/x", "/Users/me/mnt"},
		{"/Volumes", "/"}, // /Volumes itself is a dir on the root volume
	}
	for _, c := range cases {
		d := ForPath(devs, c.path)
		if assert.NotNil(t, d, c.path) {
			assert.Equal(t, c.wantMount, d.MountPoint, c.path)
		}
	}

	assert.Nil(t, ForPath(nil, "/anything"))
	assert.Nil(t, ForPath(Devices{{MountPoint: "/Volumes/SD"}}, "/etc"), "no covering mount")

	// A mount must match on a path boundary, not a bare string prefix.
	boundary := Devices{{MountPoint: "/Vol"}, {MountPoint: "/"}}
	assert.Equal(t, "/", ForPath(boundary, "/Volumes/x").MountPoint, "/Vol must not prefix-match /Volumes")
}
