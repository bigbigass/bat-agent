package main

import (
	"fmt"
	"strings"

	"github.com/liqixin/deploy-agent/internal/gui/apiclient"
)

func preDownloadOptions(enabled bool, project string, artifact string) (apiclient.RunStreamOptions, error) {
	if !enabled {
		return apiclient.RunStreamOptions{}, nil
	}
	project = strings.TrimSpace(project)
	artifact = strings.TrimSpace(artifact)
	if project == "" {
		return apiclient.RunStreamOptions{}, fmt.Errorf("请填写项目编号")
	}
	if artifact == "" {
		return apiclient.RunStreamOptions{}, fmt.Errorf("请填写产物文件名")
	}
	return apiclient.RunStreamOptions{
		Args: []string{project, artifact},
		PreDownload: apiclient.PreDownloadOptions{
			Enabled:  true,
			Project:  project,
			Artifact: artifact,
		},
	}, nil
}

func preDownloadInputsReady(enabled bool, project string, artifact string) bool {
	if !enabled {
		return true
	}
	return strings.TrimSpace(project) != "" && strings.TrimSpace(artifact) != ""
}
