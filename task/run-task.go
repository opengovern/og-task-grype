package task

import (
	"fmt"
	"github.com/opengovern/opencomply/services/tasks/db/models"
	"github.com/opengovern/opencomply/services/tasks/scheduler"
	"go.uber.org/zap"
	"golang.org/x/net/context"
	"io/ioutil"
	"os/exec"
)

func RunTask(ctx context.Context, logger *zap.Logger, request scheduler.TaskRequest) (*scheduler.TaskResponse, error) {
	var response scheduler.TaskResponse

	var ociArtifactURL, registryType string
	if v, ok := request.Params["oci_artifact_url"]; ok {
		ociArtifactURL = v
	} else {
		return nil, fmt.Errorf("OCI artifact url parameter is not provided")
	}
	if v, ok := request.Params["registry_type"]; ok {
		registryType = v
	} else {
		registryType = "ghcr"
	}

	logger.Info("Fetching image", zap.String("image", ociArtifactURL))

	err := fetchImage(registryType, fmt.Sprintf("run-%v", request.RunID), ociArtifactURL, getCredsFromParams(request.Params))
	if err != nil {
		logger.Error("failed to fetch image", zap.String("image", ociArtifactURL), zap.Error(err))
		return nil, err
	}

	err = showFiles(fmt.Sprintf("run-%v", request.RunID))
	if err != nil {
		logger.Error("failed to show files", zap.Error(err))
		return nil, err
	}

	logger.Info("Scanning image", zap.String("image", "image.tar"))

	// Run the Grype command
	cmd := exec.Command("grype", fmt.Sprintf("run-%v/%s", request.RunID, "image.tar"))

	output, err := cmd.CombinedOutput()
	logger.Info("output", zap.String("output", string(output)))
	if err != nil {
		logger.Error("error running grype script", zap.Error(err))
		return nil, err
	}

	response.Result = output
	response.RunID = request.RunID
	response.Status = models.TaskRunStatusFinished

	return &response, nil
}

func getCredsFromParams(params map[string]string) Credentials {
	creds := Credentials{}
	for k, v := range params {
		switch k {
		case "github_username":
			creds.GithubUsername = v
		case "github_token":
			creds.GithubToken = v
		case "ecr_account_id":
			creds.ECRAccountID = v
		case "ecr_region":
			creds.ECRRegion = v
		case "acr_login_server":
			creds.ACRLoginServer = v
		case "acr_tenant_id":
			creds.ACRTenantID = v
		}
	}
	return creds
}

func showFiles(dir string) error {
	// List the files in the current directory
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return err
	}

	// Print each file or directory name
	fmt.Printf("Listing files in directory: %s\n", dir)
	for _, file := range files {
		if file.IsDir() {
			fmt.Printf("[DIR] %s\n", file.Name())
		} else {
			fmt.Printf("[FILE] %s\n", file.Name())
		}
	}
	return nil
}
