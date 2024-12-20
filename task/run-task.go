package task

import (
	"encoding/json"
	"fmt"
	"github.com/opengovern/og-util/pkg/es"
	"github.com/opengovern/og-util/pkg/opengovernance-es-sdk"
	"github.com/opengovern/og-util/pkg/tasks"
	"github.com/opengovern/opencomply/services/tasks/scheduler"
	"go.uber.org/zap"
	"golang.org/x/net/context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func RunTask(ctx context.Context, esClient opengovernance.Client, logger *zap.Logger, request tasks.TaskRequest, response *scheduler.TaskResponse) error {
	var registryType string
	if v, ok := request.TaskDefinition.Params["oci_artifact_url"]; !(ok && len(v) > 0) {
		return fmt.Errorf("OCI artifact url parameter is not provided")
	}
	if v, ok := request.TaskDefinition.Params["registry_type"]; ok && len(v) > 0 {
		registryType = v[0]
	} else {
		registryType = "ghcr"
	}
	if v, ok := request.TaskDefinition.Params["artifact_digest"]; !(ok && len(v) > 0) {
		return fmt.Errorf("OCI artifact digest parameter is not provided")
	}

	var ids []string
	var index string
	for i, artifactUrl := range request.TaskDefinition.Params["oci_artifact_url"] {
		var artifactDigest string
		if len(request.TaskDefinition.Params["artifact_digest"]) >= (i + 1) {
			artifactDigest = request.TaskDefinition.Params["artifact_digest"][i]
		}
		logger.Info("Fetching image", zap.String("image", artifactUrl))

		err := fetchImage(registryType, fmt.Sprintf("run-%v", request.TaskDefinition.RunID), artifactUrl, getCredsFromParams(request.TaskDefinition.Params))
		if err != nil {
			logger.Error("failed to fetch image", zap.String("image", artifactUrl), zap.Error(err))
			return err
		}

		err = showFiles(fmt.Sprintf("run-%v", request.TaskDefinition.RunID))
		if err != nil {
			logger.Error("failed to show files", zap.Error(err))
			return err
		}

		logger.Info("Scanning image", zap.String("image", "image.tar"))

		// Run the Grype command
		cmd := exec.Command("grype", fmt.Sprintf("run-%v/%s", request.TaskDefinition.RunID, "image.tar"), "-o", "json")

		output, err := cmd.CombinedOutput()
		logger.Info("output", zap.String("output", string(output)))
		if err != nil {
			logger.Error("error running grype script", zap.Error(err))
			return err
		}

		var grypeOutput GrypeOutput
		err = json.Unmarshal(output, &grypeOutput)

		logger.Info("grypeOutput", zap.Any("grypeOutput", grypeOutput))

		result := OciArtifactVulnerabilities{
			ImageURL:        artifactUrl,
			ArtifactDigest:  artifactDigest,
			Vulnerabilities: grypeOutput.Matches,
		}

		esResult := &es.TaskResult{
			PlatformID:   fmt.Sprintf("%s:::%s:::%s", request.TaskDefinition.TaskType, request.TaskDefinition.ResultType, result.UniqueID()),
			ResourceID:   result.UniqueID(),
			ResourceName: artifactUrl,
			Description:  result,
			ResultType:   strings.ToLower(request.TaskDefinition.ResultType),
			TaskType:     request.TaskDefinition.TaskType,
			Metadata:     nil,
			DescribedAt:  time.Now().Unix(),
			DescribedBy:  strconv.FormatUint(uint64(request.TaskDefinition.RunID), 10),
		}

		keys, idx := esResult.KeysAndIndex()
		esResult.EsID = es.HashOf(keys...)
		esResult.EsIndex = idx

		err = sendDataToOpensearch(esClient.ES(), esResult)
		if err != nil {
			return err
		}

		ids = append(ids, es.HashOf(keys...))
		index = idx
	}

	resultMessage := fmt.Sprintf("Responses stored in elasticsearch index %s by ids: %v", index, ids)
	response.Result = []byte(resultMessage)

	return nil
}
