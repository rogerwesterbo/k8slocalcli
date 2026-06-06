package provider

// kindNodeImages maps a Kubernetes version to the pinned kindest/node image
// (including digest) known to work with kind. The first entry is the default.
// Source: createlocalk8s/scripts/variables.sh — keep in sync with kind releases.
var kindNodeImages = []struct {
	Version string
	Image   string
}{
	{"v1.36.1", "kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5"},
	{"v1.35.5", "kindest/node:v1.35.5@sha256:ce977ae6d65918d0b58a5f8b5e940429c2ce42fa3a5619ec2bbc60b949c0ac95"},
	{"v1.34.8", "kindest/node:v1.34.8@sha256:02722c2dedddcfc00febf5d27fbeb9b7b2c14294c82109ff4a85d89ac9ba3256"},
	{"v1.33.12", "kindest/node:v1.33.12@sha256:3f5c8443c620245e4d355cfe09e96a91ead32ceaa569d3f1ca9edf0cb2fe2ff4"},
	{"v1.32.11", "kindest/node:v1.32.11@sha256:5fc52d52a7b9574015299724bd68f183702956aa4a2116ae75a63cb574b35af8"},
	{"v1.31.14", "kindest/node:v1.31.14@sha256:6f86cf509dbb42767b6e79debc3f2c32e4ee01386f0489b3b2be24b0a55aac2b"},
	{"v1.30.13", "kindest/node:v1.30.13@sha256:397209b3d947d154f6641f2d0ce8d473732bd91c87d9575ade99049aa33cd648"},
	{"v1.29.14", "kindest/node:v1.29.14@sha256:8703bd94ee24e51b778d5556ae310c6c0fa67d761fae6379c8e0bb480e6fea29"},
	{"v1.28.15", "kindest/node:v1.28.15@sha256:a7c05c7ae043a0b8c818f5a06188bc2c4098f6cb59ca7d1856df00375d839251"},
	{"v1.27.16", "kindest/node:v1.27.16@sha256:2d21a61643eafc439905e18705b8186f3296384750a835ad7a005dceb9546d20"},
	{"v1.26.15", "kindest/node:v1.26.15@sha256:c79602a44b4056d7e48dc20f7504350f1e87530fe953428b792def00bc1076dd"},
	{"v1.25.16", "kindest/node:v1.25.16@sha256:6110314339b3b44d10da7d27881849a87e092124afab5956f2e10ecdb463b025"},
}

// kindImageFor returns the node image for the requested version. When version
// is empty it returns the default (newest) image. When the version is unknown
// it falls back to the default and reports ok=false so callers can warn.
func kindImageFor(version string) (image string, resolvedVersion string, ok bool) {
	if version == "" {
		d := kindNodeImages[0]
		return d.Image, d.Version, true
	}
	for _, e := range kindNodeImages {
		if e.Version == version || e.Version == "v"+version {
			return e.Image, e.Version, true
		}
	}
	d := kindNodeImages[0]
	return d.Image, d.Version, false
}

// talosK8sVersions maps a talosctl major.minor version to the list of supported
// Kubernetes versions, newest first. Source: Talos support matrix
// https://docs.siderolabs.com/talos/v1.13/getting-started/support-matrix
var talosK8sVersions = map[string][]string{
	"1.13": {"1.36.1", "1.35.5", "1.34.8", "1.33.12", "1.32.13", "1.31.14"},
	"1.12": {"1.35.4", "1.34.1", "1.33.1", "1.32.3", "1.31.6", "1.30.10"},
	"1.11": {"1.34.1", "1.33.1", "1.32.3", "1.31.6", "1.30.10", "1.29.14"},
	"1.10": {"1.33.1", "1.32.3", "1.31.6", "1.30.10", "1.29.14", "1.28.15"},
	"1.9":  {"1.32.3", "1.31.6", "1.30.10", "1.29.14", "1.28.15", "1.27.16"},
	"1.8":  {"1.31.6", "1.30.10", "1.29.14", "1.28.15", "1.27.16", "1.26.15"},
}

// talosLatestK8sVersions is used when the installed talosctl version is not in
// the support matrix above.
var talosLatestK8sVersions = []string{"1.36.1", "1.35.5", "1.34.8", "1.33.12", "1.32.13", "1.31.14"}

// talosK8sVersionsFor returns the supported Kubernetes versions for a talosctl
// major.minor version, falling back to the latest known set.
func talosK8sVersionsFor(majorMinor string) []string {
	if v, ok := talosK8sVersions[majorMinor]; ok {
		return v
	}
	return talosLatestK8sVersions
}
