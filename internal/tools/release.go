package tools

// Release is the default catalog identity for development builds. Artifact
// versions and manager versions are independent of the wire compatibility epoch.
const Release = "0.2.3"

const CompositionContract = "loki-tools-v2"

// CompatibleComposition preserves the homogeneous legacy contract. Explicit
// v2 manifests permit independent artifact versions in a selected composition.
func CompatibleComposition(contract, release string, manifest Manifest) bool {
	if contract == "" {
		return manifest.Contract == "" && manifest.Release == release
	}
	return contract == CompositionContract && manifest.Contract == contract
}
