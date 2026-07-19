package apparatus

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

const (
	MaxAdmissionLifetime = 90 * 24 * time.Hour
	admissionClockSkew   = 5 * time.Minute
	admissionDomain      = "agent-smith/apparatus-admission/v1"
	maxAdmissionEntries  = 64
)

// AdmissionEntry binds one exact apparatus manifest to its SPDX SBOM and SLSA
// provenance statement. Raw documents are signed as part of the catalog.
type AdmissionEntry struct {
	Manifest   domain.ApparatusManifest `json:"manifest"`
	SBOM       json.RawMessage          `json:"sbom"`
	Provenance json.RawMessage          `json:"provenance"`
}

// AdmissionCatalog is the operator-reviewed, short-lived signed payload.
type AdmissionCatalog struct {
	SchemaVersion int              `json:"schema_version"`
	IssuedAt      time.Time        `json:"issued_at"`
	ExpiresAt     time.Time        `json:"expires_at"`
	Entries       []AdmissionEntry `json:"entries"`
}

// SignedAdmissionCatalog is an Ed25519-authenticated catalog envelope.
type SignedAdmissionCatalog struct {
	SchemaVersion int              `json:"schema_version"`
	KeyID         string           `json:"key_id"`
	Catalog       AdmissionCatalog `json:"catalog"`
	Signature     string           `json:"signature"`
}

type admittedApparatus struct {
	manifest         domain.ApparatusManifest
	sbomSHA256       string
	provenanceSHA256 string
	builderID        string
	dependencyCount  int
	sbom             json.RawMessage
	provenance       json.RawMessage
}

// VerifiedAdmissionCatalog cannot be constructed from caller-controlled JSON.
type VerifiedAdmissionCatalog struct {
	keyID     string
	expiresAt time.Time
	entries   map[string]admittedApparatus
}

func (catalog *VerifiedAdmissionCatalog) KeyID() string {
	if catalog == nil {
		return ""
	}
	return catalog.keyID
}

func (catalog *VerifiedAdmissionCatalog) ExpiresAt() time.Time {
	if catalog == nil {
		return time.Time{}
	}
	return catalog.expiresAt
}

// Admit requires an exact canonical manifest match and returns supply-chain
// metadata derived from the signed documents, never from the API request.
func (catalog *VerifiedAdmissionCatalog) Admit(manifest domain.ApparatusManifest, now time.Time) (domain.ApparatusManifest, error) {
	if catalog == nil {
		return manifest, errors.New("apparatus: verified supply-chain admission required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if !now.Before(catalog.expiresAt) {
		return manifest, errors.New("apparatus: supply-chain admission expired")
	}
	if manifest.SupplyChain != nil {
		return manifest, errors.New("apparatus: caller cannot supply admission metadata")
	}
	entry, ok := catalog.entries[manifest.ID]
	if !ok || !sameManifest(entry.manifest, manifest) {
		return manifest, errors.New("apparatus: manifest is not present in the signed admission catalog")
	}
	manifest.SupplyChain = &domain.ApparatusSupplyChain{
		SchemaVersion: 1, SBOMSHA256: entry.sbomSHA256, ProvenanceSHA256: entry.provenanceSHA256,
		AdmissionKeyID: catalog.keyID, AdmissionExpiresAt: catalog.expiresAt,
		BuilderID: entry.builderID, DependencyCount: entry.dependencyCount,
		SBOM: append(json.RawMessage(nil), entry.sbom...), Provenance: append(json.RawMessage(nil), entry.provenance...),
	}
	return manifest, nil
}

// ValidateAdmitted rechecks a persisted manifest against the currently trusted
// catalog so an expired or replaced admission cannot continue launching jobs.
func (catalog *VerifiedAdmissionCatalog) ValidateAdmitted(manifest domain.ApparatusManifest, now time.Time) error {
	if manifest.SupplyChain == nil {
		return errors.New("apparatus: persisted manifest lacks supply-chain admission")
	}
	existing := *manifest.SupplyChain
	manifest.SupplyChain = nil
	admitted, err := catalog.Admit(manifest, now)
	if err != nil {
		return err
	}
	admittedJSON, admittedErr := json.Marshal(admitted.SupplyChain)
	existingJSON, existingErr := json.Marshal(existing)
	if admitted.SupplyChain == nil || admittedErr != nil || existingErr != nil || !bytes.Equal(admittedJSON, existingJSON) {
		return errors.New("apparatus: persisted admission does not match the trusted catalog")
	}
	return nil
}

func SignAdmissionCatalog(catalog AdmissionCatalog, privateKey ed25519.PrivateKey) (SignedAdmissionCatalog, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return SignedAdmissionCatalog{}, errors.New("apparatus: invalid Ed25519 private key")
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return SignedAdmissionCatalog{}, errors.New("apparatus: private key has no Ed25519 identity")
	}
	keyID, err := admissionKeyID(publicKey)
	if err != nil {
		return SignedAdmissionCatalog{}, err
	}
	if _, err := validateAdmissionCatalog(catalog, catalog.IssuedAt); err != nil {
		return SignedAdmissionCatalog{}, err
	}
	envelope := SignedAdmissionCatalog{SchemaVersion: 1, KeyID: keyID, Catalog: catalog}
	payload, err := admissionSigningPayload(envelope)
	if err != nil {
		return SignedAdmissionCatalog{}, err
	}
	envelope.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	return envelope, nil
}

func VerifyAdmissionCatalog(envelope SignedAdmissionCatalog, publicKey ed25519.PublicKey, now time.Time) (*VerifiedAdmissionCatalog, error) {
	keyID, err := admissionKeyID(publicKey)
	if err != nil {
		return nil, err
	}
	if envelope.SchemaVersion != 1 || envelope.KeyID != keyID {
		return nil, errors.New("apparatus: admission envelope identity mismatch")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	entries, err := validateAdmissionCatalog(envelope.Catalog, now)
	if err != nil {
		return nil, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.RawURLEncoding.EncodeToString(signature) != envelope.Signature {
		return nil, errors.New("apparatus: invalid admission signature encoding")
	}
	payload, err := admissionSigningPayload(envelope)
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return nil, errors.New("apparatus: admission signature mismatch")
	}
	return &VerifiedAdmissionCatalog{keyID: keyID, expiresAt: envelope.Catalog.ExpiresAt, entries: entries}, nil
}

func validateAdmissionCatalog(catalog AdmissionCatalog, now time.Time) (map[string]admittedApparatus, error) {
	if catalog.SchemaVersion != 1 || catalog.IssuedAt.IsZero() || catalog.ExpiresAt.IsZero() || len(catalog.Entries) == 0 || len(catalog.Entries) > maxAdmissionEntries {
		return nil, errors.New("apparatus: invalid admission catalog identity or entry count")
	}
	lifetime := catalog.ExpiresAt.Sub(catalog.IssuedAt)
	if lifetime <= 0 || lifetime > MaxAdmissionLifetime || now.Before(catalog.IssuedAt.Add(-admissionClockSkew)) || !now.Before(catalog.ExpiresAt) {
		return nil, errors.New("apparatus: admission catalog expired or outside time policy")
	}
	entries := make(map[string]admittedApparatus, len(catalog.Entries))
	for _, entry := range catalog.Entries {
		if len(entry.SBOM) == 0 || len(entry.SBOM) > 4<<20 || len(entry.Provenance) == 0 || len(entry.Provenance) > 4<<20 {
			return nil, errors.New("apparatus: signed SBOM or provenance exceeds bounds")
		}
		if entry.Manifest.SupplyChain != nil {
			return nil, errors.New("apparatus: unsigned manifest cannot contain admission metadata")
		}
		if err := ValidateManifest(entry.Manifest); err != nil {
			return nil, err
		}
		if _, duplicate := entries[entry.Manifest.ID]; duplicate {
			return nil, errors.New("apparatus: duplicate admission manifest id")
		}
		dependencyCount, err := validateSPDX(entry.SBOM)
		if err != nil {
			return nil, err
		}
		builderID, err := validateSLSAProvenance(entry.Provenance, entry.Manifest.ImageDigest)
		if err != nil {
			return nil, err
		}
		sbomCanonical, err := canonicalJSON(entry.SBOM)
		if err != nil {
			return nil, err
		}
		provenanceCanonical, err := canonicalJSON(entry.Provenance)
		if err != nil {
			return nil, err
		}
		sbomDigest := sha256.Sum256(sbomCanonical)
		provenanceDigest := sha256.Sum256(provenanceCanonical)
		entries[entry.Manifest.ID] = admittedApparatus{
			manifest: cloneManifest(entry.Manifest), sbomSHA256: "sha256:" + hex.EncodeToString(sbomDigest[:]),
			provenanceSHA256: "sha256:" + hex.EncodeToString(provenanceDigest[:]), builderID: builderID, dependencyCount: dependencyCount,
			sbom: append(json.RawMessage(nil), sbomCanonical...), provenance: append(json.RawMessage(nil), provenanceCanonical...),
		}
	}
	return entries, nil
}

func validateSPDX(raw json.RawMessage) (int, error) {
	var document struct {
		SPDXVersion       string `json:"spdxVersion"`
		SPDXID            string `json:"SPDXID"`
		DataLicense       string `json:"dataLicense"`
		DocumentNamespace string `json:"documentNamespace"`
		Packages          []struct {
			Name        string `json:"name"`
			SPDXID      string `json:"SPDXID"`
			VersionInfo string `json:"versionInfo"`
			Checksums   []struct {
				Algorithm     string `json:"algorithm"`
				ChecksumValue string `json:"checksumValue"`
			} `json:"checksums"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return 0, errors.New("apparatus: invalid SPDX JSON SBOM")
	}
	if !strings.HasPrefix(document.SPDXVersion, "SPDX-2.") || document.SPDXID != "SPDXRef-DOCUMENT" || document.DataLicense == "" || document.DocumentNamespace == "" || len(document.DocumentNamespace) > 2048 || len(document.Packages) == 0 || len(document.Packages) > 10000 {
		return 0, errors.New("apparatus: incomplete SPDX JSON SBOM")
	}
	seen := make(map[string]bool, len(document.Packages))
	for _, pkg := range document.Packages {
		if pkg.Name == "" || len(pkg.Name) > 1024 || pkg.SPDXID == "" || len(pkg.SPDXID) > 1024 || !isPinnedSPDXValue(pkg.VersionInfo) || len(pkg.VersionInfo) > 1024 || seen[pkg.SPDXID] {
			return 0, errors.New("apparatus: SBOM packages require unique identity and pinned version")
		}
		seen[pkg.SPDXID] = true
		validChecksum := false
		for _, checksum := range pkg.Checksums {
			if checksum.Algorithm == "SHA256" && isLowerHexDigest(checksum.ChecksumValue) {
				validChecksum = true
			}
		}
		if !validChecksum {
			return 0, errors.New("apparatus: every SBOM package requires a SHA-256 checksum")
		}
	}
	return len(document.Packages), nil
}

func isPinnedSPDXValue(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && trimmed == value && trimmed != "NOASSERTION" && trimmed != "NONE"
}

func validateSLSAProvenance(raw json.RawMessage, imageDigest string) (string, error) {
	var statement struct {
		Type          string `json:"_type"`
		PredicateType string `json:"predicateType"`
		Subject       []struct {
			Digest map[string]string `json:"digest"`
		} `json:"subject"`
		Predicate struct {
			BuildDefinition struct {
				BuildType            string `json:"buildType"`
				ResolvedDependencies []struct {
					URI    string            `json:"uri"`
					Digest map[string]string `json:"digest"`
				} `json:"resolvedDependencies"`
			} `json:"buildDefinition"`
			RunDetails struct {
				Builder struct {
					ID string `json:"id"`
				} `json:"builder"`
			} `json:"runDetails"`
		} `json:"predicate"`
	}
	if err := json.Unmarshal(raw, &statement); err != nil {
		return "", errors.New("apparatus: invalid SLSA provenance JSON")
	}
	if statement.Type != "https://in-toto.io/Statement/v1" || statement.PredicateType != "https://slsa.dev/provenance/v1" || statement.Predicate.BuildDefinition.BuildType == "" || len(statement.Predicate.BuildDefinition.BuildType) > 2048 || statement.Predicate.RunDetails.Builder.ID == "" || len(statement.Predicate.RunDetails.Builder.ID) > 2048 {
		return "", errors.New("apparatus: incomplete SLSA provenance statement")
	}
	wanted := strings.TrimPrefix(imageDigest, "sha256:")
	subjectMatch := false
	for _, subject := range statement.Subject {
		if subject.Digest["sha256"] == wanted {
			subjectMatch = true
		}
	}
	dependencies := statement.Predicate.BuildDefinition.ResolvedDependencies
	if !subjectMatch || len(dependencies) == 0 || len(dependencies) > 4096 {
		return "", errors.New("apparatus: provenance must bind the image and resolved dependencies")
	}
	for _, dependency := range dependencies {
		if dependency.URI == "" || len(dependency.URI) > 4096 || !isLowerHexDigest(dependency.Digest["sha256"]) {
			return "", errors.New("apparatus: provenance dependencies require URI and SHA-256")
		}
	}
	return statement.Predicate.RunDetails.Builder.ID, nil
}

func admissionKeyID(publicKey ed25519.PublicKey) (string, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return "", errors.New("apparatus: invalid Ed25519 public key")
	}
	digest := sha256.Sum256(publicKey)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func admissionSigningPayload(envelope SignedAdmissionCatalog) ([]byte, error) {
	return json.Marshal(struct {
		Domain        string           `json:"domain"`
		SchemaVersion int              `json:"schema_version"`
		KeyID         string           `json:"key_id"`
		Catalog       AdmissionCatalog `json:"catalog"`
	}{Domain: admissionDomain, SchemaVersion: envelope.SchemaVersion, KeyID: envelope.KeyID, Catalog: envelope.Catalog})
}

func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, fmt.Errorf("apparatus: invalid signed JSON document: %w", err)
	}
	return compact.Bytes(), nil
}

func sameManifest(left, right domain.ApparatusManifest) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func cloneManifest(manifest domain.ApparatusManifest) domain.ApparatusManifest {
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return domain.ApparatusManifest{}
	}
	var cloned domain.ApparatusManifest
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return domain.ApparatusManifest{}
	}
	return cloned
}

func isLowerHexDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
