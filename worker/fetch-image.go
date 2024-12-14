package worker

import (
	"archive/tar"
	"encoding/base64"
	"encoding/json"
	"fmt"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/opengovern/resilient-bridge/utils"
	"golang.org/x/net/context"
	"io"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"os"
	"path/filepath"
	"strings"
)

type AuthConfig struct {
	Auth string `json:"auth,omitempty"`
}

type DockerConfig struct {
	Auths map[string]AuthConfig `json:"auths"`
}

type RegistryType string

const (
	RegistryGHCR RegistryType = "ghcr"
	RegistryECR  RegistryType = "ecr"
	RegistryACR  RegistryType = "acr"
)

type Credentials struct {
	GithubUsername string `json:"github_username"`
	GithubToken    string `json:"github_token"`

	ECRAccountID string `json:"ecr_account_id"`
	ECRRegion    string `json:"ecr_region"`

	ACRLoginServer string `json:"acr_login_server"`
	ACRTenantID    string `json:"acr_tenant_id"`
}

// AllowedMediaTypes defines the permitted OCI and Docker-compatible media types that are acceptable.
var AllowedMediaTypes = []string{
	"application/vnd.oci.descriptor.v1+json",
	"application/vnd.oci.layout.header.v1+json",
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.oci.image.config.v1+json",
	"application/vnd.oci.image.layer.v1.tar",
	"application/vnd.oci.image.layer.v1.tar+gzip",
	"application/vnd.oci.image.layer.v1.tar+zstd",
	"application/vnd.oci.empty.v1+json",

	// Non-distributable (deprecated) layers
	"application/vnd.oci.image.layer.nondistributable.v1.tar",
	"application/vnd.oci.image.layer.nondistributable.v1.tar+gzip",
	"application/vnd.oci.image.layer.nondistributable.v1.tar+zstd",

	// Docker compatible types if needed:
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.docker.image.rootfs.diff.tar.gzip",
	"application/vnd.docker.container.image.v1+json",
}

func fetchImage(registryType, output, ociArtifactURI string, creds Credentials) error {
	cfg := DockerConfig{
		Auths: make(map[string]AuthConfig),
	}

	switch RegistryType(registryType) {
	case RegistryGHCR:
		ghcrCreds, err := utils.GetGHCRCredentials(creds.GithubUsername, creds.GithubToken)
		if err != nil {
			return fmt.Errorf("GHCR error: %v\n", err)
		}
		ghcrAuth := map[string]AuthConfig{}
		for host, val := range ghcrCreds {
			ghcrAuth[host] = AuthConfig{Auth: val}
		}
		mergeAuths(cfg.Auths, ghcrAuth)
	default:
		return fmt.Errorf("Unsupported registry type: %s\n", registryType)
	}

	// If user requested, write out the credentials to a file or print them
	configBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("Error marshaling config to JSON: %v\n", err)
	}

	if output == "" {
		fmt.Println(string(configBytes))
	} else {
		dir := filepath.Dir(output)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("Error creating directory for output file: %v\n", err)
		}
		err = os.WriteFile(output, configBytes, 0600)
		if err != nil {
			return fmt.Errorf("Error writing to output file: %v\n", err)
		}
		fmt.Printf("Credentials written to %s\n", output)
	}

	if err := pullAndCreateDockerArchive(ociArtifactURI, cfg); err != nil {
		return fmt.Errorf("Failed to process %s: %v\n", ociArtifactURI, err)
	} else {
		fmt.Printf("Successfully created image.tar for %s.\n", ociArtifactURI)
	}
	return nil
}

// loadDockerConfigFile loads a Docker config.json file from the specified path.
// It returns a DockerConfig object on success.
func loadDockerConfigFile(path string) (DockerConfig, error) {
	var dc DockerConfig
	bytes, err := os.ReadFile(path)
	if err != nil {
		return dc, fmt.Errorf("failed to read file: %w", err)
	}
	if err := json.Unmarshal(bytes, &dc); err != nil {
		return dc, fmt.Errorf("failed to unmarshal docker config.json: %w", err)
	}
	if dc.Auths == nil {
		dc.Auths = make(map[string]AuthConfig)
	}
	return dc, nil
}

// pullAndCreateDockerArchive pulls the OCI image specified by ociArtifactURI using the given DockerConfig credentials,
// converts it to a Docker archive (image.tar) that Grype can scan, and writes out all intermediate files.
//
// Steps:
// 1. Parse the reference for the OCI image.
// 2. Set up an auth client using the provided credentials.
// 3. Use oras.Copy to retrieve the OCI image into an in-memory store.
// 4. Extract manifest, config, layers into local files: config.json, manifest.json (Docker-style), oci-manifest.json, and layers.
// 5. Validate the OCI artifact's media types.
// 6. Package all these files into a tarball named image.tar.
// 7. Remove manifest.json and oci-manifest.json after creating the tar.
func pullAndCreateDockerArchive(ociArtifactURI string, cfg DockerConfig) error {
	ctx := context.Background()

	ref, err := registry.ParseReference(ociArtifactURI)
	if err != nil {
		return fmt.Errorf("invalid oci-artifact-uri: %w", err)
	}

	credentialsFunc := auth.CredentialFunc(func(ctx context.Context, host string) (auth.Credential, error) {
		if a, ok := cfg.Auths[host]; ok {
			decoded, err := base64.StdEncoding.DecodeString(a.Auth)
			if err != nil {
				return auth.Credential{}, err
			}
			parts := strings.SplitN(string(decoded), ":", 2)
			if len(parts) != 2 {
				return auth.Credential{}, fmt.Errorf("invalid auth format for %s", host)
			}
			return auth.Credential{
				Username: parts[0],
				Password: parts[1],
			}, nil
		}
		return auth.Credential{}, fmt.Errorf("no credentials for host %s", host)
	})

	authClient := &auth.Client{
		Credential: credentialsFunc,
	}

	repo, err := remote.NewRepository(ref.String())
	if err != nil {
		return fmt.Errorf("failed to create repository object: %w", err)
	}
	repo.Client = authClient

	// Pull the image into memory store
	desc, err := oras.Copy(ctx, repo, ref.Reference, memoryStore, "", oras.DefaultCopyOptions)
	if err != nil {
		return fmt.Errorf("oras pull failed: %w", err)
	}

	// Fetch and read the manifest content
	rc, err := memoryStore.Fetch(ctx, desc)
	if err != nil {
		return fmt.Errorf("failed to fetch manifest: %w", err)
	}
	defer rc.Close()

	manifestContent, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("failed to read manifest: %w", err)
	}

	// Write out the OCI manifest for reference as oci-manifest.json
	if err := os.WriteFile("oci-manifest.json", manifestContent, 0644); err != nil {
		return fmt.Errorf("failed to write oci-manifest.json: %w", err)
	}

	// Parse the manifest as OCI image manifest
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestContent, &manifest); err != nil {
		return fmt.Errorf("failed to unmarshal manifest: %w", err)
	}

	// Validate that all media types in manifest are allowed
	if err := validateOCIMediaTypes(manifest); err != nil {
		return fmt.Errorf("media type validation failed: %w", err)
	}

	// Fetch config
	configDesc := manifest.Config
	configRC, err := memoryStore.Fetch(ctx, configDesc)
	if err != nil {
		return fmt.Errorf("failed to fetch config: %w", err)
	}
	defer configRC.Close()
	configBytes, err := io.ReadAll(configRC)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	// Write config.json
	if err := os.WriteFile("config.json", configBytes, 0644); err != nil {
		return fmt.Errorf("failed to write config.json: %w", err)
	}

	// Fetch layers and write them out as layer tarfiles
	var layerFiles []string
	for i, layerDesc := range manifest.Layers {
		layerRC, err := memoryStore.Fetch(ctx, layerDesc)
		if err != nil {
			return fmt.Errorf("failed to fetch layer: %w", err)
		}
		layerBytes, err := io.ReadAll(layerRC)
		layerRC.Close()
		if err != nil {
			return fmt.Errorf("failed to read layer: %w", err)
		}
		layerFileName := fmt.Sprintf("layer%d.tar", i+1)
		if err := os.WriteFile(layerFileName, layerBytes, 0644); err != nil {
			return fmt.Errorf("failed to write layer to disk: %w", err)
		}
		layerFiles = append(layerFiles, layerFileName)
	}

	// Create a Docker-style manifest.json for the image.tar
	repoTag := ref.String()
	dockerManifest := []map[string]interface{}{
		{
			"Config":   "config.json",
			"RepoTags": []string{repoTag},
			"Layers":   layerFiles,
		},
	}
	dockerManifestBytes, err := json.MarshalIndent(dockerManifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal docker manifest.json: %w", err)
	}
	if err := os.WriteFile("manifest.json", dockerManifestBytes, 0644); err != nil {
		return fmt.Errorf("failed to write manifest.json: %w", err)
	}

	// Finally, create image.tar containing manifest.json, config.json, oci-manifest.json, and layers
	filesToTar := append([]string{"manifest.json", "config.json", "oci-manifest.json"}, layerFiles...)
	if err := createTar("image.tar", filesToTar); err != nil {
		return fmt.Errorf("failed to create tar: %w", err)
	}

	// Remove manifest.json and oci-manifest.json after creating the tar
	if err := os.Remove("manifest.json"); err != nil {
		return fmt.Errorf("failed to remove manifest.json: %w", err)
	}
	if err := os.Remove("oci-manifest.json"); err != nil {
		return fmt.Errorf("failed to remove oci-manifest.json: %w", err)
	}

	return nil
}

// validateOCIMediaTypes checks if the config and layers in the manifest use allowed media types.
func validateOCIMediaTypes(manifest ocispec.Manifest) error {
	if !isAllowedMediaType(manifest.Config.MediaType) {
		return fmt.Errorf("config media type %q is not allowed", manifest.Config.MediaType)
	}
	for _, layer := range manifest.Layers {
		if !isAllowedMediaType(layer.MediaType) {
			return fmt.Errorf("layer media type %q is not allowed", layer.MediaType)
		}
	}
	return nil
}

// isAllowedMediaType checks if the provided media type is in AllowedMediaTypes.
func isAllowedMediaType(mt string) bool {
	for _, allowed := range AllowedMediaTypes {
		if mt == allowed {
			return true
		}
	}
	return false
}

// createTar creates a tarball at tarPath containing the specified files.
// Each file is added to the tar archive in the given order.
func createTar(tarPath string, files []string) error {
	tarFile, err := os.Create(tarPath)
	if err != nil {
		return fmt.Errorf("failed to create tar file: %w", err)
	}
	defer tarFile.Close()

	tw := tar.NewWriter(tarFile)
	defer tw.Close()

	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			return fmt.Errorf("failed to stat file %s: %w", file, err)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("failed to create tar header for %s: %w", file, err)
		}
		header.Name = file
		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("failed to write header for %s: %w", file, err)
		}
		fh, err := os.Open(file)
		if err != nil {
			return fmt.Errorf("failed to open file %s: %w", file, err)
		}
		if _, err := io.Copy(tw, fh); err != nil {
			fh.Close()
			return fmt.Errorf("failed to copy file data for %s: %w", file, err)
		}
		fh.Close()
	}
	return nil
}

// mergeAuths merges authentication entries from src into dst.
func mergeAuths(dst, src map[string]AuthConfig) {
	for k, v := range src {
		dst[k] = v
	}
}

// memoryStore is a global in-memory content store used for oras.Copy operations.
var memoryStore = memory.New()
