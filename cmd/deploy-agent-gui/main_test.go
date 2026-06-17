package main

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/liqixin/deploy-agent/internal/gui/apiclient"
)

func TestCapOutputKeepsTailAndMarksTruncation(t *testing.T) {
	tail := "tail output"
	got := capOutput(strings.Repeat("x", maxOutputRunes) + tail)

	if !strings.HasPrefix(got, outputTruncatedNotice) {
		t.Fatalf("output prefix = %q, want truncation notice", got[:len(outputTruncatedNotice)])
	}
	if !strings.HasSuffix(got, tail) {
		t.Fatalf("output suffix = %q, want %q", got[len(got)-len(tail):], tail)
	}
	if utf8.RuneCountInString(got) > maxOutputRunes {
		t.Fatalf("output length = %d, want <= %d", utf8.RuneCountInString(got), maxOutputRunes)
	}
}

func TestRunErrorStatusRefreshesOnNotFound(t *testing.T) {
	status, refresh := runErrorStatus(apiclient.HTTPError{StatusCode: http.StatusNotFound, Message: "script not found"})

	if !refresh {
		t.Fatal("refresh = false, want true")
	}
	if !strings.Contains(status, "脚本不存在") {
		t.Fatalf("status = %q, want script-not-found message", status)
	}
}

func TestRunErrorStatusMapsCommonHTTPErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		want       string
	}{
		{name: "bad request", statusCode: http.StatusBadRequest, want: "请求错误"},
		{name: "unauthorized", statusCode: http.StatusUnauthorized, want: "鉴权失败"},
		{name: "conflict", statusCode: http.StatusConflict, want: "脚本正在运行"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, refresh := runErrorStatus(apiclient.HTTPError{StatusCode: tt.statusCode, Message: "message"})
			if refresh {
				t.Fatal("refresh = true, want false")
			}
			if !strings.Contains(status, tt.want) {
				t.Fatalf("status = %q, want it to contain %q", status, tt.want)
			}
		})
	}
}

func TestRunErrorStatusGenericError(t *testing.T) {
	status, refresh := runErrorStatus(errors.New("network down"))

	if refresh {
		t.Fatal("refresh = true, want false")
	}
	if !strings.Contains(status, "请求失败") {
		t.Fatalf("status = %q, want generic request failure", status)
	}
}

func TestPreDownloadOptionsDisabledReturnsEmptyOptions(t *testing.T) {
	opts, err := preDownloadOptions(false, "", "")
	if err != nil {
		t.Fatalf("preDownloadOptions returned error: %v", err)
	}
	if opts.PreDownload.Enabled {
		t.Fatalf("PreDownload.Enabled = true, want false")
	}
}

func TestPreDownloadOptionsRequiresProjectAndArtifact(t *testing.T) {
	tests := []struct {
		name      string
		project   string
		artifact  string
		wantError string
	}{
		{name: "project", project: "", artifact: "app.zip", wantError: "项目编号"},
		{name: "artifact", project: "ProjectA", artifact: "", wantError: "产物文件名"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := preDownloadOptions(true, tt.project, tt.artifact)
			if err == nil {
				t.Fatal("preDownloadOptions returned nil error, want missing field error")
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %q, want message containing %q", err.Error(), tt.wantError)
			}
		})
	}
}

func TestPreDownloadOptionsBuildsEnabledRequest(t *testing.T) {
	opts, err := preDownloadOptions(true, " ProjectA ", " app.zip ")
	if err != nil {
		t.Fatalf("preDownloadOptions returned error: %v", err)
	}
	if !opts.PreDownload.Enabled {
		t.Fatal("PreDownload.Enabled = false, want true")
	}
	if opts.PreDownload.Project != "ProjectA" {
		t.Fatalf("Project = %q, want ProjectA", opts.PreDownload.Project)
	}
	if opts.PreDownload.Artifact != "app.zip" {
		t.Fatalf("Artifact = %q, want app.zip", opts.PreDownload.Artifact)
	}
}

func TestPreDownloadInputsReadyAllowsDisabledInputs(t *testing.T) {
	if !preDownloadInputsReady(false, "", "") {
		t.Fatal("preDownloadInputsReady = false, want true when disabled")
	}
}

func TestPreDownloadInputsReadyRequiresEnabledInputs(t *testing.T) {
	if preDownloadInputsReady(true, "ProjectA", " ") {
		t.Fatal("preDownloadInputsReady = true, want false without artifact")
	}
	if !preDownloadInputsReady(true, " ProjectA ", " app.zip ") {
		t.Fatal("preDownloadInputsReady = false, want true with trimmed project and artifact")
	}
}
