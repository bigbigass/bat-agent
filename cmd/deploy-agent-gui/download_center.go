//go:build cgo

package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"github.com/liqixin/deploy-agent/internal/gui/apiclient"
)

func (s *guiState) buildDownloadCenter() fyne.CanvasObject {
	s.downloadVersion = widget.NewSelect(nil, s.applyDownloadVersion)
	s.downloadLatest = widget.NewLabel("")
	s.downloadUpdated = widget.NewLabel("清单尚未加载")
	s.downloadRefresh = widget.NewButton("刷新清单", s.refreshDownloadCenter)
	s.downloadStatus = widget.NewLabel("连接服务后将自动加载发布清单")
	s.downloadEmpty = widget.NewLabel("暂无资源")

	s.downloadResources = widget.NewList(
		func() int { return len(s.downloadItems) },
		func() fyne.CanvasObject { return widget.NewButton("", nil) },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			button := obj.(*widget.Button)
			if id < 0 || id >= len(s.downloadItems) {
				button.SetText("")
				button.Disable()
				return
			}
			resource := s.downloadItems[id]
			button.SetText(fmt.Sprintf("[%s]  %s    %s    SHA-256 %s    [下载]",
				resourceKindText(resource.Kind), resource.Name, formatBytes(resource.Size), resource.SHA256))
			button.OnTapped = func() { s.startDownload(s.downloadVersion.Selected, resource) }
			if s.canStartDownload() {
				button.Enable()
			} else {
				button.Disable()
			}
		},
	)

	s.downloadProgress = widget.NewProgressBar()
	s.downloadTaskResource = widget.NewLabel("当前资源：—")
	s.downloadPhase = widget.NewLabel("阶段：等待下载")
	s.downloadProgressText = widget.NewLabel("进度：0 B / 0 B（0.00%）")
	s.downloadSpeed = widget.NewLabel("速度：—")
	s.downloadDestination = widget.NewLabel("服务端路径：—")
	s.downloadDestination.Wrapping = fyne.TextWrapWord
	s.downloadError = widget.NewLabel("")
	s.downloadError.Wrapping = fyne.TextWrapWord
	s.downloadCancel = widget.NewButton("取消", s.cancelDownload)
	s.downloadRetry = widget.NewButton("重试", s.retryDownload)

	versionBar := container.NewBorder(nil, nil,
		container.NewHBox(widget.NewLabel("版本"), s.downloadVersion, s.downloadLatest),
		s.downloadRefresh,
		s.downloadUpdated,
	)
	resourcePane := container.NewBorder(
		container.NewVBox(widget.NewLabel("发布资源（点击一项开始下载）"), s.downloadEmpty),
		nil, nil, nil,
		s.downloadResources,
	)
	taskPane := widget.NewCard("当前任务", "下载、校验与落盘均在服务端执行",
		container.NewVBox(
			s.downloadTaskResource,
			s.downloadPhase,
			s.downloadProgress,
			container.NewHBox(s.downloadProgressText, s.downloadSpeed),
			s.downloadDestination,
			s.downloadError,
			container.NewHBox(s.downloadCancel, s.downloadRetry),
		),
	)
	content := container.NewVSplit(resourcePane, taskPane)
	content.Offset = 0.55
	notice := widget.NewLabel("说明：文件保存在服务端；远程模式不会把制品传回当前电脑。")
	notice.Wrapping = fyne.TextWrapWord
	s.updateDownloadControls()
	return container.NewBorder(container.NewVBox(versionBar, s.downloadStatus), notice, nil, nil, content)
}

func (s *guiState) refreshDownloadCenter() {
	s.loadDownloadReleases(true)
}

func (s *guiState) loadDownloadReleases(force bool) {
	client := s.client
	if client == nil {
		s.setDownloadStatus("请先连接服务")
		return
	}
	if isActiveDownloadState(s.downloadState) {
		s.setDownloadStatus("当前任务完成后才能刷新清单")
		return
	}
	s.downloadCatalogSeq++
	seq := s.downloadCatalogSeq
	s.downloadLoading = true
	s.setDownloadStatus("正在加载发布清单...")
	s.setDownloadEmpty("加载中...")
	s.updateDownloadControls()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var manifest *apiclient.ReleaseManifest
		var err error
		if force {
			manifest, err = client.RefreshReleases(ctx)
		} else {
			manifest, err = client.Releases(ctx)
		}
		fyne.Do(func() {
			if seq != s.downloadCatalogSeq || client != s.client {
				return
			}
			s.downloadLoading = false
			if err != nil {
				if s.downloadManifest == nil {
					s.downloadItems = nil
					if s.downloadResources != nil {
						s.downloadResources.Refresh()
					}
					s.setDownloadEmpty("清单不可用")
					s.setDownloadStatus("发布清单加载失败: " + err.Error())
				} else {
					s.setDownloadStatus("清单刷新失败，继续使用上一份清单: " + err.Error())
				}
				s.updateDownloadControls()
				return
			}
			s.applyDownloadManifest(manifest)
		})
	}()
}

func (s *guiState) applyDownloadManifest(manifest *apiclient.ReleaseManifest) {
	if manifest == nil {
		s.downloadManifest = nil
		s.downloadItems = nil
		s.setDownloadEmpty("清单不可用")
		s.updateDownloadControls()
		return
	}
	copyManifest := *manifest
	copyManifest.Releases = append([]apiclient.Release(nil), manifest.Releases...)
	sort.SliceStable(copyManifest.Releases, func(i, j int) bool {
		return copyManifest.Releases[i].PublishedAt.After(copyManifest.Releases[j].PublishedAt)
	})
	s.downloadManifest = &copyManifest
	versions := make([]string, 0, len(copyManifest.Releases))
	for _, release := range copyManifest.Releases {
		versions = append(versions, release.Version)
	}
	if s.downloadVersion == nil {
		return
	}
	s.downloadVersion.Options = versions
	selected := copyManifest.LatestVersion
	if !containsString(versions, selected) {
		selected = ""
	}
	if selected == "" {
		s.downloadVersion.ClearSelected()
		s.applyDownloadVersion("")
	} else {
		s.downloadVersion.SetSelected(selected)
		s.applyDownloadVersion(selected)
	}
	s.downloadVersion.Refresh()
	if s.downloadUpdated != nil {
		s.downloadUpdated.SetText("清单更新时间：" + copyManifest.GeneratedAt.Local().Format("2006-01-02 15:04:05"))
	}
	if selected == "" && len(copyManifest.Releases) > 0 {
		s.setDownloadStatus("清单未提供有效的 latestVersion")
	} else {
		s.setDownloadStatus(fmt.Sprintf("已加载 %d 个版本", len(copyManifest.Releases)))
	}
	s.updateDownloadControls()
}

func (s *guiState) applyDownloadVersion(version string) {
	s.downloadItems = nil
	if s.downloadManifest != nil {
		for _, release := range s.downloadManifest.Releases {
			if release.Version == version {
				s.downloadItems = append([]apiclient.Resource(nil), release.Resources...)
				break
			}
		}
	}
	sort.SliceStable(s.downloadItems, func(i, j int) bool {
		if s.downloadItems[i].Kind != s.downloadItems[j].Kind {
			return s.downloadItems[i].Kind == "bundle"
		}
		return s.downloadItems[i].Name < s.downloadItems[j].Name
	})
	if s.downloadLatest != nil {
		if s.downloadManifest != nil && version != "" && version == s.downloadManifest.LatestVersion {
			s.downloadLatest.SetText("最新")
		} else {
			s.downloadLatest.SetText("")
		}
	}
	if len(s.downloadItems) == 0 {
		if s.downloadManifest == nil {
			s.setDownloadEmpty("清单不可用")
		} else {
			s.setDownloadEmpty("该版本暂无资源")
		}
	} else {
		s.setDownloadEmpty("")
	}
	if s.downloadResources != nil {
		s.downloadResources.Refresh()
	}
	s.updateDownloadControls()
}

func (s *guiState) startDownload(version string, resource apiclient.Resource) {
	client := s.client
	if client == nil {
		s.setDownloadStatus("请先连接服务")
		return
	}
	if strings.TrimSpace(version) == "" || resource.ID == "" {
		s.setDownloadStatus("请选择版本和资源")
		return
	}
	if isActiveDownloadState(s.downloadState) {
		s.setDownloadStatus("已有下载任务正在进行")
		return
	}
	s.stopDownloadPolling()
	seq := s.downloadTaskSeq
	s.downloadState = "queued"
	s.downloadTaskID = ""
	s.downloadLastVersion = version
	s.downloadLastResource = resource.ID
	s.downloadCancelling = false
	s.downloadTaskResource.SetText("当前资源：" + resource.Name)
	s.downloadPhase.SetText("阶段：正在创建任务")
	s.downloadProgress.SetValue(0)
	s.downloadProgressText.SetText("进度：0 B / " + formatBytes(resource.Size) + "（0.00%）")
	s.downloadSpeed.SetText("速度：—")
	s.downloadDestination.SetText("服务端路径：等待服务端分配")
	s.downloadError.SetText("")
	s.setDownloadStatus("正在创建服务端下载任务...")
	s.updateDownloadControls()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		info, err := client.StartDownload(ctx, version, resource.ID)
		cancel()
		fyne.Do(func() {
			if seq != s.downloadTaskSeq || client != s.client {
				return
			}
			if err != nil {
				s.applyDownloadFailure(err.Error())
				return
			}
			if info.TaskID == "" {
				s.applyDownloadFailure("服务端未返回任务 ID")
				return
			}
			if info.State == "" {
				info.State = "queued"
				info.Version = version
				info.ResourceID = resource.ID
				info.Name = resource.Name
				info.Kind = resource.Kind
				info.TotalBytes = resource.Size
				info.Phase = "queued"
			}
			s.downloadTaskID = info.TaskID
			s.applyDownloadInfo(info)
			if !isTerminalDownloadState(info.State) {
				s.beginDownloadPolling(client, info.TaskID, seq)
			}
		})
	}()
}

func (s *guiState) beginDownloadPolling(client *apiclient.Client, taskID string, seq int) {
	ctx, cancel := context.WithCancel(context.Background())
	s.downloadPollCancel = cancel
	go func() {
		ticker := time.NewTicker(750 * time.Millisecond)
		defer ticker.Stop()
		for {
			requestCtx, requestCancel := context.WithTimeout(ctx, 5*time.Second)
			info, err := client.DownloadStatus(requestCtx, taskID)
			requestCancel()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				fyne.Do(func() {
					if seq == s.downloadTaskSeq && taskID == s.downloadTaskID && client == s.client {
						if httpErr, ok := err.(apiclient.HTTPError); ok && httpErr.StatusCode == http.StatusNotFound {
							s.applyDownloadFailure("服务端任务不存在，可能已重启")
						} else {
							s.setDownloadStatus("任务状态查询失败，正在重试: " + err.Error())
						}
					}
				})
				if httpErr, ok := err.(apiclient.HTTPError); ok && httpErr.StatusCode == http.StatusNotFound {
					return
				}
			} else {
				terminal := isTerminalDownloadState(info.State)
				fyne.Do(func() {
					if seq == s.downloadTaskSeq && taskID == s.downloadTaskID && client == s.client {
						s.applyDownloadInfo(info)
					}
				})
				if terminal {
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *guiState) applyDownloadInfo(info apiclient.DownloadInfo) {
	if info.TaskID != "" {
		s.downloadTaskID = info.TaskID
	}
	s.downloadState = info.State
	if info.Version != "" {
		s.downloadLastVersion = info.Version
	}
	if info.ResourceID != "" {
		s.downloadLastResource = info.ResourceID
	}
	name := info.Name
	if name == "" {
		name = info.ResourceID
	}
	s.downloadTaskResource.SetText("当前资源：" + name)
	s.downloadPhase.SetText("阶段：" + downloadPhaseText(info.State, info.Phase))
	progress := info.Percent / 100
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	s.downloadProgress.SetValue(progress)
	s.downloadProgressText.SetText(fmt.Sprintf("进度：%s / %s（%.2f%%）", formatBytes(info.BytesDone), formatBytes(info.TotalBytes), info.Percent))
	if info.SpeedBytesPerSecond > 0 && info.State == "downloading" {
		s.downloadSpeed.SetText("速度：" + formatBytes(int64(info.SpeedBytesPerSecond)) + "/s")
	} else {
		s.downloadSpeed.SetText("速度：—")
	}
	if info.Destination == "" {
		s.downloadDestination.SetText("服务端路径：—")
	} else {
		s.downloadDestination.SetText("服务端路径：" + info.Destination)
	}
	if info.Error == "" {
		s.downloadError.SetText("")
	} else {
		s.downloadError.SetText("错误：" + info.Error)
	}

	switch info.State {
	case "queued", "downloading":
		s.setDownloadStatus("资源正在服务端下载")
	case "verifying":
		s.setDownloadStatus("下载完成，正在校验大小和 SHA-256")
	case "completed":
		s.setDownloadStatus("下载完成，大小和 SHA-256 校验通过")
	case "failed":
		s.setDownloadStatus("下载失败，可点击重试")
	case "cancelled":
		s.setDownloadStatus("下载已取消，临时文件已清理")
	default:
		s.setDownloadStatus("任务状态：" + info.State)
	}
	if isTerminalDownloadState(info.State) {
		s.downloadCancelling = false
		if s.downloadPollCancel != nil {
			s.downloadPollCancel()
			s.downloadPollCancel = nil
		}
	}
	s.updateDownloadControls()
}

func (s *guiState) applyDownloadFailure(message string) {
	s.downloadState = "failed"
	s.downloadCancelling = false
	if s.downloadPollCancel != nil {
		s.downloadPollCancel()
		s.downloadPollCancel = nil
	}
	s.downloadPhase.SetText("阶段：失败")
	s.downloadError.SetText("错误：" + message)
	s.setDownloadStatus("下载失败，可点击重试")
	s.updateDownloadControls()
}

func (s *guiState) cancelDownload() {
	client := s.client
	taskID := s.downloadTaskID
	seq := s.downloadTaskSeq
	if client == nil || taskID == "" || !isActiveDownloadState(s.downloadState) || s.downloadCancelling {
		return
	}
	s.downloadCancelling = true
	s.setDownloadStatus("正在请求服务端取消并清理临时文件...")
	s.updateDownloadControls()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := client.CancelDownload(ctx, taskID)
		cancel()
		fyne.Do(func() {
			if seq != s.downloadTaskSeq || taskID != s.downloadTaskID || client != s.client {
				return
			}
			if err != nil {
				s.downloadCancelling = false
				s.downloadError.SetText("取消失败：" + err.Error())
				s.setDownloadStatus("取消请求失败，任务状态仍会继续更新")
				s.updateDownloadControls()
				return
			}
			// Cancellation is terminal from the page's perspective. Invalidate
			// any in-flight status response so an old downloading snapshot cannot
			// overwrite the cancelled state.
			s.stopDownloadPolling()
			s.downloadState = "cancelled"
			s.downloadCancelling = false
			s.downloadPhase.SetText("阶段：已取消")
			s.setDownloadStatus("下载已取消，服务端正在清理临时文件...")
			s.updateDownloadControls()
		})
	}()
}

func (s *guiState) retryDownload() {
	if s.client == nil || s.downloadLastVersion == "" || s.downloadLastResource == "" {
		s.setDownloadStatus("没有可重试的任务")
		return
	}
	resource, ok := s.findDownloadResource(s.downloadLastVersion, s.downloadLastResource)
	if !ok {
		s.setDownloadStatus("原资源已不在当前清单中，请刷新清单")
		return
	}
	s.startDownload(s.downloadLastVersion, resource)
}

func (s *guiState) findDownloadResource(version, resourceID string) (apiclient.Resource, bool) {
	if s.downloadManifest == nil {
		return apiclient.Resource{}, false
	}
	for _, release := range s.downloadManifest.Releases {
		if release.Version != version {
			continue
		}
		for _, resource := range release.Resources {
			if resource.ID == resourceID {
				return resource, true
			}
		}
	}
	return apiclient.Resource{}, false
}

func (s *guiState) resetDownloadCenter(status string) {
	s.downloadCatalogSeq++
	s.stopDownloadPolling()
	s.downloadManifest = nil
	s.downloadItems = nil
	s.downloadTaskID = ""
	s.downloadState = ""
	s.downloadLastVersion = ""
	s.downloadLastResource = ""
	s.downloadLoading = false
	s.downloadCancelling = false
	if s.downloadVersion != nil {
		s.downloadVersion.Options = nil
		s.downloadVersion.ClearSelected()
		s.downloadVersion.Refresh()
	}
	if s.downloadResources != nil {
		s.downloadResources.Refresh()
	}
	if s.downloadLatest != nil {
		s.downloadLatest.SetText("")
	}
	if s.downloadUpdated != nil {
		s.downloadUpdated.SetText("清单尚未加载")
	}
	if s.downloadTaskResource != nil {
		s.downloadTaskResource.SetText("当前资源：—")
	}
	if s.downloadPhase != nil {
		s.downloadPhase.SetText("阶段：等待下载")
	}
	if s.downloadProgress != nil {
		s.downloadProgress.SetValue(0)
	}
	if s.downloadProgressText != nil {
		s.downloadProgressText.SetText("进度：0 B / 0 B（0.00%）")
	}
	if s.downloadSpeed != nil {
		s.downloadSpeed.SetText("速度：—")
	}
	if s.downloadDestination != nil {
		s.downloadDestination.SetText("服务端路径：—")
	}
	if s.downloadError != nil {
		s.downloadError.SetText("")
	}
	s.setDownloadEmpty("暂无资源")
	s.setDownloadStatus(status)
	s.updateDownloadControls()
}

func (s *guiState) stopDownloadPolling() {
	if s.downloadPollCancel != nil {
		s.downloadPollCancel()
		s.downloadPollCancel = nil
	}
	s.downloadTaskSeq++
}

func (s *guiState) canStartDownload() bool {
	return s.client != nil && s.downloadManifest != nil && s.downloadVersion != nil && s.downloadVersion.Selected != "" && !s.downloadLoading && !isActiveDownloadState(s.downloadState)
}

func (s *guiState) updateDownloadControls() {
	active := isActiveDownloadState(s.downloadState)
	if s.downloadVersion != nil {
		if s.client != nil && s.downloadManifest != nil && !s.downloadLoading && !active {
			s.downloadVersion.Enable()
		} else {
			s.downloadVersion.Disable()
		}
	}
	if s.downloadRefresh != nil {
		if s.client != nil && !s.downloadLoading && !active {
			s.downloadRefresh.Enable()
		} else {
			s.downloadRefresh.Disable()
		}
	}
	if s.downloadCancel != nil {
		if s.client != nil && s.downloadTaskID != "" && active && !s.downloadCancelling {
			s.downloadCancel.Enable()
		} else {
			s.downloadCancel.Disable()
		}
	}
	if s.downloadRetry != nil {
		if s.client != nil && (s.downloadState == "failed" || s.downloadState == "cancelled") && s.downloadLastVersion != "" && s.downloadLastResource != "" {
			s.downloadRetry.Enable()
		} else {
			s.downloadRetry.Disable()
		}
	}
	if s.downloadResources != nil {
		s.downloadResources.Refresh()
	}
}

func (s *guiState) setDownloadStatus(status string) {
	if s.downloadStatus != nil {
		s.downloadStatus.SetText(status)
	}
}

func (s *guiState) setDownloadEmpty(text string) {
	if s.downloadEmpty != nil {
		s.downloadEmpty.SetText(text)
	}
}

func resourceKindText(kind string) string {
	if kind == "bundle" {
		return "完整包"
	}
	return "组件"
}

func formatBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	size := float64(value)
	unit := "B"
	for _, next := range units {
		size /= 1024
		unit = next
		if size < 1024 {
			break
		}
	}
	return fmt.Sprintf("%.2f %s", size, unit)
}

func downloadPhaseText(state, phase string) string {
	switch state {
	case "queued":
		return "排队"
	case "downloading":
		return "下载"
	case "verifying":
		return "校验大小与 SHA-256"
	case "completed":
		return "完成"
	case "failed":
		return "失败"
	case "cancelled":
		return "已取消"
	}
	if phase == "" {
		return "未知"
	}
	return phase
}

func isActiveDownloadState(state string) bool {
	return state == "queued" || state == "downloading" || state == "verifying"
}

func isTerminalDownloadState(state string) bool {
	return state == "completed" || state == "failed" || state == "cancelled"
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
