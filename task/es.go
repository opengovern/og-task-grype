package task

type OciArtifactVulnerabilities struct {
	ImageURL             string                    `json:"imageUrl"`
	GrypeVulnerabilities []GrypeVulnerabilityMatch `json:"grypeVulnerabilities"`
}

func (r OciArtifactVulnerabilities) UniqueID() string {
	return r.ImageURL
}

type GrypeVulnerabilityMatch struct {
	Vulnerability          GrypeVulnerability   `json:"vulnerability"`
	RelatedVulnerabilities []GrypeVulnerability `json:"relatedVulnerabilities"`
	MatchDetail            interface{}          `json:"matchDetail"`
	Artifact               interface{}          `json:"artifact"`
}

type GrypeVulnerability struct {
	ID          string             `json:"id"`
	DataSource  string             `json:"dataSource"`
	Namespace   string             `json:"namespace"`
	Severity    string             `json:"severity"`
	URLs        []string           `json:"urls"`
	Description string             `json:"description"`
	CVSs        []VulnerabilityCVS `json:"cvss"`
	Fix         VulnerabilityFix   `json:"fix"`
	Advisories  interface{}        `json:"advisories"`
}

type VulnerabilityCVS struct {
	Source         string            `json:"source"`
	Type           string            `json:"type"`
	Version        string            `json:"version"`
	Vector         string            `json:"vector"`
	Metrics        map[string]string `json:"metrics"`
	VendorMetadata map[string]string `json:"vendorMetadata"`
}

type VulnerabilityFix struct {
	Versions []string `json:"versions"`
	State    string   `json:"state"`
}
