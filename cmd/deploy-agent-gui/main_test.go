package main

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	fyneTest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/liqixin/deploy-agent/internal/appservice"
	"github.com/liqixin/deploy-agent/internal/gui/apiclient"
	"github.com/liqixin/deploy-agent/internal/gui/guiconfig"
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
	if len(opts.Args) != 0 {
		t.Fatalf("Args = %#v, want empty", opts.Args)
	}
	if opts.PreDownload.Enabled {
		t.Fatal("PreDownload.Enabled = true, want false")
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

func TestPreDownloadOptionsBuildsDownloadAndTargetArgsRequest(t *testing.T) {
	opts, err := preDownloadOptions(true, " ProjectA ", " app.zip ")
	if err != nil {
		t.Fatalf("preDownloadOptions returned error: %v", err)
	}
	if strings.Join(opts.Args, ",") != "ProjectA,app.zip" {
		t.Fatalf("Args = %#v, want ProjectA and app.zip", opts.Args)
	}
	if !opts.PreDownload.Enabled {
		t.Fatal("PreDownload.Enabled = false, want true")
	}
	if opts.PreDownload.Project != "ProjectA" {
		t.Fatalf("PreDownload.Project = %q, want ProjectA", opts.PreDownload.Project)
	}
	if opts.PreDownload.Artifact != "app.zip" {
		t.Fatalf("PreDownload.Artifact = %q, want app.zip", opts.PreDownload.Artifact)
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

func TestUpdateModeControlsTogglesRemoteFields(t *testing.T) {
	remoteFields := widget.NewLabel("remote connection fields")
	state := &guiState{
		config:       guiconfig.Config{Mode: guiconfig.ModeLocal},
		remoteFields: remoteFields,
	}

	state.updateModeControls()

	if remoteFields.Visible() {
		t.Fatal("remote fields are visible in local mode, want hidden")
	}

	state.config.Mode = guiconfig.ModeRemote
	state.updateModeControls()

	if !remoteFields.Visible() {
		t.Fatal("remote fields are hidden in remote mode, want visible")
	}
}

type fakeLocalHTTPProvider struct {
	cfg appservice.HTTPClientConfig
	ok  bool
}

func (f fakeLocalHTTPProvider) HTTPClientConfig() (appservice.HTTPClientConfig, bool) {
	return f.cfg, f.ok
}

func TestClientConfigForLocalModeUsesEmbeddedServiceAndIgnoresGUISecrets(t *testing.T) {
	guiCfg := guiconfig.Config{
		Mode:     guiconfig.ModeLocal,
		BaseURL:  "http://wrong:9999",
		Username: "wrong-user",
		Password: "wrong-password",
	}
	local := fakeLocalHTTPProvider{
		ok: true,
		cfg: appservice.HTTPClientConfig{
			BaseURL:  "http://127.0.0.1:8080",
			Username: "admin",
			Password: "change-me-please",
		},
	}

	got, err := clientConfigForMode(guiCfg.Mode, guiCfg, local)
	if err != nil {
		t.Fatalf("clientConfigForMode returned error: %v", err)
	}
	if got.BaseURL != "http://127.0.0.1:8080" {
		t.Fatalf("BaseURL = %q", got.BaseURL)
	}
	if got.Username != "admin" {
		t.Fatalf("Username = %q", got.Username)
	}
	if got.Password != "change-me-please" {
		t.Fatalf("Password = %q", got.Password)
	}
}

func TestClientConfigForLocalModeRequiresEmbeddedHTTP(t *testing.T) {
	_, err := clientConfigForMode(guiconfig.ModeLocal, guiconfig.Config{Mode: guiconfig.ModeLocal}, fakeLocalHTTPProvider{})
	if err == nil {
		t.Fatal("clientConfigForMode returned nil, want local service error")
	}
	if !strings.Contains(err.Error(), "本机服务 HTTP 未启用") {
		t.Fatalf("error = %q, want local HTTP message", err.Error())
	}
}

func TestClientConfigForRemoteModeUsesGUIConfig(t *testing.T) {
	guiCfg := guiconfig.Config{
		Mode:     guiconfig.ModeRemote,
		BaseURL:  "http://10.0.0.5:8080",
		Username: "alice",
		Password: "secret-password",
	}
	local := fakeLocalHTTPProvider{
		ok: true,
		cfg: appservice.HTTPClientConfig{
			BaseURL:  "http://127.0.0.1:8080",
			Username: "admin",
			Password: "change-me-please",
		},
	}

	got, err := clientConfigForMode(guiCfg.Mode, guiCfg, local)
	if err != nil {
		t.Fatalf("clientConfigForMode returned error: %v", err)
	}
	if got.BaseURL != guiCfg.BaseURL || got.Username != guiCfg.Username || got.Password != guiCfg.Password {
		t.Fatalf("client config = %#v, want remote GUI config", got)
	}
}

func TestClientConfigForUnknownModeReturnsError(t *testing.T) {
	unknownMode := "corrupt-mode"

	_, err := clientConfigForMode(unknownMode, guiconfig.Config{Mode: unknownMode}, fakeLocalHTTPProvider{})
	if err == nil {
		t.Fatal("clientConfigForMode returned nil, want unknown mode error")
	}
	if !strings.Contains(err.Error(), unknownMode) {
		t.Fatalf("error = %q, want message containing %q", err.Error(), unknownMode)
	}
}

func TestDownloadCenterSelectsLatestAndListsResources(t *testing.T) {
	testApp := fyneTest.NewApp()
	defer testApp.Quit()
	state := &guiState{client: apiclient.New("http://127.0.0.1:1", "admin", "password")}
	state.buildDownloadCenter()
	state.applyDownloadManifest(&apiclient.ReleaseManifest{
		LatestVersion: "2026.08.19",
		GeneratedAt:   time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC),
		Releases: []apiclient.Release{
			{Version: "2026.08.18", PublishedAt: time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC), Resources: []apiclient.Resource{{ID: "old", Kind: "component", Name: "old.zip", Size: 2}}},
			{Version: "2026.08.19", PublishedAt: time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC), Resources: []apiclient.Resource{
				{ID: "api", Kind: "component", Name: "api.zip", Size: 4},
				{ID: "bundle", Kind: "bundle", Name: "bundle.zip", Size: 8},
			}},
		},
	})
	if state.downloadVersion.Selected != "2026.08.19" {
		t.Fatalf("selected version = %q, want latest", state.downloadVersion.Selected)
	}
	if len(state.downloadVersion.Options) != 2 || state.downloadVersion.Options[0] != "2026.08.19" {
		t.Fatalf("version options = %#v, want newest first", state.downloadVersion.Options)
	}
	if len(state.downloadItems) != 2 || state.downloadItems[0].Kind != "bundle" {
		t.Fatalf("resources = %#v, want bundle first", state.downloadItems)
	}
	if state.downloadLatest.Text != "最新" || state.downloadEmpty.Text != "" {
		t.Fatalf("latest marker/empty text = %q/%q", state.downloadLatest.Text, state.downloadEmpty.Text)
	}
}

func TestDownloadCenterProgressCancelAndRetryStates(t *testing.T) {
	testApp := fyneTest.NewApp()
	defer testApp.Quit()
	state := &guiState{
		client:               apiclient.New("http://127.0.0.1:1", "admin", "password"),
		downloadLastVersion:  "2026.08.19",
		downloadLastResource: "api",
	}
	state.buildDownloadCenter()
	state.applyDownloadInfo(apiclient.DownloadInfo{
		TaskID: "task-1", State: "downloading", Version: "2026.08.19", ResourceID: "api",
		Name: "api.zip", Phase: "download", BytesDone: 512, TotalBytes: 1000, Percent: 51.2, SpeedBytesPerSecond: 2048,
		Destination: `D:\tools\download\api.zip`,
	})
	if state.downloadProgress.Value < 0.511 || state.downloadProgress.Value > 0.513 {
		t.Fatalf("progress value = %v, want 0.512", state.downloadProgress.Value)
	}
	if state.downloadCancel.Disabled() {
		t.Fatal("cancel button disabled during active task")
	}
	if !strings.Contains(state.downloadProgressText.Text, "51.20%") || !strings.Contains(state.downloadDestination.Text, "服务端路径") {
		t.Fatalf("progress/path labels = %q / %q", state.downloadProgressText.Text, state.downloadDestination.Text)
	}

	state.applyDownloadInfo(apiclient.DownloadInfo{
		TaskID: "task-1", State: "failed", Version: "2026.08.19", ResourceID: "api", Name: "api.zip",
		Error: "SHA-256 mismatch",
	})
	if !state.downloadCancel.Disabled() || state.downloadRetry.Disabled() {
		t.Fatalf("cancel/retry buttons after failure = %v/%v", state.downloadCancel.Disabled(), state.downloadRetry.Disabled())
	}
	if !strings.Contains(state.downloadStatus.Text, "重试") || !strings.Contains(state.downloadError.Text, "SHA-256") {
		t.Fatalf("failure labels = %q / %q", state.downloadStatus.Text, state.downloadError.Text)
	}

	state.applyDownloadInfo(apiclient.DownloadInfo{TaskID: "task-1", State: "cancelled", Version: "2026.08.19", ResourceID: "api"})
	if !strings.Contains(state.downloadStatus.Text, "取消") {
		t.Fatalf("cancelled status = %q", state.downloadStatus.Text)
	}
}
