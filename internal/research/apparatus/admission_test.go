package apparatus

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

func TestSignedAdmissionCatalogBindsManifestSBOMAndProvenance(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	manifest := validManifest()
	envelope, err := SignAdmissionCatalog(testAdmissionCatalog(now, manifest), privateKey)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyAdmissionCatalog(envelope, publicKey, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := verified.Admit(manifest, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if admitted.SupplyChain == nil || admitted.SupplyChain.SBOMSHA256 == "" || admitted.SupplyChain.ProvenanceSHA256 == "" || admitted.SupplyChain.AdmissionKeyID != verified.KeyID() || admitted.SupplyChain.DependencyCount != 1 || admitted.SupplyChain.BuilderID != "https://builder.example.test/research" || len(admitted.SupplyChain.SBOM) == 0 || len(admitted.SupplyChain.Provenance) == 0 {
		t.Fatalf("admitted manifest=%#v", admitted)
	}
	if err := verified.ValidateAdmitted(admitted, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := verified.ValidateAdmitted(admitted, verified.ExpiresAt()); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired persisted admission accepted: %v", err)
	}
	tamperedEvidence := admitted
	tamperedSupply := *admitted.SupplyChain
	tamperedSupply.SBOM = append(json.RawMessage(nil), tamperedSupply.SBOM...)
	tamperedSupply.SBOM[10] ^= 1
	tamperedEvidence.SupplyChain = &tamperedSupply
	if err := ValidateManifest(tamperedEvidence); err == nil {
		t.Fatal("tampered persisted SBOM accepted")
	}

	// Mutating the decoded envelope after verification must not change admission.
	envelope.Catalog.Entries[0].Manifest.Version = "tampered"
	if _, err := verified.Admit(manifest, now.Add(time.Hour)); err != nil {
		t.Fatalf("verified catalog retained mutable caller state: %v", err)
	}
	changed := manifest
	changed.Version = "other"
	if _, err := verified.Admit(changed, now.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("changed manifest admitted: %v", err)
	}
	manifest.SupplyChain = admitted.SupplyChain
	if _, err := verified.Admit(manifest, now.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "caller cannot") {
		t.Fatalf("caller admission metadata accepted: %v", err)
	}
}

func TestAdmissionCatalogRejectsTamperingExpiryAndIncompleteEvidence(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now().UTC()
	catalog := testAdmissionCatalog(now, validManifest())
	envelope, err := SignAdmissionCatalog(catalog, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	var sbom map[string]any
	_ = json.Unmarshal(envelope.Catalog.Entries[0].SBOM, &sbom)
	sbom["name"] = "tampered but structurally valid"
	envelope.Catalog.Entries[0].SBOM, _ = json.Marshal(sbom)
	if _, err := VerifyAdmissionCatalog(envelope, publicKey, now.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "signature mismatch") {
		t.Fatalf("tampered catalog accepted: %v", err)
	}
	envelope, _ = SignAdmissionCatalog(catalog, privateKey)
	if _, err := VerifyAdmissionCatalog(envelope, publicKey, catalog.ExpiresAt); err == nil || !strings.Contains(err.Error(), "time policy") {
		t.Fatalf("expired catalog accepted: %v", err)
	}

	bad := testAdmissionCatalog(now, validManifest())
	bad.Entries[0].SBOM = json.RawMessage(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","dataLicense":"CC0-1.0","documentNamespace":"https://example.test/sbom","packages":[{"name":"pkg","SPDXID":"SPDXRef-Package","versionInfo":"1"}]}`)
	if _, err := SignAdmissionCatalog(bad, privateKey); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("SBOM without package digest accepted: %v", err)
	}
	bad = testAdmissionCatalog(now, validManifest())
	bad.Entries[0].Provenance = testProvenanceJSON("sha256:" + strings.Repeat("c", 64))
	if _, err := SignAdmissionCatalog(bad, privateKey); err == nil || !strings.Contains(err.Error(), "bind the image") {
		t.Fatalf("provenance for another image accepted: %v", err)
	}
	bad = testAdmissionCatalog(now, validManifest())
	bad.Entries[0].SBOM = json.RawMessage(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","dataLicense":"CC0-1.0","documentNamespace":"https://example.test/sbom","packages":[{"name":"pkg","SPDXID":"SPDXRef-Package","versionInfo":"NOASSERTION","checksums":[{"algorithm":"SHA256","checksumValue":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}]}`)
	if _, err := SignAdmissionCatalog(bad, privateKey); err == nil || !strings.Contains(err.Error(), "pinned version") {
		t.Fatalf("SBOM without a pinned package version accepted: %v", err)
	}
}

func testAdmissionCatalog(now time.Time, manifest domain.ApparatusManifest) AdmissionCatalog {
	return AdmissionCatalog{
		SchemaVersion: 1, IssuedAt: now, ExpiresAt: now.Add(24 * time.Hour),
		Entries: []AdmissionEntry{{Manifest: manifest, SBOM: testSBOMJSON(), Provenance: testProvenanceJSON(manifest.ImageDigest)}},
	}
}

func testSBOMJSON() json.RawMessage {
	return json.RawMessage(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","name":"fixture","dataLicense":"CC0-1.0","documentNamespace":"https://example.test/sbom/fixture","packages":[{"name":"clang","SPDXID":"SPDXRef-Package-clang","versionInfo":"18.1.0","checksums":[{"algorithm":"SHA256","checksumValue":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}]}`)
}

func testProvenanceJSON(imageDigest string) json.RawMessage {
	return json.RawMessage(`{"_type":"https://in-toto.io/Statement/v1","subject":[{"name":"apparatus","digest":{"sha256":"` + strings.TrimPrefix(imageDigest, "sha256:") + `"}}],"predicateType":"https://slsa.dev/provenance/v1","predicate":{"buildDefinition":{"buildType":"https://example.test/docker-build/v1","resolvedDependencies":[{"uri":"pkg:docker/debian@bookworm","digest":{"sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}]},"runDetails":{"builder":{"id":"https://builder.example.test/research"}}}}`)
}
