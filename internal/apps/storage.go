package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// local-path-provisioner pins.
const (
	localPathVersion   = "0.0.37"
	localPathNamespace = "local-path-storage"
	localPathClass     = "local-path"
)

var localPathApp = App{
	Name:        "local-path",
	Aliases:     []string{"local-path-provisioner"},
	Description: "local-path-provisioner v" + localPathVersion + " (node-local volumes)",
	Category:    CategoryStorage,
	Namespace:   localPathNamespace,
	// kind ships this provisioner (same namespace and name) behind its
	// "standard" StorageClass, so on kind it is detected as installed and,
	// not being Helm-managed, left alone.
	Detect:     []string{"deployment/local-path-provisioner"},
	Privileged: true, // helper pods mount the node's disk (hostPath)
	chart: &helmChart{
		Release:   "local-path-provisioner",
		Chart:     "oci://ghcr.io/rancher/local-path-provisioner/charts/local-path-provisioner",
		Version:   localPathVersion,
		Namespace: localPathNamespace,
	},
	values: func(ctx context.Context, env *Env, _ string) (map[string]any, error) {
		values := map[string]any{
			"storageClass": map[string]any{
				"name": localPathClass,
				// Become the default only when there is none yet.
				"defaultClass": env.defaultStorageClass(ctx) == "",
			},
		}
		if env.isTalos(ctx) {
			values["nodePathMap"] = []any{map[string]any{
				"node":  "DEFAULT_PATH_FOR_NON_LISTED_NODES",
				"paths": []any{localPathTalosDir},
			}}
		}
		return values, nil
	},
	manifestURL: localPathManifestURL,
	afterManifest: func(ctx context.Context, env *Env) error {
		if env.isTalos(ctx) {
			cfg, err := localPathConfigPatch(localPathTalosDir)
			if err != nil {
				return err
			}
			if err := env.kubectl(ctx, "patch", "configmap", "local-path-config", "-n", localPathNamespace, "--type", "merge", "-p", cfg); err != nil {
				return err
			}
		}
		if env.defaultStorageClass(ctx) != "" {
			return nil // the chart's defaultClass rule: only when there is none
		}
		return env.kubectl(ctx, "patch", "storageclass", localPathClass, "-p",
			`{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}`)
	},
	check: func(ctx context.Context, env *Env) error {
		return env.storageSmokeTest(ctx, localPathNamespace, localPathClass, "ReadWriteOnce")
	},
	notes: func(ctx context.Context, env *Env, _ string) {
		fmt.Fprintf(env.Out, "   StorageClass: %s (ReadWriteOnce, data lives on one node)", localPathClass)
		if env.defaultStorageClass(ctx) == localPathClass {
			fmt.Fprint(env.Out, " · default")
		}
		fmt.Fprintln(env.Out)
	},
}

// localPathConfigPatch is a merge patch for local-path-config that stores
// every volume under dir.
func localPathConfigPatch(dir string) (string, error) {
	cfg, err := json.Marshal(map[string]any{"nodePathMap": []any{map[string]any{
		"node":  "DEFAULT_PATH_FOR_NON_LISTED_NODES",
		"paths": []string{dir},
	}}})
	if err != nil {
		return "", err
	}
	patch, err := json.Marshal(map[string]any{"data": map[string]string{"config.json": string(cfg)}})
	return string(patch), err
}

// localPathTalosDir is where volumes go on Talos, whose root filesystem
// (and so the default /opt/local-path-provisioner) is read-only; /var is the
// writable state partition (Talos' own local-path guide uses this path).
const localPathTalosDir = "/var/local-path-provisioner"

var localPathManifestURL = "https://raw.githubusercontent.com/rancher/local-path-provisioner/v" + localPathVersion + "/deploy/local-path-storage.yaml"

// NFS: an in-cluster NFS server (NFS-Ganesha) with its provisioner, so
// ReadWriteMany volumes work without an NFS server outside the cluster.
const (
	nfsChartVersion = "1.8.0"
	nfsNamespace    = "nfs-server"
	nfsClass        = "nfs"
)

var nfsApp = App{
	Name:        "nfs",
	Description: "NFS server + provisioner (ReadWriteMany volumes, StorageClass nfs)",
	Category:    CategoryStorage,
	Namespace:   nfsNamespace,
	Detect:      []string{"statefulset/nfs-server-nfs-server-provisioner"},
	Privileged:  true, // NFS-Ganesha adds DAC_READ_SEARCH/SYS_RESOURCE
	preflight:   requireStorageForProduction,
	chart: &helmChart{
		Release:   "nfs-server",
		Chart:     "nfs-server-provisioner",
		Repo:      "https://kubernetes-sigs.github.io/nfs-ganesha-server-and-external-provisioner/",
		Version:   nfsChartVersion,
		Namespace: nfsNamespace,
	},
	values: func(_ context.Context, env *Env, _ string) (map[string]any, error) {
		persistence := map[string]any{"enabled": false}
		if env.production() {
			// The exported data lives on a PV of the default StorageClass
			// instead of the node's disk.
			persistence = map[string]any{"enabled": true, "size": "20Gi"}
		}
		return map[string]any{
			"persistence": persistence,
			"storageClass": map[string]any{
				"name":                 nfsClass,
				"defaultClass":         false,
				"allowVolumeExpansion": true,
			},
		}, nil
	},
	check: func(ctx context.Context, env *Env) error {
		err := env.storageSmokeTest(ctx, nfsNamespace, nfsClass, "ReadWriteMany")
		if err != nil {
			return fmt.Errorf("%w\n  the nodes mount NFS volumes themselves, so they need an NFS client (mount.nfs)", err)
		}
		return nil
	},
	notes: func(ctx context.Context, env *Env, _ string) {
		fmt.Fprintf(env.Out, "   StorageClass: %s (ReadWriteMany)\n", nfsClass)
		if out, err := env.kubectlQuiet(ctx, "get", "pvc", "-n", nfsNamespace, "-o", "name"); err == nil && strings.TrimSpace(out) == "" {
			fmt.Fprintf(env.Out, "   Note:     dev profile — exported data is on the NFS server pod's node, not on a persistent volume\n")
		}
	},
}

// Longhorn pins.
const (
	longhornVersion   = "1.13.0"
	longhornNamespace = "longhorn-system"
	longhornClass     = "longhorn"
)

var longhornApp = App{
	Name:        "longhorn",
	Description: "Longhorn v" + longhornVersion + " (replicated block storage)",
	Category:    CategoryStorage,
	Namespace:   longhornNamespace,
	Detect:      []string{"deployment/longhorn-driver-deployer"},
	Privileged:  true, // managers and CSI plugins run privileged
	Timeout:     15 * time.Minute,
	Expose: &Expose{
		DefaultHost: "longhorn.localtest.me",
		Service:     "longhorn-frontend",
		Port:        80,
		Protocol:    ProtocolHTTP,
		LocalPort:   8081,
	},
	preflight: longhornPreflight,
	// Longhorn's uninstaller refuses to run until this setting is true, a
	// guard against deleting every volume by accident; uninstall is that
	// explicit choice.
	beforeRemove: func(ctx context.Context, env *Env) error {
		_, err := env.kubectlQuiet(ctx, "patch", "settings.longhorn.io", "deleting-confirmation-flag",
			"-n", longhornNamespace, "--type", "merge", "-p", `{"value":"true"}`)
		return err
	},
	chart: &helmChart{
		Release:   "longhorn",
		Chart:     "longhorn",
		Repo:      "https://charts.longhorn.io",
		Version:   longhornVersion,
		Namespace: longhornNamespace,
	},
	values: func(_ context.Context, env *Env, _ string) (map[string]any, error) {
		replicas := 3
		if !env.production() {
			replicas = 1
		}
		return map[string]any{
			"persistence":     map[string]any{"defaultClassReplicaCount": replicas},
			"defaultSettings": map[string]any{"defaultReplicaCount": replicas},
			"csi": map[string]any{
				"attacherReplicaCount":    replicas,
				"provisionerReplicaCount": replicas,
				"resizerReplicaCount":     replicas,
				"snapshotterReplicaCount": replicas,
			},
			"longhornUI": map[string]any{"replicas": min(replicas, 2)},
		}, nil
	},
	manifestURL: "https://raw.githubusercontent.com/longhorn/longhorn/v" + longhornVersion + "/deploy/longhorn.yaml",
	check: func(ctx context.Context, env *Env) error {
		return env.storageSmokeTest(ctx, longhornNamespace, longhornClass, "ReadWriteOnce")
	},
	notes: func(_ context.Context, env *Env, _ string) {
		fmt.Fprintf(env.Out, "   StorageClass: %s\n", longhornClass)
	},
}

// errLonghornDeclined stops a Longhorn install the user chose not to attempt.
var errLonghornDeclined = errors.New("not installing Longhorn on container nodes")

// longhornPreflight warns before installing Longhorn on kind/Talos nodes, which
// run as containers: Longhorn needs open-iscsi (iscsiadm + the iscsi_tcp
// module) on every node, which kind node images do not ship and Talos only
// provides through a system extension its Docker nodes cannot load.
func longhornPreflight(ctx context.Context, env *Env) error {
	if env.External() {
		return nil
	}
	if env.Confirm == nil {
		return fmt.Errorf("%w: Longhorn needs open-iscsi on every node, which %s nodes running in Docker do not provide", errLonghornDeclined, env.Provider)
	}
	ok, err := env.Confirm(fmt.Sprintf("Longhorn needs open-iscsi on every node, which %s nodes running in Docker do not provide — its managers will likely crash-loop. Install anyway?", env.Provider), false)
	if err != nil {
		return fmt.Errorf("%w: %w", errLonghornDeclined, err)
	}
	if !ok {
		return errLonghornDeclined
	}
	return nil
}
