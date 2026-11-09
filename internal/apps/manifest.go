package apps

import (
	"strings"

	"sigs.k8s.io/yaml"
)

// gatewayAPIGroups are the API groups of the upstream Gateway API bundle. Its
// CRDs and its safe-upgrades admission policy are named <plural>.<group>.
var gatewayAPIGroups = []string{"gateway.networking.k8s.io", "gateway.networking.x-k8s.io"}

// bundleKinds are the kinds the Gateway API bundle consists of.
var bundleKinds = map[string]bool{
	"CustomResourceDefinition":         true,
	"ValidatingAdmissionPolicy":        true,
	"ValidatingAdmissionPolicyBinding": true,
}

// withoutGatewayAPIResources drops the Gateway API bundle (CRDs and the
// safe-upgrades policy) from a multi-document manifest that vendors it, so a
// controller's install manifest cannot overwrite the channel k8slocalcli
// installed. It returns the remaining manifest and the number of documents
// dropped.
func withoutGatewayAPIResources(manifest string) (string, int, error) {
	var kept []string
	dropped := 0
	for _, doc := range splitDocuments(manifest) {
		var meta struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
			return "", 0, err
		}
		if meta.Kind == "" {
			continue // comment-only or empty document
		}
		if bundleKinds[meta.Kind] && inGatewayAPIGroup(meta.Metadata.Name) {
			dropped++
			continue
		}
		kept = append(kept, doc)
	}
	return strings.Join(kept, "\n---\n") + "\n", dropped, nil
}

func inGatewayAPIGroup(name string) bool {
	for _, g := range gatewayAPIGroups {
		if strings.HasSuffix(name, "."+g) {
			return true
		}
	}
	return false
}

// splitDocuments splits a YAML stream on its "---" separator lines.
func splitDocuments(manifest string) []string {
	var docs []string
	var cur []string
	for _, line := range strings.Split(manifest, "\n") {
		if strings.TrimRight(line, " \t\r") == "---" {
			docs = append(docs, strings.Join(cur, "\n"))
			cur = cur[:0]
			continue
		}
		cur = append(cur, line)
	}
	return append(docs, strings.Join(cur, "\n"))
}
